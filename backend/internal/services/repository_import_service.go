package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/models"
)

var (
	ErrImportNotFound              = errors.New("repository import not found")
	ErrImportNotReady              = errors.New("repository import is not in ready status")
	ErrImportInvalidInput          = errors.New("invalid repository import input")
	ErrImportCancelled             = errors.New("repository import was cancelled")
	ErrImportAlreadyActive         = errors.New("repository import is already in progress")
	ErrImportCannotCancelCompleted = errors.New("cannot cancel a repository import already attached to a project")
	ErrImportNotRetryable          = errors.New("cannot retry a repository import already attached to a project")
)

const defaultImportTTL = 24 * time.Hour

type StartImportInput struct {
	Repository string `json:"repository"` // e.g. "owner/repo" or "https://github.com/owner/repo"
	Branch     string `json:"branch"`     // optional, defaults to repository default branch
	RootDir    string `json:"root_dir"`   // optional subfolder path
}

type importJobHandle struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type RepositoryImportService struct {
	db            *pgxpool.Pool
	sourcesDir    string
	githubService *GitHubService
	importTTL     time.Duration

	// activeJobs tracks in-flight import jobs: importID -> cancel/done handle
	jobsMu     sync.Mutex
	activeJobs map[uuid.UUID]*importJobHandle

	// memImports provides deterministic in-memory durable state when db == nil (unit tests)
	memMu      sync.RWMutex
	memImports map[uuid.UUID]*models.RepositoryImport
}

func NewRepositoryImportService(
	db *pgxpool.Pool,
	sourcesDir string,
	githubService *GitHubService,
) *RepositoryImportService {
	if sourcesDir == "" {
		sourcesDir = "./data/sources"
	}
	return &RepositoryImportService{
		db:            db,
		sourcesDir:    sourcesDir,
		githubService: githubService,
		importTTL:     defaultImportTTL,
		activeJobs:    make(map[uuid.UUID]*importJobHandle),
		memImports:    make(map[uuid.UUID]*models.RepositoryImport),
	}
}

// IsImportInProgressStatus returns true for non-terminal import pipeline statuses.
func IsImportInProgressStatus(status string) bool {
	switch status {
	case models.ImportStatusPending,
		models.ImportStatusVerifying,
		models.ImportStatusResolving,
		models.ImportStatusDownloading,
		models.ImportStatusExtracting,
		models.ImportStatusDiscovering:
		return true
	default:
		return false
	}
}

// CleanGitHubRepoReference parses various forms of GitHub repo references into clean "owner/repo".
func CleanGitHubRepoReference(raw string) (owner string, repo string, err error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", fmt.Errorf("%w: repository reference is required", ErrImportInvalidInput)
	}

	// Strip git+ssh or git:// or https:// or http:// prefixes
	s = strings.TrimPrefix(s, "git@github.com:")
	s = strings.TrimPrefix(s, "ssh://git@github.com/")
	s = strings.TrimPrefix(s, "https://github.com/")
	s = strings.TrimPrefix(s, "http://github.com/")
	s = strings.TrimPrefix(s, "github.com/")
	s = strings.TrimSuffix(s, ".git")
	s = strings.Trim(s, "/")

	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("%w: invalid repository reference %q, expected 'owner/repo'", ErrImportInvalidInput, raw)
	}

	owner = parts[0]
	repo = parts[1]

	// Security: validate against path traversal or malicious characters
	if strings.Contains(owner, "..") || strings.Contains(repo, "..") ||
		strings.ContainsAny(owner, "\\?#@:*\"<>|") || strings.ContainsAny(repo, "\\?#@:*\"<>|") {
		return "", "", fmt.Errorf("%w: invalid characters in repository reference", ErrImportInvalidInput)
	}

	return owner, repo, nil
}

// CleanRootDir normalizes and validates optional monorepo subfolder.
func CleanRootDir(root string) (string, error) {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(root))))
	if clean == "." || clean == "/" || clean == "" {
		return "", nil
	}
	clean = strings.TrimPrefix(clean, "/")
	if strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("%w: root directory cannot navigate outside repository root", ErrImportInvalidInput)
	}
	return clean, nil
}

func (s *RepositoryImportService) snapshotDirForImport(importID uuid.UUID) string {
	return filepath.Join(s.sourcesDir, "imports", importID.String())
}

func (s *RepositoryImportService) purgeSnapshotDirs(importID uuid.UUID, recordedPath string) {
	if recordedPath != "" {
		_ = os.RemoveAll(recordedPath)
	}
	defaultDir := s.snapshotDirForImport(importID)
	if defaultDir != "" && defaultDir != recordedPath {
		_ = os.RemoveAll(defaultDir)
	}
}

// SeedImportForTest inserts an in-memory import record when running without Postgres.
func (s *RepositoryImportService) SeedImportForTest(imp *models.RepositoryImport) {
	if s == nil || imp == nil || imp.ID == uuid.Nil {
		return
	}
	s.memMu.Lock()
	defer s.memMu.Unlock()
	s.memImports[imp.ID] = cloneImportRecord(imp)
}

func cloneImportRecord(imp *models.RepositoryImport) *models.RepositoryImport {
	if imp == nil {
		return nil
	}
	cp := *imp
	if imp.ProjectID != nil {
		pid := *imp.ProjectID
		cp.ProjectID = &pid
	}
	if imp.DiscoveryResult != nil {
		cp.DiscoveryResult = append([]byte(nil), imp.DiscoveryResult...)
	}
	if imp.DeploymentPlan != nil {
		cp.DeploymentPlan = append([]byte(nil), imp.DeploymentPlan...)
	}
	return &cp
}

// StartImport creates a durable repository import record and starts the async import process.
func (s *RepositoryImportService) StartImport(ctx context.Context, userID uuid.UUID, input StartImportInput) (*models.RepositoryImport, error) {
	owner, repo, err := CleanGitHubRepoReference(input.Repository)
	if err != nil {
		return nil, err
	}
	rootDir, err := CleanRootDir(input.RootDir)
	if err != nil {
		return nil, err
	}
	branch := strings.TrimSpace(input.Branch)

	repoFullName := fmt.Sprintf("%s/%s", owner, repo)
	importID := uuid.New()

	imp, err := s.createImportRecord(ctx, importID, userID, repoFullName, branch, rootDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create repository import: %w", err)
	}

	s.launchImportPipeline(imp.ID, userID, owner, repo, branch, rootDir, "")

	return imp, nil
}

func (s *RepositoryImportService) createImportRecord(
	ctx context.Context,
	importID uuid.UUID,
	ownerID uuid.UUID,
	repoFullName string,
	branch string,
	rootDir string,
) (*models.RepositoryImport, error) {
	if s.db == nil {
		now := time.Now().UTC()
		imp := &models.RepositoryImport{
			ID:                 importID,
			OwnerID:            ownerID,
			SourceType:         models.SourceTypeGitHub,
			RepositoryFullName: repoFullName,
			Branch:             branch,
			RootDir:            rootDir,
			Status:             models.ImportStatusPending,
			Progress:           0,
			Stage:              "Initializing repository import",
			CreatedAt:          now,
			UpdatedAt:          now,
			ExpiresAt:          now.Add(s.importTTL),
		}
		s.memMu.Lock()
		s.memImports[importID] = cloneImportRecord(imp)
		s.memMu.Unlock()
		return imp, nil
	}

	query := `
		INSERT INTO repository_imports (
			id, owner_id, source_type, repository_full_name, branch, root_dir,
			status, progress, stage, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), NOW())
		RETURNING id, owner_id, source_type, repository_id, repository_full_name, is_private,
		          branch, commit_sha, root_dir, snapshot_path, status, progress, stage,
		          error_message, error_code, discovery_result, deployment_plan, project_id,
		          created_at, updated_at, expires_at`

	return s.scanImportRow(s.db.QueryRow(ctx, query,
		importID, ownerID, models.SourceTypeGitHub, repoFullName, branch, rootDir,
		models.ImportStatusPending, 0, "Initializing repository import",
	))
}

func (s *RepositoryImportService) launchImportPipeline(
	importID uuid.UUID,
	userID uuid.UUID,
	owner string,
	repo string,
	branch string,
	rootDir string,
	pinnedCommitSHA string,
) bool {
	jobCtx, cancel := context.WithCancel(context.Background())
	handle := &importJobHandle{
		cancel: cancel,
		done:   make(chan struct{}),
	}

	s.jobsMu.Lock()
	if _, exists := s.activeJobs[importID]; exists {
		s.jobsMu.Unlock()
		cancel()
		close(handle.done)
		return false
	}
	s.activeJobs[importID] = handle
	s.jobsMu.Unlock()

	go s.runImportPipeline(jobCtx, handle, importID, userID, owner, repo, branch, rootDir, pinnedCommitSHA)
	return true
}

// runImportPipeline executes the durable multi-stage import workflow in the background.
func (s *RepositoryImportService) runImportPipeline(
	ctx context.Context,
	handle *importJobHandle,
	importID uuid.UUID,
	userID uuid.UUID,
	owner string,
	repo string,
	branch string,
	rootDir string,
	pinnedCommitSHA string,
) {
	defer func() {
		s.jobsMu.Lock()
		if cur, ok := s.activeJobs[importID]; ok && cur == handle {
			delete(s.activeJobs, importID)
		}
		s.jobsMu.Unlock()
		close(handle.done)
	}()

	snapshotDir := s.snapshotDirForImport(importID)

	fail := func(err error, code string, stage string) {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			_ = os.RemoveAll(snapshotDir)
			return
		}
		slog.Error("repository import failed",
			"import_id", importID,
			"repo", fmt.Sprintf("%s/%s", owner, repo),
			"stage", stage,
			"error", err,
		)
		_, _ = s.updatePipelineState(context.Background(), importID, models.ImportStatusFailed, 0, stage, err.Error(), code, nil, nil)
	}

	// Stage 1: Verify repository access and metadata
	updated, err := s.updatePipelineState(ctx, importID, models.ImportStatusVerifying, 15, "Verifying repository access", "", "", nil, nil)
	if err != nil || !updated || ctx.Err() != nil {
		return
	}

	repoMeta, err := s.githubService.GetRepo(ctx, userID, owner, repo)
	if err != nil {
		code := classifyGitHubImportError(err, "access_denied")
		fail(err, code, "Repository verification failed")
		return
	}

	// If branch was not specified, use repository's real default branch from metadata
	targetBranch := branch
	if targetBranch == "" {
		targetBranch = strings.TrimSpace(repoMeta.DefaultBranch)
	}
	if targetBranch == "" {
		fail(errors.New("repository does not define a default branch"), "missing_default_branch", "Branch resolution failed")
		return
	}

	// Persist verified metadata and resolved branch
	sourceType := models.SourceTypeGitHub
	if !repoMeta.Private {
		sourceType = models.SourceTypeGitHubPublic
	}
	if okMeta, _ := s.updatePipelineMeta(context.Background(), importID, fmt.Sprintf("%d", repoMeta.ID), repoMeta.Private, targetBranch, sourceType); !okMeta {
		return
	}

	// Stage 2: Resolve immutable commit SHA (preserve pinned SHA on restart recovery if valid)
	if ctx.Err() != nil {
		return
	}
	if updated, _ := s.updatePipelineState(ctx, importID, models.ImportStatusResolving, 30, "Resolving commit SHA", "", "", nil, nil); !updated {
		return
	}

	commitSHA := strings.TrimSpace(pinnedCommitSHA)
	if len(commitSHA) != 40 {
		commitSHA, err = s.githubService.ResolveCommitSHA(ctx, userID, owner, repo, targetBranch)
		if err != nil {
			code := classifyGitHubImportError(err, "commit_resolution_failed")
			fail(fmt.Errorf("failed to resolve commit SHA for branch %s: %w", targetBranch, err), code, "Commit resolution failed")
			return
		}
	}
	if len(commitSHA) != 40 {
		fail(fmt.Errorf("resolved commit %s is not an immutable 40-character SHA", commitSHA), "invalid_commit_sha", "Commit resolution failed")
		return
	}

	if okSHA, _ := s.updatePipelineCommitSHA(context.Background(), importID, commitSHA); !okSHA {
		return
	}

	// Stage 3 & 4: Download and extract snapshot into dedicated immutable directory
	if ctx.Err() != nil {
		return
	}
	if updated, _ := s.updatePipelineState(ctx, importID, models.ImportStatusDownloading, 50, "Downloading repository snapshot", "", "", nil, nil); !updated {
		return
	}

	_ = os.RemoveAll(snapshotDir)
	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		fail(fmt.Errorf("failed to create snapshot workspace: %w", err), "workspace_creation_failed", "Workspace creation failed")
		return
	}

	if updated, _ := s.updatePipelineState(ctx, importID, models.ImportStatusExtracting, 65, "Safely extracting & validating archive", "", "", nil, nil); !updated {
		_ = os.RemoveAll(snapshotDir)
		return
	}

	if err := s.githubService.AcquireRepoTarball(ctx, userID, owner, repo, commitSHA, snapshotDir); err != nil {
		_ = os.RemoveAll(snapshotDir)
		code := classifyGitHubImportError(err, "extraction_failed")
		fail(err, code, "Extraction failed")
		return
	}

	if ctx.Err() != nil {
		_ = os.RemoveAll(snapshotDir)
		return
	}

	if okPath, _ := s.updatePipelineSnapshotPath(context.Background(), importID, snapshotDir); !okPath {
		_ = os.RemoveAll(snapshotDir)
		return
	}

	// Stage 5: Discover services on the extracted snapshot
	if ctx.Err() != nil {
		_ = os.RemoveAll(snapshotDir)
		return
	}
	if updated, _ := s.updatePipelineState(ctx, importID, models.ImportStatusDiscovering, 80, "Discovering services & configuration", "", "", nil, nil); !updated {
		_ = os.RemoveAll(snapshotDir)
		return
	}

	targetDiscoveryDir := snapshotDir
	if rootDir != "" {
		targetDiscoveryDir = filepath.Join(snapshotDir, filepath.FromSlash(rootDir))
		fi, statErr := os.Stat(targetDiscoveryDir)
		if statErr != nil || !fi.IsDir() {
			_ = os.RemoveAll(snapshotDir)
			fail(fmt.Errorf("specified root directory '%s' does not exist in repository", rootDir), "missing_root_dir", "Root directory validation failed")
			return
		}
	}

	discResult, err := discovery.Discover(targetDiscoveryDir)
	if err != nil {
		_ = os.RemoveAll(snapshotDir)
		fail(fmt.Errorf("discovery analysis failed: %w", err), "discovery_failed", "Service discovery failed")
		return
	}

	discJSON, err := json.Marshal(discResult)
	if err != nil {
		_ = os.RemoveAll(snapshotDir)
		fail(fmt.Errorf("failed to encode discovery result: %w", err), "discovery_failed", "Discovery encoding failed")
		return
	}

	// Stage 6: Generate snapshot deployment plan
	if ctx.Err() != nil {
		_ = os.RemoveAll(snapshotDir)
		return
	}
	if updated, _ := s.updatePipelineState(ctx, importID, models.ImportStatusDiscovering, 90, "Generating deployment plan", "", "", discJSON, nil); !updated {
		_ = os.RemoveAll(snapshotDir)
		return
	}

	plan, err := discovery.GeneratePlan(discResult, discovery.PlanOptions{
		SourceRevision: commitSHA,
		SourceType:     models.SourceTypeGitHub,
	})
	if err != nil {
		_ = os.RemoveAll(snapshotDir)
		fail(fmt.Errorf("deployment plan generation failed: %w", err), "plan_generation_failed", "Plan generation failed")
		return
	}

	planJSON, err := json.Marshal(plan)
	if err != nil {
		_ = os.RemoveAll(snapshotDir)
		fail(fmt.Errorf("failed to encode deployment plan: %w", err), "plan_generation_failed", "Plan encoding failed")
		return
	}

	if ctx.Err() != nil {
		_ = os.RemoveAll(snapshotDir)
		return
	}

	// Complete: Mark ready with full discovery and deployment plan (only if not concurrently cancelled)
	updated, err = s.updatePipelineState(context.Background(), importID, models.ImportStatusReady, 100, "Repository import ready", "", "", discJSON, planJSON)
	if err != nil || !updated {
		_ = os.RemoveAll(snapshotDir)
		return
	}

	slog.Info("repository import completed successfully",
		"import_id", importID,
		"repo", fmt.Sprintf("%s/%s", owner, repo),
		"branch", targetBranch,
		"sha", commitSHA,
	)
}

func classifyGitHubImportError(err error, defaultCode string) string {
	switch {
	case errors.Is(err, ErrGitHubRepoNotFound):
		return "not_found"
	case errors.Is(err, ErrGitHubNeedsReauth):
		return "needs_reauth"
	case errors.Is(err, ErrGitHubSSORequired):
		return "sso_required"
	case errors.Is(err, ErrGitHubRateLimited):
		return "rate_limited"
	case errors.Is(err, ErrGitHubNotConnected):
		return "not_connected"
	case errors.Is(err, ErrGitHubArchiveTooLarge):
		return "archive_too_large"
	case errors.Is(err, ErrGitHubTooManyFiles):
		return "too_many_files"
	case errors.Is(err, ErrGitHubFileTooLarge):
		return "file_too_large"
	case errors.Is(err, ErrGitHubExtractionTimeout):
		return "extraction_timeout"
	case errors.Is(err, ErrGitHubMaliciousArchive):
		return "path_traversal_violation"
	default:
		return defaultCode
	}
}

func (s *RepositoryImportService) updatePipelineMeta(
	ctx context.Context,
	importID uuid.UUID,
	repoID string,
	isPrivate bool,
	branch string,
	sourceType string,
) (bool, error) {
	if s.db == nil {
		s.memMu.Lock()
		defer s.memMu.Unlock()
		imp, ok := s.memImports[importID]
		if !ok || imp.Status == models.ImportStatusCancelled || imp.Status == models.ImportStatusCompleted {
			return false, nil
		}
		imp.RepositoryID = repoID
		imp.IsPrivate = isPrivate
		imp.Branch = branch
		imp.SourceType = sourceType
		imp.UpdatedAt = time.Now().UTC()
		return true, nil
	}

	tag, err := s.db.Exec(ctx, `
		UPDATE repository_imports
		SET repository_id = $1, is_private = $2, branch = $3, source_type = $4, updated_at = NOW()
		WHERE id = $5 AND status NOT IN ('cancelled', 'completed')`,
		repoID, isPrivate, branch, sourceType, importID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *RepositoryImportService) updatePipelineCommitSHA(ctx context.Context, importID uuid.UUID, commitSHA string) (bool, error) {
	if s.db == nil {
		s.memMu.Lock()
		defer s.memMu.Unlock()
		imp, ok := s.memImports[importID]
		if !ok || imp.Status == models.ImportStatusCancelled || imp.Status == models.ImportStatusCompleted {
			return false, nil
		}
		imp.CommitSHA = commitSHA
		imp.UpdatedAt = time.Now().UTC()
		return true, nil
	}

	tag, err := s.db.Exec(ctx, `
		UPDATE repository_imports
		SET commit_sha = $1, updated_at = NOW()
		WHERE id = $2 AND status NOT IN ('cancelled', 'completed')`,
		commitSHA, importID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *RepositoryImportService) updatePipelineSnapshotPath(ctx context.Context, importID uuid.UUID, snapshotDir string) (bool, error) {
	if s.db == nil {
		s.memMu.Lock()
		defer s.memMu.Unlock()
		imp, ok := s.memImports[importID]
		if !ok || imp.Status == models.ImportStatusCancelled || imp.Status == models.ImportStatusCompleted {
			return false, nil
		}
		imp.SnapshotPath = snapshotDir
		imp.UpdatedAt = time.Now().UTC()
		return true, nil
	}

	tag, err := s.db.Exec(ctx, `
		UPDATE repository_imports
		SET snapshot_path = $1, updated_at = NOW()
		WHERE id = $2 AND status NOT IN ('cancelled', 'completed')`,
		snapshotDir, importID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// updatePipelineState updates the status, progress, stage, and optional results of an in-flight import
// without ever overwriting a cancelled or completed record.
func (s *RepositoryImportService) updatePipelineState(
	ctx context.Context,
	importID uuid.UUID,
	status string,
	progress int,
	stage string,
	errMsg string,
	errCode string,
	discoveryResult []byte,
	planResult []byte,
) (bool, error) {
	if s.db == nil {
		s.memMu.Lock()
		defer s.memMu.Unlock()
		imp, ok := s.memImports[importID]
		if !ok || imp.Status == models.ImportStatusCancelled || imp.Status == models.ImportStatusCompleted {
			return false, nil
		}
		imp.Status = status
		imp.Progress = progress
		imp.Stage = stage
		imp.ErrorMessage = errMsg
		imp.ErrorCode = errCode
		if discoveryResult != nil {
			imp.DiscoveryResult = append([]byte(nil), discoveryResult...)
		}
		if planResult != nil {
			imp.DeploymentPlan = append([]byte(nil), planResult...)
		}
		imp.UpdatedAt = time.Now().UTC()
		return true, nil
	}

	query := `
		UPDATE repository_imports
		SET status = $1,
		    progress = $2,
		    stage = $3,
		    error_message = $4,
		    error_code = $5,
		    discovery_result = COALESCE($6, discovery_result),
		    deployment_plan = COALESCE($7, deployment_plan),
		    updated_at = NOW()
		WHERE id = $8 AND status NOT IN ('cancelled', 'completed')`

	tag, err := s.db.Exec(ctx, query,
		status, progress, stage, errMsg, errCode,
		discoveryResult, planResult, importID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// GetImport retrieves a repository import record ensuring owner isolation.
func (s *RepositoryImportService) GetImport(ctx context.Context, userID uuid.UUID, importID uuid.UUID) (*models.RepositoryImport, error) {
	if s.db == nil {
		s.memMu.RLock()
		defer s.memMu.RUnlock()
		imp, ok := s.memImports[importID]
		if !ok || imp.OwnerID != userID {
			return nil, ErrImportNotFound
		}
		return cloneImportRecord(imp), nil
	}

	query := `
		SELECT id, owner_id, source_type, repository_id, repository_full_name, is_private,
		       branch, commit_sha, root_dir, snapshot_path, status, progress, stage,
		       error_message, error_code, discovery_result, deployment_plan, project_id,
		       created_at, updated_at, expires_at
		FROM repository_imports
		WHERE id = $1 AND owner_id = $2`

	imp, err := s.scanImportRow(s.db.QueryRow(ctx, query, importID, userID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrImportNotFound
		}
		return nil, fmt.Errorf("failed to fetch repository import: %w", err)
	}
	return imp, nil
}

// ListImports lists repository imports belonging to userID ordered newest first.
func (s *RepositoryImportService) ListImports(ctx context.Context, userID uuid.UUID, limit int) ([]*models.RepositoryImport, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	if s.db == nil {
		s.memMu.RLock()
		defer s.memMu.RUnlock()
		var out []*models.RepositoryImport
		for _, imp := range s.memImports {
			if imp.OwnerID == userID {
				out = append(out, cloneImportRecord(imp))
			}
		}
		sort.Slice(out, func(i, j int) bool {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		})
		if len(out) > limit {
			out = out[:limit]
		}
		return out, nil
	}

	rows, err := s.db.Query(ctx, `
		SELECT id, owner_id, source_type, repository_id, repository_full_name, is_private,
		       branch, commit_sha, root_dir, snapshot_path, status, progress, stage,
		       error_message, error_code, discovery_result, deployment_plan, project_id,
		       created_at, updated_at, expires_at
		FROM repository_imports
		WHERE owner_id = $1
		ORDER BY created_at DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list repository imports: %w", err)
	}
	defer rows.Close()

	var result []*models.RepositoryImport
	for rows.Next() {
		imp, err := s.scanImportRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, imp)
	}
	return result, rows.Err()
}

// CancelImport cancels an in-progress import job and purges incomplete workspaces.
func (s *RepositoryImportService) CancelImport(ctx context.Context, userID uuid.UUID, importID uuid.UUID) (*models.RepositoryImport, error) {
	imp, err := s.GetImport(ctx, userID, importID)
	if err != nil {
		return nil, err
	}

	if imp.Status == models.ImportStatusCompleted || imp.ProjectID != nil {
		return nil, ErrImportCannotCancelCompleted
	}

	// Mark cancelled in persistent storage first so in-flight stages refuse to commit ready/failed
	if err := s.persistCancelState(ctx, userID, importID); err != nil {
		return nil, err
	}

	// Signal and await any running pipeline goroutine before cleaning workspace files
	s.jobsMu.Lock()
	handle := s.activeJobs[importID]
	s.jobsMu.Unlock()
	if handle != nil {
		handle.cancel()
		select {
		case <-handle.done:
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}

	// Clean up disk workspace since the import is not attached to a completed project
	s.purgeSnapshotDirs(importID, imp.SnapshotPath)

	return s.GetImport(ctx, userID, importID)
}

func (s *RepositoryImportService) persistCancelState(ctx context.Context, userID uuid.UUID, importID uuid.UUID) error {
	if s.db == nil {
		s.memMu.Lock()
		defer s.memMu.Unlock()
		imp, ok := s.memImports[importID]
		if !ok || imp.OwnerID != userID {
			return ErrImportNotFound
		}
		if imp.Status == models.ImportStatusCompleted || imp.ProjectID != nil {
			return ErrImportCannotCancelCompleted
		}
		imp.Status = models.ImportStatusCancelled
		imp.Progress = 0
		imp.Stage = "Import cancelled by user"
		imp.ErrorMessage = "Import cancelled by user"
		imp.ErrorCode = "cancelled"
		imp.SnapshotPath = ""
		imp.UpdatedAt = time.Now().UTC()
		return nil
	}

	tag, err := s.db.Exec(ctx, `
		UPDATE repository_imports
		SET status = $1,
		    progress = 0,
		    stage = $2,
		    error_message = $3,
		    error_code = 'cancelled',
		    snapshot_path = '',
		    updated_at = NOW()
		WHERE id = $4 AND owner_id = $5 AND status <> 'completed' AND project_id IS NULL`,
		models.ImportStatusCancelled, "Import cancelled by user", "Import cancelled by user", importID, userID,
	)
	if err != nil {
		return fmt.Errorf("failed to cancel repository import: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Distinguish not-found vs already-completed race
		current, err := s.GetImport(ctx, userID, importID)
		if err != nil {
			return err
		}
		if current.Status == models.ImportStatusCompleted || current.ProjectID != nil {
			return ErrImportCannotCancelCompleted
		}
	}
	return nil
}

// RetryImport resets a failed, cancelled, or stalled import and restarts execution cleanly.
func (s *RepositoryImportService) RetryImport(ctx context.Context, userID uuid.UUID, importID uuid.UUID) (*models.RepositoryImport, error) {
	imp, err := s.GetImport(ctx, userID, importID)
	if err != nil {
		return nil, err
	}

	if imp.Status == models.ImportStatusCompleted || imp.ProjectID != nil {
		return nil, ErrImportNotRetryable
	}

	owner, repo, err := CleanGitHubRepoReference(imp.RepositoryFullName)
	if err != nil {
		return nil, err
	}

	// Atomically reserve activeJobs slot before resetting state so concurrent retries cannot race
	jobCtx, cancel := context.WithCancel(context.Background())
	handle := &importJobHandle{
		cancel: cancel,
		done:   make(chan struct{}),
	}

	s.jobsMu.Lock()
	if _, running := s.activeJobs[importID]; running {
		s.jobsMu.Unlock()
		cancel()
		close(handle.done)
		return nil, ErrImportAlreadyActive
	}
	s.activeJobs[importID] = handle
	s.jobsMu.Unlock()

	cleanupReservation := func() {
		cancel()
		s.jobsMu.Lock()
		if cur, ok := s.activeJobs[importID]; ok && cur == handle {
			delete(s.activeJobs, importID)
		}
		s.jobsMu.Unlock()
		close(handle.done)
	}

	// Purge previous workspace files
	s.purgeSnapshotDirs(importID, imp.SnapshotPath)

	resetImp, err := s.resetImportForRetry(ctx, userID, importID, "Retrying repository import")
	if err != nil {
		cleanupReservation()
		return nil, err
	}

	go s.runImportPipeline(jobCtx, handle, imp.ID, userID, owner, repo, imp.Branch, imp.RootDir, "")

	return resetImp, nil
}

func (s *RepositoryImportService) resetImportForRetry(ctx context.Context, userID uuid.UUID, importID uuid.UUID, stage string) (*models.RepositoryImport, error) {
	if s.db == nil {
		s.memMu.Lock()
		defer s.memMu.Unlock()
		imp, ok := s.memImports[importID]
		if !ok || imp.OwnerID != userID {
			return nil, ErrImportNotFound
		}
		if imp.Status == models.ImportStatusCompleted || imp.ProjectID != nil {
			return nil, ErrImportNotRetryable
		}
		now := time.Now().UTC()
		imp.Status = models.ImportStatusPending
		imp.Progress = 0
		imp.Stage = stage
		imp.CommitSHA = ""
		imp.SnapshotPath = ""
		imp.ErrorMessage = ""
		imp.ErrorCode = ""
		imp.DiscoveryResult = nil
		imp.DeploymentPlan = nil
		imp.UpdatedAt = now
		imp.ExpiresAt = now.Add(s.importTTL)
		return cloneImportRecord(imp), nil
	}

	query := `
		UPDATE repository_imports
		SET status = $1,
		    progress = 0,
		    stage = $2,
		    commit_sha = '',
		    snapshot_path = '',
		    error_message = '',
		    error_code = '',
		    discovery_result = NULL,
		    deployment_plan = NULL,
		    expires_at = NOW() + INTERVAL '24 hours',
		    updated_at = NOW()
		WHERE id = $3 AND owner_id = $4 AND status <> 'completed' AND project_id IS NULL
		RETURNING id, owner_id, source_type, repository_id, repository_full_name, is_private,
		          branch, commit_sha, root_dir, snapshot_path, status, progress, stage,
		          error_message, error_code, discovery_result, deployment_plan, project_id,
		          created_at, updated_at, expires_at`

	imp, err := s.scanImportRow(s.db.QueryRow(ctx, query, models.ImportStatusPending, stage, importID, userID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			current, getErr := s.GetImport(ctx, userID, importID)
			if getErr != nil {
				return nil, getErr
			}
			if current.Status == models.ImportStatusCompleted || current.ProjectID != nil {
				return nil, ErrImportNotRetryable
			}
			return nil, ErrImportNotFound
		}
		return nil, fmt.Errorf("failed to reset import for retry: %w", err)
	}
	return imp, nil
}

// RecoverInterruptedImports resumes non-terminal repository imports that were interrupted
// by a backend restart, while marking already-expired interrupted imports as failed and
// cleaning up any partial disk directories.
func (s *RepositoryImportService) RecoverInterruptedImports(ctx context.Context) (int, error) {
	candidates, err := s.listInterruptedImports(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to query interrupted repository imports: %w", err)
	}

	recovered := 0
	expiredFailed := 0
	now := time.Now().UTC()

	for _, imp := range candidates {
		s.jobsMu.Lock()
		_, alreadyRunning := s.activeJobs[imp.ID]
		s.jobsMu.Unlock()
		if alreadyRunning {
			continue
		}

		// Clean any partial extraction files on disk before failing or resuming
		s.purgeSnapshotDirs(imp.ID, imp.SnapshotPath)

		if !imp.ExpiresAt.IsZero() && !imp.ExpiresAt.After(now) {
			if err := s.markInterruptedImportFailed(ctx, imp.ID, "Import expired before recovery", "Repository import expired before server recovery could resume it", "import_expired"); err != nil {
				slog.Warn("failed to mark expired interrupted import as failed", "import_id", imp.ID, "error", err)
			} else {
				expiredFailed++
			}
			continue
		}

		owner, repo, parseErr := CleanGitHubRepoReference(imp.RepositoryFullName)
		if parseErr != nil {
			_ = s.markInterruptedImportFailed(ctx, imp.ID, "Invalid repository reference", parseErr.Error(), "invalid_repository")
			continue
		}

		jobCtx, cancel := context.WithCancel(context.Background())
		handle := &importJobHandle{
			cancel: cancel,
			done:   make(chan struct{}),
		}

		s.jobsMu.Lock()
		if _, running := s.activeJobs[imp.ID]; running {
			s.jobsMu.Unlock()
			cancel()
			close(handle.done)
			continue
		}
		s.activeJobs[imp.ID] = handle
		s.jobsMu.Unlock()

		claimed, resetErr := s.claimInterruptedImportForRecovery(ctx, imp.ID)
		if resetErr != nil || !claimed {
			cancel()
			s.jobsMu.Lock()
			if cur, ok := s.activeJobs[imp.ID]; ok && cur == handle {
				delete(s.activeJobs, imp.ID)
			}
			s.jobsMu.Unlock()
			close(handle.done)
			if resetErr != nil {
				slog.Warn("failed to claim interrupted import for recovery", "import_id", imp.ID, "error", resetErr)
			}
			continue
		}

		pinnedSHA := ""
		if len(strings.TrimSpace(imp.CommitSHA)) == 40 {
			pinnedSHA = strings.TrimSpace(imp.CommitSHA)
		}

		go s.runImportPipeline(jobCtx, handle, imp.ID, imp.OwnerID, owner, repo, imp.Branch, imp.RootDir, pinnedSHA)
		recovered++
	}

	if recovered > 0 || expiredFailed > 0 {
		slog.Info("repository import startup recovery complete",
			"recovered", recovered,
			"expired_failed", expiredFailed,
		)
	}

	return recovered, nil
}

func (s *RepositoryImportService) listInterruptedImports(ctx context.Context) ([]*models.RepositoryImport, error) {
	if s.db == nil {
		s.memMu.RLock()
		defer s.memMu.RUnlock()
		var out []*models.RepositoryImport
		for _, imp := range s.memImports {
			if IsImportInProgressStatus(imp.Status) && imp.ProjectID == nil {
				out = append(out, cloneImportRecord(imp))
			}
		}
		sort.Slice(out, func(i, j int) bool {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		})
		return out, nil
	}

	rows, err := s.db.Query(ctx, `
		SELECT id, owner_id, source_type, repository_id, repository_full_name, is_private,
		       branch, commit_sha, root_dir, snapshot_path, status, progress, stage,
		       error_message, error_code, discovery_result, deployment_plan, project_id,
		       created_at, updated_at, expires_at
		FROM repository_imports
		WHERE status IN ('pending', 'verifying', 'resolving', 'downloading', 'extracting', 'discovering')
		  AND project_id IS NULL
		ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*models.RepositoryImport
	for rows.Next() {
		imp, err := s.scanImportRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, imp)
	}
	return result, rows.Err()
}

func (s *RepositoryImportService) markInterruptedImportFailed(ctx context.Context, importID uuid.UUID, stage, errMsg, errCode string) error {
	if s.db == nil {
		s.memMu.Lock()
		defer s.memMu.Unlock()
		imp, ok := s.memImports[importID]
		if !ok || !IsImportInProgressStatus(imp.Status) || imp.ProjectID != nil {
			return nil
		}
		imp.Status = models.ImportStatusFailed
		imp.Progress = 0
		imp.Stage = stage
		imp.ErrorMessage = errMsg
		imp.ErrorCode = errCode
		imp.SnapshotPath = ""
		imp.UpdatedAt = time.Now().UTC()
		return nil
	}

	_, err := s.db.Exec(ctx, `
		UPDATE repository_imports
		SET status = $1,
		    progress = 0,
		    stage = $2,
		    error_message = $3,
		    error_code = $4,
		    snapshot_path = '',
		    updated_at = NOW()
		WHERE id = $5
		  AND status IN ('pending', 'verifying', 'resolving', 'downloading', 'extracting', 'discovering')
		  AND project_id IS NULL`,
		models.ImportStatusFailed, stage, errMsg, errCode, importID,
	)
	return err
}

func (s *RepositoryImportService) claimInterruptedImportForRecovery(ctx context.Context, importID uuid.UUID) (bool, error) {
	if s.db == nil {
		s.memMu.Lock()
		defer s.memMu.Unlock()
		imp, ok := s.memImports[importID]
		if !ok || !IsImportInProgressStatus(imp.Status) || imp.ProjectID != nil {
			return false, nil
		}
		imp.Status = models.ImportStatusPending
		imp.Progress = 0
		imp.Stage = "Recovering repository import after server restart"
		imp.SnapshotPath = ""
		imp.ErrorMessage = ""
		imp.ErrorCode = ""
		imp.DiscoveryResult = nil
		imp.DeploymentPlan = nil
		imp.UpdatedAt = time.Now().UTC()
		return true, nil
	}

	tag, err := s.db.Exec(ctx, `
		UPDATE repository_imports
		SET status = $1,
		    progress = 0,
		    stage = 'Recovering repository import after server restart',
		    snapshot_path = '',
		    error_message = '',
		    error_code = '',
		    discovery_result = NULL,
		    deployment_plan = NULL,
		    updated_at = NOW()
		WHERE id = $2
		  AND status IN ('pending', 'verifying', 'resolving', 'downloading', 'extracting', 'discovering')
		  AND project_id IS NULL`,
		models.ImportStatusPending, importID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Stop cancels all active import worker goroutines without overwriting their persisted
// in-progress state in storage, allowing RecoverInterruptedImports to resume them after restart.
func (s *RepositoryImportService) Stop(ctx context.Context) error {
	s.jobsMu.Lock()
	handles := make([]*importJobHandle, 0, len(s.activeJobs))
	for _, h := range s.activeJobs {
		handles = append(handles, h)
	}
	s.jobsMu.Unlock()

	for _, h := range handles {
		h.cancel()
	}
	for _, h := range handles {
		select {
		case <-h.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// MarkCompleted associates an import with a newly created project and marks it completed.
func (s *RepositoryImportService) MarkCompleted(ctx context.Context, userID uuid.UUID, importID uuid.UUID, projectID uuid.UUID) error {
	if s.db == nil {
		s.memMu.Lock()
		defer s.memMu.Unlock()
		imp, ok := s.memImports[importID]
		if !ok || imp.OwnerID != userID {
			return ErrImportNotFound
		}
		pid := projectID
		imp.Status = models.ImportStatusCompleted
		imp.ProjectID = &pid
		imp.UpdatedAt = time.Now().UTC()
		return nil
	}

	query := `
		UPDATE repository_imports
		SET status = $1, project_id = $2, updated_at = NOW()
		WHERE id = $3 AND owner_id = $4`

	tag, err := s.db.Exec(ctx, query, models.ImportStatusCompleted, projectID, importID, userID)
	if err != nil {
		return fmt.Errorf("failed to mark import completed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrImportNotFound
	}
	return nil
}

// PruneExpiredImports cleans up expired or incomplete imports older than 24 hours.
func (s *RepositoryImportService) PruneExpiredImports(ctx context.Context) (int64, error) {
	if s.db == nil {
		s.memMu.Lock()
		defer s.memMu.Unlock()
		now := time.Now().UTC()
		var pruned int64
		for id, imp := range s.memImports {
			if imp.ProjectID == nil && !imp.ExpiresAt.IsZero() && imp.ExpiresAt.Before(now) {
				s.purgeSnapshotDirs(id, imp.SnapshotPath)
				delete(s.memImports, id)
				pruned++
			}
		}
		return pruned, nil
	}

	rows, err := s.db.Query(ctx, `
		SELECT id, snapshot_path FROM repository_imports
		WHERE expires_at < NOW() AND project_id IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("failed to query expired imports: %w", err)
	}
	defer rows.Close()

	var toDelete []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		var path string
		if err := rows.Scan(&id, &path); err == nil {
			toDelete = append(toDelete, id)
			s.purgeSnapshotDirs(id, path)
		}
	}

	if len(toDelete) == 0 {
		return 0, nil
	}

	tag, err := s.db.Exec(ctx, `DELETE FROM repository_imports WHERE id = ANY($1)`, toDelete)
	if err != nil {
		return 0, fmt.Errorf("failed to delete expired import rows: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *RepositoryImportService) scanImportRow(row pgx.Row) (*models.RepositoryImport, error) {
	var imp models.RepositoryImport
	var discResult, planResult []byte
	var repoID, snapPath, errCode *string

	err := row.Scan(
		&imp.ID, &imp.OwnerID, &imp.SourceType, &repoID, &imp.RepositoryFullName, &imp.IsPrivate,
		&imp.Branch, &imp.CommitSHA, &imp.RootDir, &snapPath, &imp.Status, &imp.Progress, &imp.Stage,
		&imp.ErrorMessage, &errCode, &discResult, &planResult, &imp.ProjectID,
		&imp.CreatedAt, &imp.UpdatedAt, &imp.ExpiresAt,
	)
	if err != nil {
		return nil, err
	}

	if repoID != nil {
		imp.RepositoryID = *repoID
	}
	if snapPath != nil {
		imp.SnapshotPath = *snapPath
	}
	if errCode != nil {
		imp.ErrorCode = *errCode
	}
	if len(discResult) > 0 {
		imp.DiscoveryResult = discResult
	}
	if len(planResult) > 0 {
		imp.DeploymentPlan = planResult
	}

	return &imp, nil
}
