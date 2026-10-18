package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/models"
)

var (
	ErrImportNotFound     = errors.New("repository import not found")
	ErrImportNotReady     = errors.New("repository import is not in ready status")
	ErrImportInvalidInput = errors.New("invalid repository import input")
	ErrImportCancelled    = errors.New("repository import was cancelled")
)

type StartImportInput struct {
	Repository string `json:"repository"` // e.g. "owner/repo" or "https://github.com/owner/repo"
	Branch     string `json:"branch"`     // optional, defaults to repository default branch
	RootDir    string `json:"root_dir"`   // optional subfolder path
}

type RepositoryImportService struct {
	db            *pgxpool.Pool
	sourcesDir    string
	githubService *GitHubService

	// activeJobs tracks in-flight import cancellations: importID -> cancelFunc
	jobsMu     sync.Mutex
	activeJobs map[uuid.UUID]context.CancelFunc
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
		activeJobs:    make(map[uuid.UUID]context.CancelFunc),
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

	query := `
		INSERT INTO repository_imports (
			id, owner_id, source_type, repository_full_name, branch, root_dir,
			status, progress, stage, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), NOW())
		RETURNING id, owner_id, source_type, repository_id, repository_full_name, is_private,
		          branch, commit_sha, root_dir, snapshot_path, status, progress, stage,
		          error_message, error_code, discovery_result, deployment_plan, project_id,
		          created_at, updated_at, expires_at`

	imp, err := s.scanImportRow(s.db.QueryRow(ctx, query,
		importID, userID, models.SourceTypeGitHub, repoFullName, branch, rootDir,
		models.ImportStatusPending, 0, "Initializing repository import",
	))
	if err != nil {
		return nil, fmt.Errorf("failed to create repository import: %w", err)
	}

	// Create cancellable context for background execution
	jobCtx, cancel := context.WithCancel(context.Background())
	s.jobsMu.Lock()
	s.activeJobs[importID] = cancel
	s.jobsMu.Unlock()

	go s.runImportPipeline(jobCtx, imp.ID, userID, owner, repo, branch, rootDir)

	return imp, nil
}

// runImportPipeline executes the durable multi-stage import workflow in the background.
func (s *RepositoryImportService) runImportPipeline(
	ctx context.Context,
	importID uuid.UUID,
	userID uuid.UUID,
	owner string,
	repo string,
	branch string,
	rootDir string,
) {
	defer func() {
		s.jobsMu.Lock()
		delete(s.activeJobs, importID)
		s.jobsMu.Unlock()
	}()

	fail := func(err error, code string, stage string) {
		slog.Error("repository import failed",
			"import_id", importID,
			"repo", fmt.Sprintf("%s/%s", owner, repo),
			"stage", stage,
			"error", err,
		)
		_ = s.updateImportState(context.Background(), importID, models.ImportStatusFailed, 0, stage, err.Error(), code, nil, nil)
	}

	// Stage 1: Verify repository access and metadata
	_ = s.updateImportState(ctx, importID, models.ImportStatusVerifying, 15, "Verifying repository access", "", "", nil, nil)

	repoMeta, err := s.githubService.GetRepo(ctx, userID, owner, repo)
	if err != nil {
		code := "access_denied"
		if errors.Is(err, ErrGitHubRepoNotFound) {
			code = "not_found"
		} else if errors.Is(err, ErrGitHubNeedsReauth) {
			code = "needs_reauth"
		} else if errors.Is(err, ErrGitHubSSORequired) {
			code = "sso_required"
		} else if errors.Is(err, ErrGitHubRateLimited) {
			code = "rate_limited"
		} else if errors.Is(err, ErrGitHubNotConnected) {
			code = "not_connected"
		}
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
	_, _ = s.db.Exec(context.Background(), `
		UPDATE repository_imports
		SET repository_id = $1, is_private = $2, branch = $3, source_type = $4, updated_at = NOW()
		WHERE id = $5`,
		fmt.Sprintf("%d", repoMeta.ID), repoMeta.Private, targetBranch, sourceType, importID,
	)

	// Stage 2: Resolve immutable commit SHA
	if err := ctx.Err(); err != nil {
		fail(ErrImportCancelled, "cancelled", "Import cancelled")
		return
	}
	_ = s.updateImportState(ctx, importID, models.ImportStatusResolving, 30, "Resolving commit SHA", "", "", nil, nil)

	commitSHA, err := s.githubService.ResolveCommitSHA(ctx, userID, owner, repo, targetBranch)
	if err != nil {
		fail(fmt.Errorf("failed to resolve commit SHA for branch %s: %w", targetBranch, err), "commit_resolution_failed", "Commit resolution failed")
		return
	}
	if len(commitSHA) != 40 {
		fail(fmt.Errorf("resolved commit %s is not an immutable 40-character SHA", commitSHA), "invalid_commit_sha", "Commit resolution failed")
		return
	}

	_, _ = s.db.Exec(context.Background(), `
		UPDATE repository_imports SET commit_sha = $1, updated_at = NOW() WHERE id = $2`,
		commitSHA, importID,
	)

	// Stage 3 & 4: Download and extract snapshot into dedicated immutable directory
	if err := ctx.Err(); err != nil {
		fail(ErrImportCancelled, "cancelled", "Import cancelled")
		return
	}
	_ = s.updateImportState(ctx, importID, models.ImportStatusDownloading, 50, "Downloading repository snapshot", "", "", nil, nil)

	snapshotDir := filepath.Join(s.sourcesDir, "imports", importID.String())
	_ = os.RemoveAll(snapshotDir)
	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		fail(fmt.Errorf("failed to create snapshot workspace: %w", err), "workspace_creation_failed", "Workspace creation failed")
		return
	}

	_ = s.updateImportState(ctx, importID, models.ImportStatusExtracting, 65, "Safely extracting & validating archive", "", "", nil, nil)

	if err := s.githubService.AcquireRepoTarball(ctx, userID, owner, repo, commitSHA, snapshotDir); err != nil {
		_ = os.RemoveAll(snapshotDir)
		code := "extraction_failed"
		if errors.Is(err, ErrGitHubArchiveTooLarge) {
			code = "archive_too_large"
		} else if errors.Is(err, ErrGitHubTooManyFiles) {
			code = "too_many_files"
		} else if errors.Is(err, ErrGitHubFileTooLarge) {
			code = "file_too_large"
		} else if errors.Is(err, ErrGitHubExtractionTimeout) {
			code = "extraction_timeout"
		} else if errors.Is(err, ErrGitHubMaliciousArchive) {
			code = "path_traversal_violation"
		}
		fail(err, code, "Extraction failed")
		return
	}

	_, _ = s.db.Exec(context.Background(), `
		UPDATE repository_imports SET snapshot_path = $1, updated_at = NOW() WHERE id = $2`,
		snapshotDir, importID,
	)

	// Stage 5: Discover services on the extracted snapshot
	if err := ctx.Err(); err != nil {
		_ = os.RemoveAll(snapshotDir)
		fail(ErrImportCancelled, "cancelled", "Import cancelled")
		return
	}
	_ = s.updateImportState(ctx, importID, models.ImportStatusDiscovering, 80, "Discovering services & configuration", "", "", nil, nil)

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
		fail(fmt.Errorf("failed to encode discovery result: %w", err), "discovery_failed", "Discovery encoding failed")
		return
	}

	// Stage 6: Generate snapshot deployment plan
	_ = s.updateImportState(ctx, importID, models.ImportStatusDiscovering, 90, "Generating deployment plan", "", "", discJSON, nil)

	plan, err := discovery.GeneratePlan(discResult, discovery.PlanOptions{
		SourceRevision: commitSHA,
		SourceType:     models.SourceTypeGitHub,
	})
	if err != nil {
		fail(fmt.Errorf("deployment plan generation failed: %w", err), "plan_generation_failed", "Plan generation failed")
		return
	}

	planJSON, err := json.Marshal(plan)
	if err != nil {
		fail(fmt.Errorf("failed to encode deployment plan: %w", err), "plan_generation_failed", "Plan encoding failed")
		return
	}

	// Complete: Mark ready with full discovery and deployment plan
	_ = s.updateImportState(context.Background(), importID, models.ImportStatusReady, 100, "Repository import ready", "", "", discJSON, planJSON)
	slog.Info("repository import completed successfully",
		"import_id", importID,
		"repo", fmt.Sprintf("%s/%s", owner, repo),
		"branch", targetBranch,
		"sha", commitSHA,
	)
}

// updateImportState updates the status, progress, stage, and optional results of an import.
func (s *RepositoryImportService) updateImportState(
	ctx context.Context,
	importID uuid.UUID,
	status string,
	progress int,
	stage string,
	errMsg string,
	errCode string,
	discoveryResult []byte,
	planResult []byte,
) error {
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
		WHERE id = $8`

	_, err := s.db.Exec(ctx, query,
		status, progress, stage, errMsg, errCode,
		discoveryResult, planResult, importID,
	)
	return err
}

// GetImport retrieves a repository import record ensuring owner isolation.
func (s *RepositoryImportService) GetImport(ctx context.Context, userID uuid.UUID, importID uuid.UUID) (*models.RepositoryImport, error) {
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

// CancelImport cancels an in-progress import job and purges incomplete workspaces.
func (s *RepositoryImportService) CancelImport(ctx context.Context, userID uuid.UUID, importID uuid.UUID) (*models.RepositoryImport, error) {
	imp, err := s.GetImport(ctx, userID, importID)
	if err != nil {
		return nil, err
	}

	s.jobsMu.Lock()
	if cancel, ok := s.activeJobs[importID]; ok {
		cancel()
		delete(s.activeJobs, importID)
	}
	s.jobsMu.Unlock()

	// Clean up disk workspace if not already attached to a completed project
	if imp.ProjectID == nil && imp.SnapshotPath != "" {
		_ = os.RemoveAll(imp.SnapshotPath)
	}

	_ = s.updateImportState(ctx, importID, models.ImportStatusCancelled, 0, "Import cancelled by user", "Import cancelled by user", "cancelled", nil, nil)
	return s.GetImport(ctx, userID, importID)
}

// RetryImport resets a failed or cancelled import and restarts execution.
func (s *RepositoryImportService) RetryImport(ctx context.Context, userID uuid.UUID, importID uuid.UUID) (*models.RepositoryImport, error) {
	imp, err := s.GetImport(ctx, userID, importID)
	if err != nil {
		return nil, err
	}

	owner, repo, err := CleanGitHubRepoReference(imp.RepositoryFullName)
	if err != nil {
		return nil, err
	}

	// Purge previous failed workspace
	if imp.SnapshotPath != "" {
		_ = os.RemoveAll(imp.SnapshotPath)
	}

	_ = s.updateImportState(ctx, importID, models.ImportStatusPending, 0, "Retrying repository import", "", "", nil, nil)

	jobCtx, cancel := context.WithCancel(context.Background())
	s.jobsMu.Lock()
	s.activeJobs[importID] = cancel
	s.jobsMu.Unlock()

	go s.runImportPipeline(jobCtx, imp.ID, userID, owner, repo, imp.Branch, imp.RootDir)

	return s.GetImport(ctx, userID, importID)
}

// MarkCompleted associates an import with a newly created project and marks it completed.
func (s *RepositoryImportService) MarkCompleted(ctx context.Context, userID uuid.UUID, importID uuid.UUID, projectID uuid.UUID) error {
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
		return 0, nil
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
			if path != "" {
				_ = os.RemoveAll(path)
			}
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
