package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/agent"
	"github.com/forgelab/backend/internal/detector"
	"github.com/forgelab/backend/internal/docker"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
)

// MaxUncompressedSourceBytes is the maximum allowed uncompressed source code size (100MB).
const MaxUncompressedSourceBytes = 100 * 1024 * 1024 // 100MB

// MaxUploadBytes is the maximum allowed HTTP request body size for POST /api/sources/upload (105MB).
// Provides 5MB headroom for multipart form-data boundary framing and MIME headers.
const MaxUploadBytes = 105 * 1024 * 1024 // 105MB

type SourceHandler struct {
	sourceService           *services.SourceService
	pathValidator           *security.PathValidator
	localValidationSessions sync.Map
}

func NewSourceHandler(sourceService *services.SourceService, pathValidator *security.PathValidator) *SourceHandler {
	return &SourceHandler{
		sourceService: sourceService,
		pathValidator: pathValidator,
	}
}

// Upload handles POST /api/sources/upload by streaming multipart parts sequentially directly
// to a temporary staging workspace, avoiding ParseMultipartForm disk/RAM buffering.
func (h *SourceHandler) Upload(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	slog.Info("source upload initiated",
		"user_id", userID.String(),
		"content_length", r.ContentLength,
		"content_type", r.Header.Get("Content-Type"),
	)

	// Early HTTP boundary check: reject if declared Content-Length exceeds MaxUploadBytes
	if r.ContentLength > MaxUploadBytes {
		slog.Warn("source upload rejected: content-length exceeds maximum limit",
			"user_id", userID.String(),
			"content_length", r.ContentLength,
			"limit", MaxUploadBytes,
			"duration_ms", time.Since(startTime).Milliseconds(),
		)
		writeError(w, http.StatusRequestEntityTooLarge, "upload size exceeds the maximum allowed limit of 100MB")
		return
	}

	// Protect server by limiting the streaming request body at HTTP boundary
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadBytes)

	// Stream multipart parts sequentially without buffering the entire form in memory or temp files
	reader, err := r.MultipartReader()
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) || strings.Contains(strings.ToLower(err.Error()), "request body too large") {
			writeError(w, http.StatusRequestEntityTooLarge, "upload size exceeds the maximum allowed limit of 100MB")
			return
		}
		slog.Warn("source upload rejected: invalid multipart request",
			"user_id", userID.String(),
			"error", err.Error(),
			"duration_ms", time.Since(startTime).Milliseconds(),
		)
		writeError(w, http.StatusBadRequest, "failed to read multipart request: "+err.Error())
		return
	}

	res, err := h.sourceService.IngestMultipartStream(r.Context(), userID, reader)
	if err != nil {
		// Detect client cancellation (e.g. user aborted XHR or closed tab)
		if errors.Is(err, r.Context().Err()) || strings.Contains(strings.ToLower(err.Error()), "context canceled") {
			slog.Info("source upload cancelled by client",
				"user_id", userID.String(),
				"duration_ms", time.Since(startTime).Milliseconds(),
			)
			return
		}

		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) || errors.Is(err, services.ErrArchiveTooLarge) ||
			strings.Contains(strings.ToLower(err.Error()), "request body too large") {
			slog.Warn("source upload rejected: payload exceeds size limit",
				"user_id", userID.String(),
				"error", err.Error(),
				"duration_ms", time.Since(startTime).Milliseconds(),
			)
			writeError(w, http.StatusRequestEntityTooLarge, "upload size exceeds the maximum allowed limit of 100MB")
			return
		}

		if errors.Is(err, services.ErrPathTraversalDetected) {
			slog.Warn("source upload rejected: path traversal detected",
				"user_id", userID.String(),
				"duration_ms", time.Since(startTime).Milliseconds(),
			)
			writeError(w, http.StatusBadRequest, "security violation: path traversal detected in uploaded files")
			return
		}

		if errors.Is(err, services.ErrInvalidSourceArchive) {
			writeError(w, http.StatusBadRequest, "invalid source archive: "+err.Error())
			return
		}

		slog.Warn("source upload failed",
			"user_id", userID.String(),
			"reason", err.Error(),
			"duration_ms", time.Since(startTime).Milliseconds(),
		)
		writeError(w, http.StatusBadRequest, "failed to ingest source files: "+err.Error())
		return
	}

	slog.Info("source upload stream accepted",
		"user_id", userID.String(),
		"source_id", res.SourceID.String(),
		"status", res.Status,
		"accepted_files", res.FilesCount,
		"total_bytes", res.TotalBytes,
		"duration_ms", time.Since(startTime).Milliseconds(),
	)

	writeJSON(w, http.StatusCreated, res)
}

// GetStatus handles GET /api/sources/{id}
func (h *SourceHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	idStr := chi.URLParam(r, "id")
	sourceID, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid source ID format")
		return
	}

	status, err := h.sourceService.GetSourceStatus(r.Context(), userID, sourceID)
	if err != nil {
		if errors.Is(err, services.ErrUnauthorizedSource) {
			writeError(w, http.StatusForbidden, "unauthorized: you do not own this source workspace")
			return
		}
		if errors.Is(err, services.ErrSourceDirNotFound) {
			writeError(w, http.StatusNotFound, "source workspace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to retrieve source status: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, status)
}

// Delete handles DELETE /api/sources/{id}
func (h *SourceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	idStr := chi.URLParam(r, "id")
	sourceID, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid source ID format")
		return
	}

	if err := h.sourceService.DeleteSource(r.Context(), userID, sourceID); err != nil {
		if errors.Is(err, services.ErrUnauthorizedSource) {
			writeError(w, http.StatusForbidden, "unauthorized: you do not own this source workspace")
			return
		}
		if errors.Is(err, services.ErrSourceDirNotFound) {
			writeError(w, http.StatusNotFound, "source workspace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete source workspace: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "source workspace deleted successfully"})
}

// ValidateLocalPathRequest defines payload for POST /api/sources/local/validate
type ValidateLocalPathRequest struct {
	RepositoryPath string `json:"repository_path"`
	Async          bool   `json:"async,omitempty"`
}

// ValidateLocalPathResponse defines metadata response for validated local paths
type ValidateLocalPathResponse struct {
	SessionID       string `json:"session_id,omitempty"`
	Status          string `json:"status,omitempty"` // "ready", "scanning", "failed"
	Valid           bool   `json:"valid"`
	RepositoryPath  string `json:"repository_path"`
	ProjectName     string `json:"project_name"`
	FilesCount      int    `json:"files_count"`
	TotalBytes      int64  `json:"total_bytes"`
	Runtime         string `json:"runtime"`
	Framework       string `json:"framework"`
	BuildStrategy   string `json:"build_strategy"`
	DockerfilePath  string `json:"dockerfile_path"`
	BuildContext    string `json:"build_context"`
	BuildCommand    string `json:"build_command"`
	StartCommand    string `json:"start_command"`
	SuggestedPort   int    `json:"suggested_port"`
	HealthStrategy  string `json:"health_strategy"`
	HealthCheckPath string `json:"health_check_path"`
	Error           string `json:"error,omitempty"`
}

type LocalValidationSession struct {
	SessionID      string                     `json:"session_id"`
	UserID         uuid.UUID                  `json:"user_id"`
	Status         string                     `json:"status"` // "scanning", "ready", "failed"
	RepositoryPath string                     `json:"repository_path"`
	FilesScanned   int                        `json:"files_scanned"`
	TotalBytes     int64                      `json:"total_bytes"`
	Result         *ValidateLocalPathResponse `json:"result,omitempty"`
	Error          string                     `json:"error,omitempty"`
	CreatedAt      time.Time                  `json:"created_at"`
	UpdatedAt      time.Time                  `json:"updated_at"`
	mu             sync.RWMutex
}

// ValidateLocalPath handles POST /api/sources/local/validate
func (h *SourceHandler) ValidateLocalPath(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req ValidateLocalPathRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request payload")
		return
	}

	trimmedPath := strings.TrimSpace(req.RepositoryPath)
	if trimmedPath == "" {
		writeError(w, http.StatusBadRequest, "repository_path is required")
		return
	}

	if h.pathValidator == nil {
		writeError(w, http.StatusInternalServerError, "path validator service is not configured")
		return
	}

	canonicalPath, err := h.pathValidator.ValidateSourcePath(trimmedPath)
	if err != nil {
		if errors.Is(err, security.ErrPathNotExist) {
			writeError(w, http.StatusBadRequest, "The selected directory does not exist or ForgeLAB cannot access it from the backend environment.")
			return
		}
		if errors.Is(err, security.ErrPathNotDirectory) {
			writeError(w, http.StatusBadRequest, "The selected path is not a directory.")
			return
		}
		if errors.Is(err, security.ErrPathNotAllowed) {
			writeError(w, http.StatusForbidden, "This directory is outside the configured ForgeLAB source roots.")
			return
		}
		if errors.Is(err, security.ErrRestrictedSystemPath) {
			writeError(w, http.StatusForbidden, "Access to system directory is forbidden.")
			return
		}
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Path validation failed: %v", err))
		return
	}

	sessionID := uuid.New().String()
	session := &LocalValidationSession{
		SessionID:      sessionID,
		UserID:         userID,
		Status:         "scanning",
		RepositoryPath: canonicalPath,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	h.localValidationSessions.Store(sessionID, session)

	go h.cleanupValidationSessions()

	doneCh := make(chan struct{})

	go func() {
		defer close(doneCh)
		matcher, _ := docker.LoadDockerignore(canonicalPath)
		filesCount := 0
		var totalBytes int64

		_ = filepath.WalkDir(canonicalPath, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, err := filepath.Rel(canonicalPath, path)
			if err != nil || rel == "." || rel == "" {
				return nil
			}
			slashRel := filepath.ToSlash(rel)
			isDir := d.IsDir()

			if matcher != nil {
				if isDir && matcher.CanSkipDir(slashRel) {
					return filepath.SkipDir
				}
				if matcher.Matches(slashRel, isDir) {
					if isDir {
						return filepath.SkipDir
					}
					return nil
				}
			}

			if !isDir {
				info, err := d.Info()
				if err == nil && info.Mode().IsRegular() {
					filesCount++
					totalBytes += info.Size()
					if filesCount%50 == 0 {
						session.mu.Lock()
						session.FilesScanned = filesCount
						session.TotalBytes = totalBytes
						session.UpdatedAt = time.Now()
						session.mu.Unlock()
					}
				}
			}
			return nil
		})

		detectionRes, err := detector.Detect(canonicalPath)
		if err != nil {
			detectionRes = &detector.DetectionResult{
				Runtime:         "generic",
				Framework:       "Generic",
				BuildStrategy:   "auto",
				SuggestedPort:   8080,
				HealthCheckPath: "/health",
				HealthStrategy:  "auto",
			}
		}

		hasDockerfile := false
		if _, err := os.Stat(filepath.Join(canonicalPath, "Dockerfile")); err == nil {
			hasDockerfile = true
		}

		buildStrategy := detectionRes.BuildStrategy
		if hasDockerfile {
			buildStrategy = "dockerfile"
		} else if buildStrategy == "" {
			buildStrategy = "auto"
		}

		projectName := filepath.Base(canonicalPath)
		if projectName == "" || projectName == "/" || projectName == "." {
			projectName = "local-project"
		}

		resp := ValidateLocalPathResponse{
			SessionID:       sessionID,
			Status:          "ready",
			Valid:           true,
			RepositoryPath:  canonicalPath,
			ProjectName:     projectName,
			FilesCount:      filesCount,
			TotalBytes:      totalBytes,
			Runtime:         detectionRes.Runtime,
			Framework:       detectionRes.Framework,
			BuildStrategy:   buildStrategy,
			DockerfilePath:  "Dockerfile",
			BuildContext:    ".",
			BuildCommand:    detectionRes.BuildCommand,
			StartCommand:    detectionRes.StartCommand,
			SuggestedPort:   detectionRes.SuggestedPort,
			HealthStrategy:  detectionRes.HealthStrategy,
			HealthCheckPath: detectionRes.HealthCheckPath,
		}

		session.mu.Lock()
		session.Status = "ready"
		session.FilesScanned = filesCount
		session.TotalBytes = totalBytes
		session.Result = &resp
		session.UpdatedAt = time.Now()
		session.mu.Unlock()
	}()

	if req.Async {
		writeJSON(w, http.StatusAccepted, ValidateLocalPathResponse{
			SessionID:      sessionID,
			Status:         "scanning",
			Valid:          true,
			RepositoryPath: canonicalPath,
		})
		return
	}

	select {
	case <-doneCh:
		session.mu.RLock()
		res := session.Result
		session.mu.RUnlock()
		if res != nil {
			writeJSON(w, http.StatusOK, res)
			return
		}
	case <-time.After(150 * time.Millisecond):
		writeJSON(w, http.StatusAccepted, ValidateLocalPathResponse{
			SessionID:      sessionID,
			Status:         "scanning",
			Valid:          true,
			RepositoryPath: canonicalPath,
		})
		return
	}
}

// GetLocalValidationStatus handles GET /api/sources/local/validate/{sessionId}
func (h *SourceHandler) GetLocalValidationStatus(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	sessionID := chi.URLParam(r, "sessionId")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "sessionId is required")
		return
	}

	val, ok := h.localValidationSessions.Load(sessionID)
	if !ok {
		writeError(w, http.StatusNotFound, "validation session not found")
		return
	}

	session := val.(*LocalValidationSession)
	if session.UserID != userID {
		writeError(w, http.StatusForbidden, "unauthorized access to validation session")
		return
	}

	session.mu.RLock()
	defer session.mu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id":      session.SessionID,
		"status":          session.Status,
		"repository_path": session.RepositoryPath,
		"files_scanned":   session.FilesScanned,
		"total_bytes":     session.TotalBytes,
		"result":          session.Result,
		"error":           session.Error,
		"updated_at":      session.UpdatedAt,
	})
}

func (h *SourceHandler) cleanupValidationSessions() {
	now := time.Now()
	h.localValidationSessions.Range(func(key, value any) bool {
		sess, ok := value.(*LocalValidationSession)
		if ok && now.Sub(sess.CreatedAt) > 30*time.Minute {
			h.localValidationSessions.Delete(key)
		}
		return true
	})
}

// CreateAgentSession handles POST /api/sources/agent/session
// Issues a short-lived authenticated session token for the user
func (h *SourceHandler) CreateAgentSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req struct {
		AgentID string `json:"agent_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	sm := agent.GetGlobalSessionManager()
	session, err := sm.CreateSession(userID, req.AgentID, 30*time.Minute)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create agent session: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"session_id": session.ID.String(),
		"token":      session.Token,
		"agent_id":   session.AgentID,
		"expires_at": session.ExpiresAt.Format(time.RFC3339),
	})
}

// ValidateAgentSession handles POST /api/sources/agent/session/validate
// Used by local agent or backend to verify and bind sessions
func (h *SourceHandler) ValidateAgentSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token      string `json:"token"`
		SourceID   string `json:"source_id"`
		AgentID    string `json:"agent_id"`
		FolderName string `json:"folder_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Token) == "" {
		writeError(w, http.StatusBadRequest, "valid session token is required")
		return
	}

	sm := agent.GetGlobalSessionManager()
	var session *agent.AgentSession
	var err error

	if req.SourceID != "" {
		sourceUUID, parseErr := uuid.Parse(req.SourceID)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid source_id format")
			return
		}
		session, err = sm.BindSource(req.Token, sourceUUID, req.FolderName, req.AgentID)
	} else {
		session, err = sm.ValidateToken(req.Token)
	}

	if err != nil {
		if errors.Is(err, agent.ErrSessionExpired) {
			writeError(w, http.StatusUnauthorized, "agent session has expired")
			return
		}
		if errors.Is(err, agent.ErrUnauthorized) {
			writeError(w, http.StatusForbidden, "unauthorized session access: "+err.Error())
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid agent session: "+err.Error())
		return
	}

	resp := map[string]interface{}{
		"valid":      true,
		"session_id": session.ID.String(),
		"user_id":    session.UserID.String(),
		"agent_id":   session.AgentID,
		"expires_at": session.ExpiresAt.Format(time.RFC3339),
	}
	if session.SourceID != nil {
		resp["source_id"] = session.SourceID.String()
	}
	writeJSON(w, http.StatusOK, resp)
}

// RegisterAgentSourceRequest defines payload for POST /api/sources/agent/register
type RegisterAgentSourceRequest struct {
	SessionID  string                 `json:"session_id"`
	Token      string                 `json:"token"`
	SourceID   string                 `json:"source_id"`
	AgentID    string                 `json:"agent_id"`
	FolderName string                 `json:"folder_name"`
	Metadata   map[string]interface{} `json:"metadata"`
}

// RegisterAgentSource handles POST /api/sources/agent/register
func (h *SourceHandler) RegisterAgentSource(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req RegisterAgentSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request payload")
		return
	}

	sm := agent.GetGlobalSessionManager()
	var verifiedSourceID uuid.UUID
	var verifiedAgentID string
	var folderName string = req.FolderName

	// If session_id is provided, verify session ownership and bound source/agent IDs
	if strings.TrimSpace(req.SessionID) != "" {
		sessionUUID, err := uuid.Parse(req.SessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid session_id format")
			return
		}
		sess, err := sm.VerifyForRegistration(userID, sessionUUID, req.Token)
		if err != nil {
			slog.Warn("agent source registration rejected: session verification failed",
				"user_id", userID.String(),
				"session_id", req.SessionID,
				"error", err.Error(),
			)
			if errors.Is(err, agent.ErrUnauthorized) {
				writeError(w, http.StatusForbidden, "unauthorized: session does not belong to authenticated user")
				return
			}
			if errors.Is(err, agent.ErrSessionExpired) {
				writeError(w, http.StatusUnauthorized, "agent session has expired; please re-select folder")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid session for registration: "+err.Error())
			return
		}

		// Security: Do NOT trust client-provided source IDs or agent IDs!
		// Authoritative IDs come strictly from the verified session.
		verifiedSourceID = *sess.SourceID
		verifiedAgentID = sess.AgentID
		if sess.FolderName != "" {
			folderName = sess.FolderName
		}

		// Reject forged client-provided IDs if they do not match the session
		if req.SourceID != "" && req.SourceID != verifiedSourceID.String() {
			writeError(w, http.StatusForbidden, "security violation: client-provided source_id does not match authenticated session")
			return
		}
		if req.AgentID != "" && req.AgentID != verifiedAgentID {
			writeError(w, http.StatusForbidden, "security violation: client-provided agent_id does not match authenticated session")
			return
		}
	} else if strings.TrimSpace(req.Token) != "" {
		// Validated via token
		sess, err := sm.ValidateToken(req.Token)
		if err != nil || sess.UserID != userID || sess.SourceID == nil {
			writeError(w, http.StatusForbidden, "unauthorized or unverified agent session")
			return
		}
		verifiedSourceID = *sess.SourceID
		verifiedAgentID = sess.AgentID
		if sess.FolderName != "" {
			folderName = sess.FolderName
		}
	} else {
		// If no session token is provided, only allow in non-production/test fallback
		if os.Getenv("APP_ENV") == "production" || os.Getenv("FORGELAB_ENV") == "production" {
			writeError(w, http.StatusForbidden, "authenticated agent session required in production")
			return
		}
		sourceUUID, err := uuid.Parse(req.SourceID)
		if err != nil {
			sourceUUID = uuid.New()
		}
		verifiedSourceID = sourceUUID
		verifiedAgentID = req.AgentID
	}

	meta := req.Metadata
	if meta == nil {
		meta = make(map[string]interface{})
	}
	meta["folder_name"] = folderName
	if req.Token != "" {
		meta["session_token"] = req.Token
	}

	sourceRecord := &models.Source{
		ID:              verifiedSourceID,
		OwnerID:         userID,
		SourceType:      models.SourceTypeLocalAgent,
		SourceReference: verifiedSourceID.String(),
		AgentID:         verifiedAgentID,
		Metadata:        meta,
	}

	if h.sourceService != nil {
		if err := h.sourceService.SaveSource(r.Context(), sourceRecord); err != nil {
			slog.Error("failed to save agent source in database", "source_id", verifiedSourceID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to register agent source: "+err.Error())
			return
		}
	}

	res := map[string]interface{}{
		"source": map[string]interface{}{
			"id":               verifiedSourceID.String(),
			"source_type":      models.SourceTypeLocalAgent,
			"source_reference": verifiedSourceID.String(),
			"agent_id":         verifiedAgentID,
			"folder_name":      folderName,
			"metadata":         meta,
		},
		"source_id":   verifiedSourceID.String(),
		"owner_id":    userID.String(),
		"agent_id":    verifiedAgentID,
		"type":        models.SourceTypeLocalAgent,
		"status":      "ready",
		"folder_name": folderName,
		"metadata":    meta,
	}

	writeJSON(w, http.StatusCreated, res)
}
