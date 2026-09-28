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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

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
	sourceService *services.SourceService
	pathValidator *security.PathValidator
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
}

// ValidateLocalPathResponse defines metadata response for validated local paths
type ValidateLocalPathResponse struct {
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

// ValidateLocalPath handles POST /api/sources/local/validate
func (h *SourceHandler) ValidateLocalPath(w http.ResponseWriter, r *http.Request) {
	_, ok := getUserIDFromContext(r)
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

	// Efficiently inspect files and calculate total size without walking ignored directories
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
			}
		}
		return nil
	})

	// Run detector on canonical path
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

	// Check if Dockerfile exists in project
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

	writeJSON(w, http.StatusOK, resp)
}

// RegisterAgentSourceRequest defines payload for POST /api/sources/agent/register
type RegisterAgentSourceRequest struct {
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

	sourceUUID, err := uuid.Parse(req.SourceID)
	if err != nil {
		sourceUUID = uuid.New()
	}

	meta := req.Metadata
	if meta == nil {
		meta = make(map[string]interface{})
	}
	meta["folder_name"] = req.FolderName

	res := map[string]interface{}{
		"source_id":   sourceUUID.String(),
		"owner_id":    userID.String(),
		"agent_id":    req.AgentID,
		"type":        models.SourceTypeLocalAgent,
		"status":      "ready",
		"folder_name": req.FolderName,
		"metadata":    meta,
	}

	writeJSON(w, http.StatusCreated, res)
}
