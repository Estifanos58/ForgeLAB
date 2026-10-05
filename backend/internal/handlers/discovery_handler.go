package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
)

// GeneratePlanRequest specifies input parameters for generating a deployment plan across any source type.
type GeneratePlanRequest struct {
	SourceType      string `json:"source_type"`      // "github", "local_directory", "local_upload", "local_agent"
	SourceReference string `json:"source_reference"` // "owner/repo" or upload source UUID
	RepositoryPath  string `json:"repository_path"`  // local filesystem path
	Branch          string `json:"branch"`           // git branch
	RootDir         string `json:"root_dir"`         // optional subfolder
	AgentID         string `json:"agent_id"`         // optional local agent ID
	ProjectID       string `json:"project_id"`       // optional existing project ID for env conflict checks
}

// GeneratePlanResponse returns the authoritative discovery result and snapshot deployment plan.
type GeneratePlanResponse struct {
	Discovery *discovery.DiscoveryResult `json:"discovery"`
	Plan      *discovery.DeploymentPlan  `json:"plan"`
}

// DiscoveryHandler handles source discovery and deployment plan generation.
type DiscoveryHandler struct {
	githubService *services.GitHubService
	sourceService *services.SourceService
	pathValidator *security.PathValidator
	secretService *services.SecretService
}

// NewDiscoveryHandler creates a new DiscoveryHandler.
func NewDiscoveryHandler(
	githubService *services.GitHubService,
	sourceService *services.SourceService,
	pathValidator *security.PathValidator,
	secretService *services.SecretService,
) *DiscoveryHandler {
	return &DiscoveryHandler{
		githubService: githubService,
		sourceService: sourceService,
		pathValidator: pathValidator,
		secretService: secretService,
	}
}

// GeneratePlan handles POST /api/discovery/plan
func (h *DiscoveryHandler) GeneratePlan(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req GeneratePlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var workspaceDir string
	var cleanupDir func()
	var sourceRevision string

	switch req.SourceType {
	case models.SourceTypeGitHub:
		parts := strings.Split(req.SourceReference, "/")
		if len(parts) != 2 {
			writeError(w, http.StatusBadRequest, "invalid GitHub repository reference. Expected 'owner/repo'")
			return
		}
		if h.githubService == nil {
			writeError(w, http.StatusInternalServerError, "GitHub integration service is not available")
			return
		}

		branch := req.Branch
		if branch == "" {
			branch = "main"
		}

		tempDir, err := os.MkdirTemp("", "forgelab-discovery-*")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create isolated temp workspace")
			return
		}
		cleanupDir = func() { _ = os.RemoveAll(tempDir) }

		pinnedRef, matErr := h.githubService.MaterializeGitHubSnapshot(r.Context(), userID, parts[0], parts[1], branch, tempDir)
		if matErr != nil {
			cleanupDir()
			slog.Error("failed to materialize github snapshot for plan", "repo", req.SourceReference, "error", matErr)
			if errors.Is(matErr, services.ErrGitHubNotConnected) {
				writeError(w, http.StatusForbidden, "GitHub account not connected")
				return
			}
			writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("failed to fetch repository archive: %v", matErr))
			return
		}
		workspaceDir = tempDir
		sourceRevision = pinnedRef

	case models.SourceTypeLocalDirectory:
		if req.RepositoryPath == "" {
			writeError(w, http.StatusBadRequest, "repository_path is required for local directory sources")
			return
		}
		canonicalPath, valErr := h.pathValidator.ValidateSourcePath(req.RepositoryPath)
		if valErr != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid directory path: %v", valErr))
			return
		}
		workspaceDir = canonicalPath
		hash := sha256.Sum256([]byte(fmt.Sprintf("dir:%s", canonicalPath)))
		sourceRevision = fmt.Sprintf("fp_%s", hex.EncodeToString(hash[:16]))

	case models.SourceTypeLocalUpload:
		if req.SourceReference == "" {
			writeError(w, http.StatusBadRequest, "source_reference is required for uploaded sources")
			return
		}
		sourceUUID, parseErr := uuid.Parse(req.SourceReference)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid source reference UUID")
			return
		}
		if h.sourceService == nil {
			writeError(w, http.StatusInternalServerError, "source service unavailable")
			return
		}
		p, getErr := h.sourceService.GetSourcePath(r.Context(), userID, sourceUUID)
		if getErr != nil {
			writeError(w, http.StatusNotFound, "uploaded source not found")
			return
		}
		workspaceDir = p
		hash := sha256.Sum256([]byte(fmt.Sprintf("upload:%s", sourceUUID.String())))
		sourceRevision = fmt.Sprintf("fp_%s", hex.EncodeToString(hash[:16]))

	case models.SourceTypeLocalAgent:
		if req.RepositoryPath != "" {
			canonicalPath, valErr := h.pathValidator.ValidateSourcePath(req.RepositoryPath)
			if valErr == nil {
				workspaceDir = canonicalPath
			}
		}
		if workspaceDir == "" {
			workspaceDir = "."
		}
		sourceRevision = fmt.Sprintf("agent_%s", req.SourceReference)

	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported source type '%s'", req.SourceType))
		return
	}

	if cleanupDir != nil {
		defer cleanupDir()
	}

	// 1. Run authoritative discovery engine
	discResult, err := discovery.Discover(workspaceDir)
	if err != nil {
		slog.Error("discovery failed", "workspace", workspaceDir, "error", err)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("discovery failed: %v", err))
		return
	}

	// 2. Generate snapshot deployment plan
	var projUUID *uuid.UUID
	if req.ProjectID != "" {
		if parsed, pErr := uuid.Parse(req.ProjectID); pErr == nil {
			projUUID = &parsed
		}
	}

	plan, err := discovery.GeneratePlan(discResult, discovery.PlanOptions{
		SourceRevision: sourceRevision,
		SourceType:     req.SourceType,
		ProjectID:      projUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to generate plan: %v", err))
		return
	}

	// 3. Track environment variable provenance and conflicts without overwriting existing ForgeLab values
	if projUUID != nil && h.secretService != nil {
		existingEnv, _, _ := h.secretService.GetDecryptedEnvMap(r.Context(), *projUUID, nil, models.EnvScopeRuntime)
		for i := range plan.Environment {
			if _, alreadySet := existingEnv[plan.Environment[i].Key]; alreadySet {
				plan.Environment[i].HasConflict = true
				plan.Environment[i].ConflictResolution = "overridden_by_forgelab"
				plan.Environment[i].ActiveValue = "forgelab"
			}
		}
	}

	resp := GeneratePlanResponse{
		Discovery: discResult,
		Plan:      plan,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
