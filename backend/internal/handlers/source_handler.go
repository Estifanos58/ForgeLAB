package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/services"
)

// MaxUncompressedSourceBytes is the maximum allowed uncompressed source code size (100MB).
const MaxUncompressedSourceBytes = 100 * 1024 * 1024 // 100MB

// MaxUploadBytes is the maximum allowed HTTP request body size for POST /api/sources/upload (105MB).
// Provides 5MB headroom for multipart form-data boundary framing and MIME headers.
const MaxUploadBytes = 105 * 1024 * 1024 // 105MB

type SourceHandler struct {
	sourceService *services.SourceService
}

func NewSourceHandler(sourceService *services.SourceService) *SourceHandler {
	return &SourceHandler{sourceService: sourceService}
}

// Upload handles POST /api/sources/upload
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

	parseStart := time.Now()
	// Buffer up to 32MB in RAM, spill excess to temp files on disk (cleaned up by RemoveAll)
	err := r.ParseMultipartForm(32 * 1024 * 1024)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) || strings.Contains(strings.ToLower(err.Error()), "request body too large") {
			slog.Warn("source upload rejected: body stream exceeded limit",
				"user_id", userID.String(),
				"limit", MaxUploadBytes,
				"duration_ms", time.Since(startTime).Milliseconds(),
			)
			writeError(w, http.StatusRequestEntityTooLarge, "upload size exceeds the maximum allowed limit of 100MB")
			return
		}
		slog.Warn("source upload rejected: multipart parsing failed",
			"user_id", userID.String(),
			"error", err.Error(),
			"duration_ms", time.Since(startTime).Milliseconds(),
		)
		writeError(w, http.StatusBadRequest, "failed to parse multipart form: "+err.Error())
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	slog.Debug("source upload multipart parsed",
		"user_id", userID.String(),
		"parse_duration_ms", time.Since(parseStart).Milliseconds(),
	)

	// 1. Check if archive was uploaded (zip / tar.gz)
	fileHeaders := r.MultipartForm.File["archive"]
	if len(fileHeaders) == 0 {
		fileHeaders = r.MultipartForm.File["file"]
	}

	if len(fileHeaders) == 1 && (strings.HasSuffix(strings.ToLower(fileHeaders[0].Filename), ".zip") ||
		strings.HasSuffix(strings.ToLower(fileHeaders[0].Filename), ".tar.gz") ||
		strings.HasSuffix(strings.ToLower(fileHeaders[0].Filename), ".tgz")) {

		fh := fileHeaders[0]
		f, err := fh.Open()
		if err != nil {
			slog.Warn("source upload archive open failed",
				"user_id", userID.String(),
				"error", err.Error(),
				"duration_ms", time.Since(startTime).Milliseconds(),
			)
			writeError(w, http.StatusBadRequest, "failed to open archive: "+err.Error())
			return
		}
		defer f.Close()

		var res *services.SourceUploadResult
		if strings.HasSuffix(strings.ToLower(fh.Filename), ".zip") {
			res, err = h.sourceService.IngestZip(r.Context(), userID, f, fh.Size)
		} else {
			res, err = h.sourceService.IngestTarGz(r.Context(), userID, f)
		}

		if err != nil {
			slog.Warn("source upload archive extraction failed",
				"user_id", userID.String(),
				"reason", err.Error(),
				"duration_ms", time.Since(startTime).Milliseconds(),
			)
			if errors.Is(err, services.ErrPathTraversalDetected) {
				writeError(w, http.StatusBadRequest, "security violation: path traversal detected in archive")
				return
			}
			if errors.Is(err, services.ErrArchiveTooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, err.Error())
				return
			}
			writeError(w, http.StatusBadRequest, "failed to extract source archive: "+err.Error())
			return
		}

		slog.Info("source upload completed successfully (archive)",
			"user_id", userID.String(),
			"source_id", res.SourceID.String(),
			"accepted_files", res.FilesCount,
			"total_bytes", res.TotalBytes,
			"duration_ms", time.Since(startTime).Milliseconds(),
		)
		writeJSON(w, http.StatusCreated, res)
		return
	}

	// 2. Check if individual directory files were uploaded
	allFiles := r.MultipartForm.File["files"]
	if len(allFiles) == 0 {
		allFiles = r.MultipartForm.File["file"]
	}

	if len(allFiles) == 0 {
		slog.Warn("source upload rejected: no files in multipart payload",
			"user_id", userID.String(),
			"duration_ms", time.Since(startTime).Milliseconds(),
		)
		writeError(w, http.StatusBadRequest, "no files or source archive provided in upload")
		return
	}

	receivedFiles := len(allFiles)
	slog.Debug("processing uploaded directory files",
		"user_id", userID.String(),
		"received_files", receivedFiles,
	)

	res, err := h.sourceService.IngestMultipartFiles(r.Context(), userID, allFiles)
	if err != nil {
		slog.Warn("source upload directory ingestion failed",
			"user_id", userID.String(),
			"received_files", receivedFiles,
			"reason", err.Error(),
			"duration_ms", time.Since(startTime).Milliseconds(),
		)
		if errors.Is(err, services.ErrPathTraversalDetected) {
			writeError(w, http.StatusBadRequest, "security violation: path traversal detected in uploaded files")
			return
		}
		if errors.Is(err, services.ErrArchiveTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to ingest source files: "+err.Error())
		return
	}

	slog.Info("source upload completed successfully (directory)",
		"user_id", userID.String(),
		"source_id", res.SourceID.String(),
		"received_files", receivedFiles,
		"accepted_files", res.FilesCount,
		"total_bytes", res.TotalBytes,
		"duration_ms", time.Since(startTime).Milliseconds(),
	)
	writeJSON(w, http.StatusCreated, res)
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
