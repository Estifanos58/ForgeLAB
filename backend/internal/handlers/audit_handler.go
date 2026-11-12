package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/services"
)

// AuditHandler exposes user-scoped and project-scoped operational audit trail endpoints.
type AuditHandler struct {
	auditService   *services.AuditService
	projectService *services.ProjectService
}

// NewAuditHandler creates an AuditHandler.
func NewAuditHandler(auditService *services.AuditService, projectService *services.ProjectService) *AuditHandler {
	return &AuditHandler{
		auditService:   auditService,
		projectService: projectService,
	}
}

// ListUserLogs handles GET /api/audit-logs.
func (h *AuditHandler) ListUserLogs(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if h.auditService == nil {
		writeError(w, http.StatusServiceUnavailable, "audit service unavailable")
		return
	}

	filter, errMsg := parseAuditListFilter(r)
	if errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}

	if projectIDStr := strings.TrimSpace(r.URL.Query().Get("project_id")); projectIDStr != "" {
		pid, err := uuid.Parse(projectIDStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid project_id")
			return
		}
		filter.ProjectID = &pid
	}

	logs, err := h.auditService.List(r.Context(), userID, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list audit logs")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"logs":   logs,
		"limit":  filter.Limit,
		"offset": filter.Offset,
	})
}

// ListProjectLogs handles GET /api/projects/{id}/audit-logs.
func (h *AuditHandler) ListProjectLogs(w http.ResponseWriter, r *http.Request) {
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

	if h.auditService == nil {
		writeError(w, http.StatusServiceUnavailable, "audit service unavailable")
		return
	}

	filter, errMsg := parseAuditListFilter(r)
	if errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	filter.ProjectID = &projectID

	logs, err := h.auditService.List(r.Context(), userID, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list project audit logs")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"logs":   logs,
		"limit":  filter.Limit,
		"offset": filter.Offset,
	})
}

func parseAuditListFilter(r *http.Request) (services.AuditListFilter, string) {
	limit := 50
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 {
			return services.AuditListFilter{}, "limit must be a positive integer"
		}
		if parsed > 200 {
			parsed = 200
		}
		limit = parsed
	}

	offset := 0
	if rawOffset := strings.TrimSpace(r.URL.Query().Get("offset")); rawOffset != "" {
		parsed, err := strconv.Atoi(rawOffset)
		if err != nil || parsed < 0 {
			return services.AuditListFilter{}, "offset must be a non-negative integer"
		}
		offset = parsed
	}

	return services.AuditListFilter{
		Action:       strings.TrimSpace(r.URL.Query().Get("action")),
		ResourceType: strings.TrimSpace(r.URL.Query().Get("resource_type")),
		Limit:        limit,
		Offset:       offset,
	}, ""
}
