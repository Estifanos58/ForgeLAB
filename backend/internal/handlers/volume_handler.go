package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/docker"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

// VolumeManager defines the volume inspection, backup, and restore operations needed by VolumeHandler.
type VolumeManager interface {
	ListProjectVolumes(ctx context.Context, projectID uuid.UUID) (*docker.ProjectVolumesResponse, error)
	BackupProjectVolume(ctx context.Context, projectID uuid.UUID, volumeName string, w io.Writer) error
	RestoreProjectVolume(ctx context.Context, projectID uuid.UUID, volumeName string, r io.Reader) (*docker.VolumeRestoreResult, error)
}

var _ VolumeManager = (*docker.Engine)(nil)

// VolumeHandler exposes endpoints for project persistent volumes, Docker disk usage, backup, and restore.
type VolumeHandler struct {
	projectService *services.ProjectService
	volumeManager  VolumeManager
	auditService   *services.AuditService
}

// NewVolumeHandler creates a VolumeHandler.
func NewVolumeHandler(projectService *services.ProjectService, volumeManager VolumeManager) *VolumeHandler {
	if eng, ok := volumeManager.(*docker.Engine); ok && eng == nil {
		volumeManager = nil
	}
	return &VolumeHandler{
		projectService: projectService,
		volumeManager:  volumeManager,
	}
}

// SetAuditService configures operational audit logging for VolumeHandler.
func (h *VolumeHandler) SetAuditService(auditSvc *services.AuditService) {
	h.auditService = auditSvc
}

func (h *VolumeHandler) recordAudit(r *http.Request, userID uuid.UUID, project *models.Project, action, volumeName, status, message string, metadata map[string]any) {
	if h.auditService == nil || project == nil {
		return
	}
	pid := project.ID
	h.auditService.RecordBestEffort(r.Context(), services.AuditRecordInput{
		ActorID:      userID,
		ActorLabel:   getUserEmailFromContext(r),
		IsGuest:      models.IsGuest(userID),
		ProjectID:    &pid,
		ProjectName:  project.Name,
		Action:       action,
		ResourceType: "volume",
		ResourceID:   volumeName,
		Status:       status,
		Message:      message,
		Metadata:     metadata,
		IPAddress:    getClientIP(r),
	})
}

func (h *VolumeHandler) authorizeProject(w http.ResponseWriter, r *http.Request) (*models.Project, bool) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}

	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid project ID")
		return nil, false
	}

	if h.projectService == nil {
		writeError(w, http.StatusServiceUnavailable, "project service unavailable")
		return nil, false
	}

	project, err := h.projectService.GetProject(r.Context(), projectID, userID)
	if err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return nil, false
		}
		if errors.Is(err, services.ErrProjectNotOwned) {
			writeError(w, http.StatusForbidden, "access denied")
			return nil, false
		}
		writeError(w, http.StatusInternalServerError, "failed to verify project access")
		return nil, false
	}

	return project, true
}

// List handles GET /api/projects/{id}/volumes.
func (h *VolumeHandler) List(w http.ResponseWriter, r *http.Request) {
	project, ok := h.authorizeProject(w, r)
	if !ok {
		return
	}
	if h.volumeManager == nil {
		writeError(w, http.StatusServiceUnavailable, "docker engine is not available")
		return
	}

	listCtx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	forceRefresh := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("refresh")), "true") ||
		strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("force")), "true")

	type volumeRefreshManager interface {
		ListProjectVolumesWithOptions(ctx context.Context, projectID uuid.UUID, forceRefresh bool) (*docker.ProjectVolumesResponse, error)
	}

	var (
		res *docker.ProjectVolumesResponse
		err error
	)
	if forceRefresh {
		if rm, ok := h.volumeManager.(volumeRefreshManager); ok {
			res, err = rm.ListProjectVolumesWithOptions(listCtx, project.ID, true)
		} else {
			res, err = h.volumeManager.ListProjectVolumes(listCtx, project.ID)
		}
	} else {
		res, err = h.volumeManager.ListProjectVolumes(listCtx, project.ID)
	}
	if err != nil {
		writeVolumeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// Backup handles POST /api/projects/{id}/volumes/{volumeName}/backup.
func (h *VolumeHandler) Backup(w http.ResponseWriter, r *http.Request) {
	project, ok := h.authorizeProject(w, r)
	if !ok {
		return
	}
	if h.volumeManager == nil {
		writeError(w, http.StatusServiceUnavailable, "docker engine is not available")
		return
	}

	volumeName, err := docker.ValidateVolumeName(chi.URLParam(r, "volumeName"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	filename := fmt.Sprintf(
		"%s-%s-%s.tar.gz",
		sanitizeVolumeFilenamePart(project.Name),
		sanitizeVolumeFilenamePart(volumeName),
		time.Now().UTC().Format("20060102-150405"),
	)

	lw := &lazyGzipResponseWriter{
		w:        w,
		filename: filename,
	}

	if err := h.volumeManager.BackupProjectVolume(r.Context(), project.ID, volumeName, lw); err != nil {
		if userID, ok := getUserIDFromContext(r); ok {
			h.recordAudit(r, userID, project, "volume.backup", volumeName, models.AuditStatusFailure, "Volume backup failed: "+err.Error(), map[string]any{"volume": volumeName})
		}
		if lw.wroteHeader {
			slog.Error("volume backup stream failed after headers sent", "project_id", project.ID, "volume", volumeName, "error", err)
			return
		}
		writeVolumeError(w, err)
		return
	}

	if !lw.wroteHeader {
		lw.writeHeaders()
	}
	if userID, ok := getUserIDFromContext(r); ok {
		h.recordAudit(r, userID, project, "volume.backup", volumeName, models.AuditStatusSuccess, "Downloaded volume backup archive", map[string]any{"volume": volumeName, "filename": filename})
	}
}

// Restore handles POST /api/projects/{id}/volumes/{volumeName}/restore.
func (h *VolumeHandler) Restore(w http.ResponseWriter, r *http.Request) {
	project, ok := h.authorizeProject(w, r)
	if !ok {
		return
	}
	if h.volumeManager == nil {
		writeError(w, http.StatusServiceUnavailable, "docker engine is not available")
		return
	}

	volumeName, err := docker.ValidateVolumeName(chi.URLParam(r, "volumeName"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "missing backup archive body")
		return
	}

	var reader io.Reader = r.Body
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.Contains(contentType, "multipart/form-data") {
		mpReader, mpErr := r.MultipartReader()
		if mpErr != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(mpErr, &maxBytesErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "volume backup upload exceeds maximum request size")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid multipart upload: "+mpErr.Error())
			return
		}

		var foundPart io.ReadCloser
		for {
			part, nextErr := mpReader.NextPart()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				var maxBytesErr *http.MaxBytesError
				if errors.As(nextErr, &maxBytesErr) {
					writeError(w, http.StatusRequestEntityTooLarge, "volume backup upload exceeds maximum request size")
					return
				}
				writeError(w, http.StatusBadRequest, "failed reading multipart form upload: "+nextErr.Error())
				return
			}
			fieldName := part.FormName()
			if fieldName == "file" || fieldName == "archive" || part.FileName() != "" {
				foundPart = part
				break
			}
			_ = part.Close()
		}
		if foundPart == nil {
			writeError(w, http.StatusBadRequest, "missing archive file field ('file' or 'archive') in multipart form")
			return
		}
		defer func() {
			_ = foundPart.Close()
		}()
		reader = foundPart
	}

	res, err := h.volumeManager.RestoreProjectVolume(r.Context(), project.ID, volumeName, reader)
	if err != nil {
		if userID, ok := getUserIDFromContext(r); ok {
			h.recordAudit(r, userID, project, "volume.restore", volumeName, models.AuditStatusFailure, "Volume restore failed: "+err.Error(), map[string]any{"volume": volumeName})
		}
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "volume backup upload exceeds maximum request size")
			return
		}
		writeVolumeError(w, err)
		return
	}

	if userID, ok := getUserIDFromContext(r); ok {
		h.recordAudit(r, userID, project, "volume.restore", volumeName, models.AuditStatusSuccess, "Restored volume from backup archive", map[string]any{
			"volume":         volumeName,
			"files_restored": res.FilesRestored,
			"bytes_restored": res.BytesRestored,
			"archive_bytes":  res.ArchiveBytes,
		})
	}

	writeJSON(w, http.StatusOK, res)
}

func writeVolumeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, docker.ErrInvalidVolumeName),
		errors.Is(err, docker.ErrInvalidVolumeArchive):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, docker.ErrVolumeNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, docker.ErrVolumeInUse):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, docker.ErrVolumeArchiveTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
	case errors.Is(err, docker.ErrDockerUnavailable):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

type lazyGzipResponseWriter struct {
	w           http.ResponseWriter
	filename    string
	wroteHeader bool
}

func (l *lazyGzipResponseWriter) writeHeaders() {
	if l.wroteHeader {
		return
	}
	l.w.Header().Set("Content-Type", "application/gzip")
	l.w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", l.filename))
	l.w.Header().Set("Cache-Control", "no-store")
	l.w.Header().Set("X-Content-Type-Options", "nosniff")
	l.w.WriteHeader(http.StatusOK)
	l.wroteHeader = true
}

func (l *lazyGzipResponseWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	l.writeHeaders()
	return l.w.Write(p)
}

func sanitizeVolumeFilenamePart(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "volume"
	}
	return out
}
