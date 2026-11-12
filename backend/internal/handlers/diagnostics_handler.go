package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/docker"
	"github.com/forgelab/backend/internal/services"
)

// DiagnosticsProvider exposes system and project operational diagnostics.
type DiagnosticsProvider interface {
	GetSystemDiagnostics(ctx context.Context, ownerID uuid.UUID, importSvc *services.RepositoryImportService) (*docker.DiagnosticsReport, error)
	GetProjectDiagnostics(ctx context.Context, projectID uuid.UUID, importSvc *services.RepositoryImportService) (*docker.DiagnosticsReport, error)
}

var _ DiagnosticsProvider = (*docker.Engine)(nil)

// DiagnosticsHandler handles system and project operational diagnostics endpoints.
type DiagnosticsHandler struct {
	projectService      *services.ProjectService
	importService       *services.RepositoryImportService
	diagnosticsProvider DiagnosticsProvider
}

// NewDiagnosticsHandler creates a DiagnosticsHandler.
func NewDiagnosticsHandler(
	projectService *services.ProjectService,
	importService *services.RepositoryImportService,
	provider DiagnosticsProvider,
) *DiagnosticsHandler {
	if eng, ok := provider.(*docker.Engine); ok && eng == nil {
		provider = nil
	}
	return &DiagnosticsHandler{
		projectService:      projectService,
		importService:       importService,
		diagnosticsProvider: provider,
	}
}

// GetSystemDiagnostics handles GET /api/diagnostics.
func (h *DiagnosticsHandler) GetSystemDiagnostics(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if h.diagnosticsProvider == nil {
		writeError(w, http.StatusServiceUnavailable, "execution engine unavailable")
		return
	}

	report, err := h.diagnosticsProvider.GetSystemDiagnostics(r.Context(), userID, h.importService)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to collect diagnostics: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, report)
}

// GetProjectDiagnostics handles GET /api/projects/{id}/diagnostics.
func (h *DiagnosticsHandler) GetProjectDiagnostics(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid project ID")
		return
	}

	if h.projectService == nil {
		writeError(w, http.StatusServiceUnavailable, "project service unavailable")
		return
	}

	if _, err := h.projectService.GetProject(r.Context(), projectID, userID); err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if errors.Is(err, services.ErrProjectNotOwned) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to verify project access")
		return
	}

	if h.diagnosticsProvider == nil {
		writeError(w, http.StatusServiceUnavailable, "execution engine unavailable")
		return
	}

	report, err := h.diagnosticsProvider.GetProjectDiagnostics(r.Context(), projectID, h.importService)
	if err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to collect project diagnostics: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, report)
}
