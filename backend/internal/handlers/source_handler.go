package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/services"
)

type SourceHandler struct {
	sourceService *services.SourceService
}

func NewSourceHandler(sourceService *services.SourceService) *SourceHandler {
	return &SourceHandler{sourceService: sourceService}
}

// Upload handles POST /api/sources/upload
func (h *SourceHandler) Upload(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// 100MB max memory buffer for parsing multipart
	err := r.ParseMultipartForm(100 * 1024 * 1024)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse multipart form: "+err.Error())
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

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

		writeJSON(w, http.StatusCreated, res)
		return
	}

	// 2. Check if individual directory files were uploaded
	allFiles := r.MultipartForm.File["files"]
	if len(allFiles) == 0 {
		allFiles = r.MultipartForm.File["file"]
	}

	if len(allFiles) == 0 {
		writeError(w, http.StatusBadRequest, "no files or source archive provided in upload")
		return
	}

	res, err := h.sourceService.IngestMultipartFiles(r.Context(), userID, allFiles)
	if err != nil {
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
