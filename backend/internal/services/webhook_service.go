package services

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/crypto"
	"github.com/forgelab/backend/internal/models"
)

var (
	ErrInvalidWebhookSignature  = errors.New("invalid webhook signature")
	ErrWebhookSecretUnavailable = errors.New("webhook secret unavailable or decryption failed")
	ErrDeployQueueUnavailable   = errors.New("deployment queue unavailable")
)

// DeploymentEnqueuer abstracts enqueueing newly created deployments onto Redis.
type DeploymentEnqueuer interface {
	EnqueueDeployment(ctx context.Context, deploymentID uuid.UUID) error
}

// WebhookConfigView represents the sanitized push-to-deploy webhook settings returned via API.
type WebhookConfigView struct {
	AutoDeploy       bool   `json:"auto_deploy"`
	AutoDeployBranch string `json:"auto_deploy_branch"`
	HasWebhookSecret bool   `json:"has_webhook_secret"`
	WebhookURL       string `json:"webhook_url"`
	WebhookSecret    string `json:"webhook_secret,omitempty"` // Populated ONLY once when generated or rotated
}

// UpdateWebhookConfigInput holds requested changes to a project's push-to-deploy configuration.
type UpdateWebhookConfigInput struct {
	AutoDeploy       *bool   `json:"auto_deploy,omitempty"`
	AutoDeployBranch *string `json:"auto_deploy_branch,omitempty"`
	RegenerateSecret bool    `json:"regenerate_secret,omitempty"`
}

// WebhookHandleResult describes the outcome of processing an incoming GitHub webhook.
type WebhookHandleResult struct {
	OK           bool       `json:"ok"`
	Triggered    bool       `json:"triggered"`
	Ignored      bool       `json:"ignored,omitempty"`
	Duplicate    bool       `json:"duplicate,omitempty"`
	Reason       string     `json:"reason,omitempty"`
	Event        string     `json:"event"`
	ProjectID    *uuid.UUID `json:"project_id,omitempty"`
	DeploymentID *uuid.UUID `json:"deployment_id,omitempty"`
	CommitSHA    string     `json:"commit_sha,omitempty"`
	Branch       string     `json:"branch,omitempty"`
}

type githubPushPayload struct {
	Ref        string `json:"ref"`
	Before     string `json:"before"`
	After      string `json:"after"`
	Deleted    bool   `json:"deleted"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type memWebhookEntry struct {
	project         *models.Project
	autoDeploy      bool
	branch          string
	encryptedSecret []byte
}

// WebhookService manages GitHub webhook configuration, HMAC-SHA256 verification, replay prevention,
// and automatic deployment creation/enqueueing on branch pushes.
type WebhookService struct {
	db                *pgxpool.Pool
	encryptor         *crypto.Encryptor
	projectService    *ProjectService
	deploymentService *DeploymentService
	deployQueue       DeploymentEnqueuer
	publicBaseURL     string

	memMu         sync.RWMutex
	memConfigs    map[uuid.UUID]*memWebhookEntry
	memDeliveries map[string]time.Time
}

// NewWebhookService creates a new WebhookService.
func NewWebhookService(
	db *pgxpool.Pool,
	encryptor *crypto.Encryptor,
	projectService *ProjectService,
	deploymentService *DeploymentService,
	deployQueue DeploymentEnqueuer,
	publicBaseURL string,
) *WebhookService {
	return &WebhookService{
		db:                db,
		encryptor:         encryptor,
		projectService:    projectService,
		deploymentService: deploymentService,
		deployQueue:       deployQueue,
		publicBaseURL:     strings.TrimRight(strings.TrimSpace(publicBaseURL), "/"),
		memConfigs:        make(map[uuid.UUID]*memWebhookEntry),
		memDeliveries:     make(map[string]time.Time),
	}
}

// SetDeployQueue sets the deployment queue enqueuer.
func (s *WebhookService) SetDeployQueue(q DeploymentEnqueuer) {
	s.deployQueue = q
}

// VerifyGitHubSignature validates GitHub's `X-Hub-Signature-256: sha256=<hex>` header
// against the raw payload using constant-time HMAC-SHA256 comparison. Fails closed on missing inputs.
func VerifyGitHubSignature(payload []byte, signatureHeader, secret string) bool {
	if secret == "" {
		return false
	}
	sigHeader := strings.TrimSpace(signatureHeader)
	const prefix = "sha256="
	if !strings.HasPrefix(sigHeader, prefix) {
		return false
	}
	sigHex := strings.TrimPrefix(sigHeader, prefix)
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil || len(sigBytes) != sha256.Size {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	expected := mac.Sum(nil)

	return hmac.Equal(sigBytes, expected)
}

func generateWebhookSecretHex() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate random webhook secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func (s *WebhookService) buildWebhookURL(projectID uuid.UUID, requestBaseURL string) string {
	base := s.publicBaseURL
	if strings.TrimSpace(requestBaseURL) != "" {
		base = strings.TrimRight(strings.TrimSpace(requestBaseURL), "/")
	}
	if base == "" {
		return fmt.Sprintf("/api/webhooks/github?project_id=%s", projectID.String())
	}
	return fmt.Sprintf("%s/api/webhooks/github?project_id=%s", base, projectID.String())
}

// RegisterProjectForTest registers an in-memory project record when db == nil in unit tests.
func (s *WebhookService) RegisterProjectForTest(project *models.Project) {
	if project == nil || s.db != nil {
		return
	}
	s.memMu.Lock()
	defer s.memMu.Unlock()
	cp := *project
	entry, ok := s.memConfigs[project.ID]
	if !ok {
		entry = &memWebhookEntry{}
		s.memConfigs[project.ID] = entry
	}
	entry.project = &cp
	entry.autoDeploy = cp.AutoDeploy
	entry.branch = cp.AutoDeployBranch
	if len(cp.EncryptedWebhookSecret) > 0 {
		entry.encryptedSecret = append([]byte(nil), cp.EncryptedWebhookSecret...)
	}
}

// GetWebhookConfig returns sanitized push-to-deploy webhook settings for a project.
func (s *WebhookService) GetWebhookConfig(ctx context.Context, project *models.Project, requestBaseURL string) (*WebhookConfigView, error) {
	if project == nil || project.ID == uuid.Nil {
		return nil, fmt.Errorf("%w: valid project is required", ErrValidationFailed)
	}

	view := &WebhookConfigView{
		AutoDeploy:       project.AutoDeploy,
		AutoDeployBranch: project.AutoDeployBranch,
		HasWebhookSecret: project.HasWebhookSecret,
		WebhookURL:       s.buildWebhookURL(project.ID, requestBaseURL),
	}

	if s.db == nil {
		s.memMu.RLock()
		if entry, ok := s.memConfigs[project.ID]; ok {
			view.AutoDeploy = entry.autoDeploy
			view.AutoDeployBranch = entry.branch
			view.HasWebhookSecret = len(entry.encryptedSecret) > 0
		}
		s.memMu.RUnlock()
		if view.AutoDeployBranch == "" {
			view.AutoDeployBranch = fallbackBranch(project.Branch)
		}
		return view, nil
	}

	var autoDeploy bool
	var branch string
	var hasSecret bool
	err := s.db.QueryRow(ctx,
		`SELECT COALESCE(auto_deploy, false), COALESCE(auto_deploy_branch, ''), (encrypted_webhook_secret IS NOT NULL)
		 FROM projects WHERE id = $1`,
		project.ID,
	).Scan(&autoDeploy, &branch, &hasSecret)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrProjectNotFound
		}
		return nil, fmt.Errorf("failed to load webhook configuration: %w", err)
	}

	view.AutoDeploy = autoDeploy
	view.AutoDeployBranch = branch
	if view.AutoDeployBranch == "" {
		view.AutoDeployBranch = fallbackBranch(project.Branch)
	}
	view.HasWebhookSecret = hasSecret
	return view, nil
}

// UpdateWebhookConfig updates push-to-deploy settings and creates or rotates an AES-GCM encrypted webhook secret.
func (s *WebhookService) UpdateWebhookConfig(ctx context.Context, project *models.Project, input UpdateWebhookConfigInput, requestBaseURL string) (*WebhookConfigView, error) {
	if project == nil || project.ID == uuid.Nil {
		return nil, fmt.Errorf("%w: valid project is required", ErrValidationFailed)
	}

	current, err := s.GetWebhookConfig(ctx, project, requestBaseURL)
	if err != nil {
		return nil, err
	}

	nextAuto := current.AutoDeploy
	if input.AutoDeploy != nil {
		nextAuto = *input.AutoDeploy
	}
	nextBranch := current.AutoDeployBranch
	if input.AutoDeployBranch != nil {
		trimmed := strings.TrimSpace(*input.AutoDeployBranch)
		if strings.ContainsAny(trimmed, " \t\r\n") {
			return nil, fmt.Errorf("%w: target branch must not contain whitespace", ErrValidationFailed)
		}
		nextBranch = trimmed
	}
	if nextBranch == "" {
		nextBranch = fallbackBranch(project.Branch)
	}

	needNewSecret := input.RegenerateSecret || (nextAuto && !current.HasWebhookSecret)
	var plaintextSecret string
	var encryptedSecret []byte

	if needNewSecret {
		if s.encryptor == nil {
			return nil, fmt.Errorf("%w: encryption key unavailable for webhook secret storage", ErrWebhookSecretUnavailable)
		}
		plaintextSecret, err = generateWebhookSecretHex()
		if err != nil {
			return nil, err
		}
		encryptedSecret, err = s.encryptor.Encrypt([]byte(plaintextSecret))
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt webhook secret: %w", err)
		}
	}

	if s.db == nil {
		s.memMu.Lock()
		entry, ok := s.memConfigs[project.ID]
		if !ok {
			cp := *project
			entry = &memWebhookEntry{project: &cp}
			s.memConfigs[project.ID] = entry
		}
		entry.autoDeploy = nextAuto
		entry.branch = nextBranch
		if needNewSecret {
			entry.encryptedSecret = encryptedSecret
		}
		if entry.project != nil {
			entry.project.AutoDeploy = nextAuto
			entry.project.AutoDeployBranch = nextBranch
			entry.project.HasWebhookSecret = len(entry.encryptedSecret) > 0
			entry.project.EncryptedWebhookSecret = entry.encryptedSecret
		}
		hasSecret := len(entry.encryptedSecret) > 0
		s.memMu.Unlock()

		return &WebhookConfigView{
			AutoDeploy:       nextAuto,
			AutoDeployBranch: nextBranch,
			HasWebhookSecret: hasSecret,
			WebhookURL:       s.buildWebhookURL(project.ID, requestBaseURL),
			WebhookSecret:    plaintextSecret,
		}, nil
	}

	if needNewSecret {
		_, err = s.db.Exec(ctx,
			`UPDATE projects
			 SET auto_deploy = $1, auto_deploy_branch = $2, encrypted_webhook_secret = $3, updated_at = NOW()
			 WHERE id = $4`,
			nextAuto, nextBranch, encryptedSecret, project.ID,
		)
	} else {
		_, err = s.db.Exec(ctx,
			`UPDATE projects
			 SET auto_deploy = $1, auto_deploy_branch = $2, updated_at = NOW()
			 WHERE id = $3`,
			nextAuto, nextBranch, project.ID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to update webhook configuration: %w", err)
	}

	hasSecret := current.HasWebhookSecret || needNewSecret
	return &WebhookConfigView{
		AutoDeploy:       nextAuto,
		AutoDeployBranch: nextBranch,
		HasWebhookSecret: hasSecret,
		WebhookURL:       s.buildWebhookURL(project.ID, requestBaseURL),
		WebhookSecret:    plaintextSecret,
	}, nil
}

func fallbackBranch(b string) string {
	trimmed := strings.TrimSpace(b)
	if trimmed == "" {
		return "main"
	}
	return trimmed
}

type webhookCandidate struct {
	projectID       uuid.UUID
	encryptedSecret []byte
}

func (s *WebhookService) findVerifiedProject(ctx context.Context, projectIDHint *uuid.UUID, repoFullName, signatureHeader string, payload []byte) (*models.Project, error) {
	if s.encryptor == nil {
		return nil, ErrWebhookSecretUnavailable
	}

	var candidates []webhookCandidate
	if s.db == nil {
		s.memMu.RLock()
		if projectIDHint != nil && *projectIDHint != uuid.Nil {
			if entry, ok := s.memConfigs[*projectIDHint]; ok {
				candidates = append(candidates, webhookCandidate{
					projectID:       *projectIDHint,
					encryptedSecret: entry.encryptedSecret,
				})
			}
		} else {
			normRepo := strings.ToLower(strings.TrimSpace(repoFullName))
			for id, entry := range s.memConfigs {
				if entry.project == nil || len(entry.encryptedSecret) == 0 {
					continue
				}
				srcRef := strings.ToLower(strings.TrimSpace(entry.project.SourceReference))
				repoPath := strings.ToLower(strings.TrimSpace(entry.project.RepositoryPath))
				if normRepo != "" && (srcRef == normRepo || repoPath == normRepo) {
					candidates = append(candidates, webhookCandidate{
						projectID:       id,
						encryptedSecret: entry.encryptedSecret,
					})
				}
			}
		}
		s.memMu.RUnlock()
	} else {
		if projectIDHint != nil && *projectIDHint != uuid.Nil {
			var enc []byte
			err := s.db.QueryRow(ctx,
				`SELECT encrypted_webhook_secret FROM projects WHERE id = $1`,
				*projectIDHint,
			).Scan(&enc)
			if err == nil && len(enc) > 0 {
				candidates = append(candidates, webhookCandidate{
					projectID:       *projectIDHint,
					encryptedSecret: enc,
				})
			}
		} else if trimmedRepo := strings.TrimSpace(repoFullName); trimmedRepo != "" {
			rows, err := s.db.Query(ctx,
				`SELECT id, encrypted_webhook_secret
				 FROM projects
				 WHERE encrypted_webhook_secret IS NOT NULL
				   AND (LOWER(source_reference) = LOWER($1) OR LOWER(repository_path) = LOWER($1))`,
				trimmedRepo,
			)
			if err != nil {
				return nil, fmt.Errorf("failed to query webhook target projects: %w", err)
			}
			defer rows.Close()
			for rows.Next() {
				var c webhookCandidate
				if scanErr := rows.Scan(&c.projectID, &c.encryptedSecret); scanErr == nil && len(c.encryptedSecret) > 0 {
					candidates = append(candidates, c)
				}
			}
		}
	}

	if len(candidates) == 0 {
		return nil, ErrInvalidWebhookSignature
	}

	for _, cand := range candidates {
		if len(cand.encryptedSecret) == 0 {
			continue
		}
		secretBytes, err := s.encryptor.Decrypt(cand.encryptedSecret)
		if err != nil {
			slog.Warn("failed to decrypt project webhook secret", "project_id", cand.projectID, "error", err)
			continue
		}
		if VerifyGitHubSignature(payload, signatureHeader, string(secretBytes)) {
			// Matched! Fetch full project record.
			if s.db == nil {
				s.memMu.RLock()
				entry, ok := s.memConfigs[cand.projectID]
				s.memMu.RUnlock()
				if ok && entry.project != nil {
					cp := *entry.project
					cp.AutoDeploy = entry.autoDeploy
					cp.AutoDeployBranch = entry.branch
					cp.HasWebhookSecret = len(entry.encryptedSecret) > 0
					return &cp, nil
				}
			}
			if s.projectService != nil {
				return s.projectService.GetProjectByIDWithoutOwnership(ctx, cand.projectID)
			}
		}
	}

	return nil, ErrInvalidWebhookSignature
}

func (s *WebhookService) recordDeliveryIfNew(ctx context.Context, deliveryID string, projectID uuid.UUID, eventType string) (bool, error) {
	if s.db == nil {
		s.memMu.Lock()
		defer s.memMu.Unlock()
		if _, exists := s.memDeliveries[deliveryID]; exists {
			return false, nil
		}
		s.memDeliveries[deliveryID] = time.Now().UTC()
		return true, nil
	}

	tag, err := s.db.Exec(ctx,
		`INSERT INTO webhook_deliveries (delivery_id, project_id, event_type)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (delivery_id) DO NOTHING`,
		deliveryID, projectID, eventType,
	)
	if err != nil {
		return false, fmt.Errorf("failed to record webhook delivery: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// HandleGitHubWebhook validates a GitHub webhook request, enforces signature verification BEFORE
// replay deduplication, and triggers an automated commit-pinned deployment on matching branch pushes.
func (s *WebhookService) HandleGitHubWebhook(
	ctx context.Context,
	projectIDHint *uuid.UUID,
	eventType string,
	deliveryID string,
	signatureHeader string,
	payload []byte,
) (*WebhookHandleResult, error) {
	trimmedDelivery := strings.TrimSpace(deliveryID)
	if trimmedDelivery == "" {
		return nil, fmt.Errorf("%w: missing X-GitHub-Delivery header", ErrValidationFailed)
	}
	if strings.TrimSpace(signatureHeader) == "" {
		return nil, ErrInvalidWebhookSignature
	}

	var push githubPushPayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &push); err != nil {
			return nil, fmt.Errorf("%w: invalid JSON webhook payload", ErrValidationFailed)
		}
	}

	// Verify HMAC signature BEFORE recording delivery_id to prevent forged delivery-ID poisoning
	project, err := s.findVerifiedProject(ctx, projectIDHint, push.Repository.FullName, signatureHeader, payload)
	if err != nil {
		return nil, err
	}

	// Deduplicate replay deliveries
	isNew, err := s.recordDeliveryIfNew(ctx, trimmedDelivery, project.ID, eventType)
	if err != nil {
		return nil, err
	}
	if !isNew {
		return &WebhookHandleResult{
			OK:        true,
			Triggered: false,
			Ignored:   true,
			Duplicate: true,
			Reason:    "duplicate webhook delivery ignored",
			Event:     eventType,
			ProjectID: &project.ID,
		}, nil
	}

	eventLower := strings.ToLower(strings.TrimSpace(eventType))
	if eventLower == "ping" {
		return &WebhookHandleResult{
			OK:        true,
			Triggered: false,
			Event:     "ping",
			ProjectID: &project.ID,
		}, nil
	}

	if eventLower != "push" {
		return &WebhookHandleResult{
			OK:        true,
			Triggered: false,
			Ignored:   true,
			Reason:    fmt.Sprintf("unsupported event type %q ignored", eventType),
			Event:     eventType,
			ProjectID: &project.ID,
		}, nil
	}

	if !project.AutoDeploy {
		return &WebhookHandleResult{
			OK:        true,
			Triggered: false,
			Ignored:   true,
			Reason:    "auto-deploy is disabled for this project",
			Event:     "push",
			ProjectID: &project.ID,
		}, nil
	}

	if !strings.HasPrefix(push.Ref, "refs/heads/") {
		return &WebhookHandleResult{
			OK:        true,
			Triggered: false,
			Ignored:   true,
			Reason:    fmt.Sprintf("non-branch ref %q ignored", push.Ref),
			Event:     "push",
			ProjectID: &project.ID,
		}, nil
	}

	pushedBranch := strings.TrimPrefix(push.Ref, "refs/heads/")
	targetBranch := strings.TrimSpace(project.AutoDeployBranch)
	if targetBranch == "" {
		targetBranch = fallbackBranch(project.Branch)
	}
	if pushedBranch != targetBranch {
		return &WebhookHandleResult{
			OK:        true,
			Triggered: false,
			Ignored:   true,
			Reason:    fmt.Sprintf("push to branch %q ignored (tracking %q)", pushedBranch, targetBranch),
			Event:     "push",
			ProjectID: &project.ID,
			Branch:    pushedBranch,
		}, nil
	}

	if push.Deleted || push.After == "" || push.After == strings.Repeat("0", 40) {
		return &WebhookHandleResult{
			OK:        true,
			Triggered: false,
			Ignored:   true,
			Reason:    "branch deletion or empty commit ignored",
			Event:     "push",
			ProjectID: &project.ID,
			Branch:    pushedBranch,
		}, nil
	}

	commitSHA := strings.ToLower(strings.TrimSpace(push.After))
	if !isValidGitCommitSHA(commitSHA) {
		return nil, fmt.Errorf("%w: webhook push 'after' must be a valid 40-character commit SHA", ErrValidationFailed)
	}

	if s.deploymentService == nil {
		return nil, errors.New("deployment service unavailable")
	}
	if s.deployQueue == nil {
		return nil, ErrDeployQueueUnavailable
	}

	dep, err := s.deploymentService.CreateDeploymentWithCommit(ctx, project, &commitSHA)
	if err != nil {
		return nil, err
	}

	if err := s.deployQueue.EnqueueDeployment(ctx, dep.ID); err != nil {
		failReason := fmt.Sprintf("Failed to enqueue push-to-deploy deployment: %v", err)
		_ = s.deploymentService.UpdateDeploymentStatus(ctx, dep.ID, models.DeployStatusFailed, &failReason)
		return nil, fmt.Errorf("failed to enqueue deployment %s: %w", dep.ID, err)
	}

	slog.Info("triggered push-to-deploy build from verified GitHub webhook",
		"project_id", project.ID,
		"deployment_id", dep.ID,
		"branch", pushedBranch,
		"commit_sha", commitSHA,
		"delivery_id", trimmedDelivery,
	)

	return &WebhookHandleResult{
		OK:           true,
		Triggered:    true,
		Event:        "push",
		ProjectID:    &project.ID,
		DeploymentID: &dep.ID,
		CommitSHA:    commitSHA,
		Branch:       pushedBranch,
	}, nil
}
