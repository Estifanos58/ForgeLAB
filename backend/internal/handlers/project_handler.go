package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/docker"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/queue"
	"github.com/forgelab/backend/internal/services"
)

// ProjectHandler handles project-related HTTP requests.
type ProjectHandler struct {
	projectService    *services.ProjectService
	deploymentService *services.DeploymentService
	dockerEngine      *docker.Engine
	deployQueue       *queue.DeploymentQueue
	auditService      *services.AuditService
	metricsService    *services.MetricsService
}

// NewProjectHandler creates a new ProjectHandler.
func NewProjectHandler(
	projectService *services.ProjectService,
	deploymentService *services.DeploymentService,
	dockerEngine *docker.Engine,
	deployQueue *queue.DeploymentQueue,
) *ProjectHandler {
	return &ProjectHandler{
		projectService:    projectService,
		deploymentService: deploymentService,
		dockerEngine:      dockerEngine,
		deployQueue:       deployQueue,
	}
}

// SetAuditService configures operational audit logging on ProjectHandler.
func (h *ProjectHandler) SetAuditService(auditSvc *services.AuditService) {
	h.auditService = auditSvc
}

// SetMetricsService configures historical runtime metrics querying on ProjectHandler.
func (h *ProjectHandler) SetMetricsService(metricsSvc *services.MetricsService) {
	h.metricsService = metricsSvc
}

func (h *ProjectHandler) recordAudit(r *http.Request, userID uuid.UUID, projectID *uuid.UUID, projectName, action, resourceType, resourceID, status, message string, metadata map[string]any) {
	if h.auditService == nil {
		return
	}
	h.auditService.RecordBestEffort(r.Context(), services.AuditRecordInput{
		ActorID:      userID,
		ActorLabel:   getUserEmailFromContext(r),
		IsGuest:      models.IsGuest(userID),
		ProjectID:    projectID,
		ProjectName:  projectName,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Status:       status,
		Message:      message,
		Metadata:     metadata,
		IPAddress:    getClientIP(r),
	})
}

// Create handles POST /api/projects
func (h *ProjectHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var input services.CreateProjectInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if strings.TrimSpace(input.Name) == "" {
		writeError(w, http.StatusBadRequest, "project name is required")
		return
	}

	project, err := h.projectService.CreateProject(r.Context(), userID, input)
	if err != nil {
		if errors.Is(err, services.ErrValidationFailed) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, services.ErrProjectSlugTaken) {
			writeError(w, http.StatusConflict, "a project with a similar name already exists")
			return
		}
		if errors.Is(err, services.ErrInvalidSource) {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if errors.Is(err, services.ErrGitHubNeedsReauth) {
			writeJSON(w, http.StatusForbidden, map[string]interface{}{
				"error":        "GitHub connection requires re-authorization for repository access. Please re-authorize GitHub.",
				"needs_reauth": true,
			})
			return
		}
		if errors.Is(err, services.ErrGitHubNotConnected) {
			writeError(w, http.StatusForbidden, "GitHub repository access has not been granted. Please authorize repository permissions.")
			return
		}
		slog.Error("failed to create project", "user_id", userID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create project")
		return
	}

	h.recordAudit(r, userID, &project.ID, project.Name, "project.create", "project", project.ID.String(), models.AuditStatusSuccess, "Created project "+project.Name, map[string]any{
		"source_type": project.SourceType,
		"branch":      project.Branch,
	})

	writeJSON(w, http.StatusCreated, project)
}

// List handles GET /api/projects
func (h *ProjectHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	projects, err := h.projectService.ListProjects(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list projects")
		return
	}

	writeJSON(w, http.StatusOK, projects)
}

// Get handles GET /api/projects/{id}
func (h *ProjectHandler) Get(w http.ResponseWriter, r *http.Request) {
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

	project, err := h.projectService.GetProject(r.Context(), projectID, userID)
	if err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if errors.Is(err, services.ErrProjectNotOwned) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get project")
		return
	}

	writeJSON(w, http.StatusOK, project)
}

// GetTopology handles GET /api/projects/{id}/topology
func (h *ProjectHandler) GetTopology(w http.ResponseWriter, r *http.Request) {
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

	// Verify project access
	_, err = h.projectService.GetProject(r.Context(), projectID, userID)
	if err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if errors.Is(err, services.ErrProjectNotOwned) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get project")
		return
	}

	if h.dockerEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "execution engine unavailable")
		return
	}

	topology, err := h.dockerEngine.GetProjectTopology(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build project topology: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, topology)
}

// GetRuntimeMetrics handles GET /api/projects/{id}/metrics
func (h *ProjectHandler) GetRuntimeMetrics(w http.ResponseWriter, r *http.Request) {
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

	// Verify project access
	_, err = h.projectService.GetProject(r.Context(), projectID, userID)
	if err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if errors.Is(err, services.ErrProjectNotOwned) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get project")
		return
	}

	if h.dockerEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "execution engine unavailable")
		return
	}

	forceRefresh := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("refresh")), "true") ||
		strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("force")), "true")

	metrics, err := h.dockerEngine.GetProjectRuntimeMetricsWithOptions(r.Context(), projectID, forceRefresh)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to collect runtime metrics: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, metrics)
}

// GetRuntimeMetricsHistory handles GET /api/projects/{id}/metrics/history
func (h *ProjectHandler) GetRuntimeMetricsHistory(w http.ResponseWriter, r *http.Request) {
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

	project, err := h.projectService.GetProject(r.Context(), projectID, userID)
	if err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if errors.Is(err, services.ErrProjectNotOwned) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get project")
		return
	}

	if h.metricsService == nil {
		writeError(w, http.StatusServiceUnavailable, "metrics history service unavailable")
		return
	}

	var serviceUUIDPtr *uuid.UUID
	if serviceIDStr := strings.TrimSpace(r.URL.Query().Get("service_id")); serviceIDStr != "" {
		serviceUUID, parseErr := uuid.Parse(serviceIDStr)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid service_id")
			return
		}
		belongs := false
		for _, svc := range project.Services {
			if svc != nil && svc.ID == serviceUUID {
				belongs = true
				break
			}
		}
		if !belongs && h.deploymentService != nil {
			var vErr error
			belongs, vErr = h.deploymentService.VerifyServiceBelongsToProject(r.Context(), serviceUUID, projectID)
			if vErr != nil {
				writeError(w, http.StatusInternalServerError, "failed to verify service access")
				return
			}
		}
		if !belongs {
			writeError(w, http.StatusBadRequest, "service does not belong to project")
			return
		}
		serviceUUIDPtr = &serviceUUID
	}

	now := time.Now().UTC()
	to := now
	hours := 1
	if hoursStr := strings.TrimSpace(r.URL.Query().Get("hours")); hoursStr != "" {
		parsedHours, parseErr := strconv.Atoi(hoursStr)
		if parseErr != nil || parsedHours < 1 || parsedHours > 720 {
			writeError(w, http.StatusBadRequest, "hours must be an integer between 1 and 720")
			return
		}
		hours = parsedHours
	}
	from := to.Add(-time.Duration(hours) * time.Hour)

	if fromStr := strings.TrimSpace(r.URL.Query().Get("from")); fromStr != "" {
		parsedFrom, parseErr := time.Parse(time.RFC3339, fromStr)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid from timestamp (expected RFC3339)")
			return
		}
		from = parsedFrom.UTC()
	}
	if toStr := strings.TrimSpace(r.URL.Query().Get("to")); toStr != "" {
		parsedTo, parseErr := time.Parse(time.RFC3339, toStr)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid to timestamp (expected RFC3339)")
			return
		}
		to = parsedTo.UTC()
	}

	limit := 240
	if limitStr := strings.TrimSpace(r.URL.Query().Get("limit")); limitStr != "" {
		parsedLimit, parseErr := strconv.Atoi(limitStr)
		if parseErr != nil || parsedLimit < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsedLimit
	}

	history, err := h.metricsService.QueryProjectHistory(r.Context(), projectID, services.MetricsHistoryQueryOptions{
		From:      from,
		To:        to,
		ServiceID: serviceUUIDPtr,
		Limit:     limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query runtime metrics history")
		return
	}

	writeJSON(w, http.StatusOK, history)
}

// Update handles PATCH /api/projects/{id}
func (h *ProjectHandler) Update(w http.ResponseWriter, r *http.Request) {
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

	var input services.UpdateProjectInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	project, err := h.projectService.UpdateProject(r.Context(), projectID, userID, input)
	if err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if errors.Is(err, services.ErrProjectNotOwned) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		if errors.Is(err, services.ErrValidationFailed) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, services.ErrProjectSlugTaken) {
			writeError(w, http.StatusConflict, "a project with a similar name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update project")
		return
	}

	h.recordAudit(r, userID, &project.ID, project.Name, "project.update", "project", project.ID.String(), models.AuditStatusSuccess, "Updated project "+project.Name, map[string]any{
		"name":        project.Name,
		"branch":      project.Branch,
		"auto_deploy": project.AutoDeploy,
	})

	writeJSON(w, http.StatusOK, project)
}

// Delete handles DELETE /api/projects/{id}
func (h *ProjectHandler) Delete(w http.ResponseWriter, r *http.Request) {
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

	projName := ""
	if h.projectService != nil {
		if existing, gErr := h.projectService.GetProject(r.Context(), projectID, userID); gErr == nil && existing != nil {
			projName = existing.Name
		}
	}

	var cleanup func(ctx context.Context, id uuid.UUID) error
	if h.dockerEngine != nil {
		cleanup = h.dockerEngine.CleanUpProjectResources
	}

	err = h.projectService.DeleteProject(r.Context(), projectID, userID, cleanup)
	if err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if errors.Is(err, services.ErrProjectNotOwned) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	msg := "Deleted project"
	if projName != "" {
		msg = "Deleted project " + projName
	}
	h.recordAudit(r, userID, &projectID, projName, "project.delete", "project", projectID.String(), models.AuditStatusSuccess, msg, nil)

	writeJSON(w, http.StatusOK, map[string]string{"message": "project deleted"})
}

// Deploy handles POST /api/projects/{id}/deployments
func (h *ProjectHandler) Deploy(w http.ResponseWriter, r *http.Request) {
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

	// Verify ownership
	project, err := h.projectService.GetProject(r.Context(), projectID, userID)
	if err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if errors.Is(err, services.ErrProjectNotOwned) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get project")
		return
	}

	deployment, err := h.deploymentService.CreateDeployment(r.Context(), project)
	if err != nil {
		if errors.Is(err, services.ErrActiveDeployment) {
			writeError(w, http.StatusConflict, "a deployment is already in progress")
			return
		}
		if errors.Is(err, services.ErrProjectDeleting) {
			writeError(w, http.StatusConflict, "project deletion is in progress")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create deployment")
		return
	}

	if h.deployQueue != nil {
		if err := h.deployQueue.EnqueueDeployment(r.Context(), deployment.ID); err != nil {
			reason := "failed to enqueue deployment job: " + err.Error()
			h.handleEnqueueFailure(r.Context(), projectID, deployment.ID, reason)
			writeError(w, http.StatusServiceUnavailable, "deployment queue unavailable: "+err.Error())
			return
		}
	} else if h.dockerEngine != nil {
		go func() {
			_ = h.dockerEngine.ExecuteDeployment(context.Background(), deployment.ID)
		}()
	} else {
		reason := "no functioning deployment queue or execution engine available"
		h.handleEnqueueFailure(r.Context(), projectID, deployment.ID, reason)
		writeError(w, http.StatusServiceUnavailable, reason)
		return
	}

	h.recordAudit(r, userID, &project.ID, project.Name, "deployment.create", "deployment", deployment.ID.String(), models.AuditStatusSuccess, fmt.Sprintf("Queued release deployment #%d", deployment.DeployNumber), map[string]any{
		"deploy_number": deployment.DeployNumber,
		"strategy":      deployment.DeploymentStrategy,
	})

	writeJSON(w, http.StatusCreated, deployment)
}

func (h *ProjectHandler) handleEnqueueFailure(ctx context.Context, projectID uuid.UUID, deploymentID uuid.UUID, failureReason string) {
	_ = h.deploymentService.UpdateDeploymentStatus(ctx, deploymentID, models.DeployStatusFailed, &failureReason)
	// A failed deployment attempt must never destroy the previous healthy release.
	proj, err := h.projectService.GetProjectByIDWithoutOwnership(ctx, projectID)
	if err == nil && proj != nil && proj.CurrentDeploymentID != nil && len(proj.Services) > 0 {
		_ = h.projectService.UpdateProjectStatus(ctx, projectID, services.CalculateProjectStatus(proj.Services))
	} else {
		_ = h.projectService.UpdateProjectStatus(ctx, projectID, models.ProjectStatusFailed)
	}
}

// Stop handles POST /api/projects/{id}/stop
func (h *ProjectHandler) Stop(w http.ResponseWriter, r *http.Request) {
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

	if h.dockerEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "docker engine is not available")
		return
	}

	if err := h.dockerEngine.StopApp(r.Context(), projectID, userID); err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if errors.Is(err, services.ErrProjectNotOwned) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	projName := ""
	if h.projectService != nil {
		if existing, gErr := h.projectService.GetProject(r.Context(), projectID, userID); gErr == nil && existing != nil {
			projName = existing.Name
		}
	}
	h.recordAudit(r, userID, &projectID, projName, "project.stop", "project", projectID.String(), models.AuditStatusSuccess, "Stopped project containers", nil)

	writeJSON(w, http.StatusOK, map[string]string{"message": "application stopped"})
}

// Start handles POST /api/projects/{id}/start
func (h *ProjectHandler) Start(w http.ResponseWriter, r *http.Request) {
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

	if h.dockerEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "docker engine is not available")
		return
	}

	if err := h.dockerEngine.StartApp(r.Context(), projectID, userID); err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if errors.Is(err, services.ErrProjectNotOwned) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	projName := ""
	if h.projectService != nil {
		if existing, gErr := h.projectService.GetProject(r.Context(), projectID, userID); gErr == nil && existing != nil {
			projName = existing.Name
		}
	}
	h.recordAudit(r, userID, &projectID, projName, "project.start", "project", projectID.String(), models.AuditStatusSuccess, "Started project containers", nil)

	writeJSON(w, http.StatusOK, map[string]string{"message": "application started"})
}

// Restart handles POST /api/projects/{id}/restart
func (h *ProjectHandler) Restart(w http.ResponseWriter, r *http.Request) {
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

	if h.dockerEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "docker engine is not available")
		return
	}

	if err := h.dockerEngine.RestartApp(r.Context(), projectID, userID); err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if errors.Is(err, services.ErrProjectNotOwned) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	projName := ""
	if h.projectService != nil {
		if existing, gErr := h.projectService.GetProject(r.Context(), projectID, userID); gErr == nil && existing != nil {
			projName = existing.Name
		}
	}
	h.recordAudit(r, userID, &projectID, projName, "project.restart", "project", projectID.String(), models.AuditStatusSuccess, "Restarted project containers", nil)

	writeJSON(w, http.StatusOK, map[string]string{"message": "application restarted"})
}

// Rollback handles POST /api/projects/{id}/rollback
func (h *ProjectHandler) Rollback(w http.ResponseWriter, r *http.Request) {
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

	project, err := h.projectService.GetProject(r.Context(), projectID, userID)
	if err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if errors.Is(err, services.ErrProjectNotOwned) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get project")
		return
	}

	if project.CurrentDeploymentID == nil {
		writeError(w, http.StatusBadRequest, "no active deployment to rollback from")
		return
	}

	newDeploy, err := h.deploymentService.RollbackDeployment(r.Context(), project)
	if err != nil {
		if errors.Is(err, services.ErrNoDeploymentToRollback) {
			writeError(w, http.StatusBadRequest, "no previous successful deployment available to rollback to")
			return
		}
		if errors.Is(err, services.ErrActiveDeployment) {
			writeError(w, http.StatusConflict, "a deployment is already in progress")
			return
		}
		if errors.Is(err, services.ErrProjectDeleting) {
			writeError(w, http.StatusConflict, "project deletion is in progress")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create rollback deployment: "+err.Error())
		return
	}

	if h.deployQueue != nil {
		if err := h.deployQueue.EnqueueDeployment(r.Context(), newDeploy.ID); err != nil {
			reason := "failed to enqueue rollback deployment: " + err.Error()
			h.handleEnqueueFailure(r.Context(), projectID, newDeploy.ID, reason)
			writeError(w, http.StatusServiceUnavailable, "deployment queue unavailable: "+err.Error())
			return
		}
	} else if h.dockerEngine != nil {
		go func() {
			_ = h.dockerEngine.ExecuteDeployment(context.Background(), newDeploy.ID)
		}()
	} else {
		reason := "no functioning deployment queue or execution engine available"
		h.handleEnqueueFailure(r.Context(), projectID, newDeploy.ID, reason)
		writeError(w, http.StatusServiceUnavailable, reason)
		return
	}

	h.recordAudit(r, userID, &project.ID, project.Name, "deployment.rollback", "deployment", newDeploy.ID.String(), models.AuditStatusSuccess, fmt.Sprintf("Queued rollback release deployment #%d", newDeploy.DeployNumber), map[string]any{
		"deploy_number": newDeploy.DeployNumber,
	})

	writeJSON(w, http.StatusCreated, newDeploy)
}

// ListDeployments handles GET /api/projects/{id}/deployments
func (h *ProjectHandler) ListDeployments(w http.ResponseWriter, r *http.Request) {
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

	// Verify ownership
	_, err = h.projectService.GetProject(r.Context(), projectID, userID)
	if err != nil {
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

	deployments, err := h.deploymentService.ListDeployments(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list deployments")
		return
	}

	writeJSON(w, http.StatusOK, deployments)
}

// GetDeployment handles GET /api/projects/{id}/deployments/{deploymentId}
func (h *ProjectHandler) GetDeployment(w http.ResponseWriter, r *http.Request) {
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

	deploymentID, err := uuid.Parse(chi.URLParam(r, "deploymentId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid deployment ID")
		return
	}

	// Verify ownership
	_, err = h.projectService.GetProject(r.Context(), projectID, userID)
	if err != nil {
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

	deployment, err := h.deploymentService.GetDeployment(r.Context(), deploymentID)
	if err != nil {
		if errors.Is(err, services.ErrDeploymentNotFound) {
			writeError(w, http.StatusNotFound, "deployment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get deployment")
		return
	}

	// Verify deployment belongs to the project
	if deployment.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}

	writeJSON(w, http.StatusOK, deployment)
}

// GetDeploymentLogs handles GET /api/projects/{id}/deployments/{deploymentId}/logs
func (h *ProjectHandler) GetDeploymentLogs(w http.ResponseWriter, r *http.Request) {
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

	deploymentID, err := uuid.Parse(chi.URLParam(r, "deploymentId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid deployment ID")
		return
	}

	// Verify ownership
	_, err = h.projectService.GetProject(r.Context(), projectID, userID)
	if err != nil {
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

	// Verify deployment belongs to project
	deployment, err := h.deploymentService.GetDeployment(r.Context(), deploymentID)
	if err != nil {
		if errors.Is(err, services.ErrDeploymentNotFound) {
			writeError(w, http.StatusNotFound, "deployment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get deployment")
		return
	}
	if deployment.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}

	var serviceUUIDPtr *uuid.UUID
	if serviceIDStr := r.URL.Query().Get("service_id"); serviceIDStr != "" {
		serviceUUID, parseErr := uuid.Parse(serviceIDStr)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid service_id")
			return
		}
		belongs, err := h.deploymentService.VerifyServiceBelongsToProject(r.Context(), serviceUUID, projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to verify service access")
			return
		}
		if !belongs {
			writeError(w, http.StatusBadRequest, "service does not belong to project")
			return
		}
		serviceUUIDPtr = &serviceUUID
	}

	limit := 1000
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if lim, parseErr := strconv.Atoi(limitStr); parseErr == nil && lim > 0 {
			limit = lim
		}
	}

	var afterID int64
	afterStr := r.URL.Query().Get("cursor")
	if afterStr == "" {
		afterStr = r.URL.Query().Get("after_id")
	}
	if afterStr == "" {
		afterStr = r.URL.Query().Get("after")
	}
	if afterStr != "" {
		if val, parseErr := strconv.ParseInt(afterStr, 10, 64); parseErr == nil && val > 0 {
			afterID = val
		}
	}

	var beforeID int64
	beforeStr := r.URL.Query().Get("before_id")
	if beforeStr == "" {
		beforeStr = r.URL.Query().Get("before")
	}
	if beforeStr != "" {
		if val, parseErr := strconv.ParseInt(beforeStr, 10, 64); parseErr == nil && val > 0 {
			beforeID = val
		}
	}

	phase := r.URL.Query().Get("phase")

	logs, err := h.deploymentService.GetDeploymentLogsWithOptions(r.Context(), deploymentID, services.LogQueryOptions{
		ServiceID: serviceUUIDPtr,
		Phase:     phase,
		AfterID:   afterID,
		BeforeID:  beforeID,
		Limit:     limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get deployment logs: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, logs)
}

// GetProjectLogs handles GET /api/projects/{id}/logs
func (h *ProjectHandler) GetProjectLogs(w http.ResponseWriter, r *http.Request) {
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

	// Verify ownership
	_, err = h.projectService.GetProject(r.Context(), projectID, userID)
	if err != nil {
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

	var serviceUUIDPtr *uuid.UUID
	if serviceIDStr := r.URL.Query().Get("service_id"); serviceIDStr != "" {
		serviceUUID, parseErr := uuid.Parse(serviceIDStr)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid service_id")
			return
		}
		belongs, err := h.deploymentService.VerifyServiceBelongsToProject(r.Context(), serviceUUID, projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to verify service access")
			return
		}
		if !belongs {
			writeError(w, http.StatusBadRequest, "service does not belong to project")
			return
		}
		serviceUUIDPtr = &serviceUUID
	}

	limit := 1000
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if lim, parseErr := strconv.Atoi(limitStr); parseErr == nil && lim > 0 {
			limit = lim
		}
	}

	var afterID int64
	afterStr := r.URL.Query().Get("cursor")
	if afterStr == "" {
		afterStr = r.URL.Query().Get("after_id")
	}
	if afterStr == "" {
		afterStr = r.URL.Query().Get("after")
	}
	if afterStr != "" {
		if val, parseErr := strconv.ParseInt(afterStr, 10, 64); parseErr == nil && val > 0 {
			afterID = val
		}
	}

	var beforeID int64
	beforeStr := r.URL.Query().Get("before_id")
	if beforeStr == "" {
		beforeStr = r.URL.Query().Get("before")
	}
	if beforeStr != "" {
		if val, parseErr := strconv.ParseInt(beforeStr, 10, 64); parseErr == nil && val > 0 {
			beforeID = val
		}
	}

	phase := r.URL.Query().Get("phase")

	logs, err := h.deploymentService.GetProjectLogs(r.Context(), projectID, services.LogQueryOptions{
		ServiceID: serviceUUIDPtr,
		Phase:     phase,
		AfterID:   afterID,
		BeforeID:  beforeID,
		Limit:     limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get project logs: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, logs)
}

// ListServiceDeployments handles GET /api/projects/{id}/deployments/{deploymentId}/services
func (h *ProjectHandler) ListServiceDeployments(w http.ResponseWriter, r *http.Request) {
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

	deploymentID, err := uuid.Parse(chi.URLParam(r, "deploymentId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid deployment ID")
		return
	}

	// Verify ownership
	_, err = h.projectService.GetProject(r.Context(), projectID, userID)
	if err != nil {
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

	// Verify deployment belongs to the project
	deployment, err := h.deploymentService.GetDeployment(r.Context(), deploymentID)
	if err != nil {
		if errors.Is(err, services.ErrDeploymentNotFound) {
			writeError(w, http.StatusNotFound, "deployment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get deployment")
		return
	}
	if deployment.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}

	serviceDeploys, err := h.deploymentService.ListServiceDeployments(r.Context(), deploymentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list service deployments")
		return
	}

	writeJSON(w, http.StatusOK, serviceDeploys)
}

// CancelDeployment handles POST /api/projects/{id}/deployments/{deploymentId}/cancel
func (h *ProjectHandler) CancelDeployment(w http.ResponseWriter, r *http.Request) {
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

	deploymentID, err := uuid.Parse(chi.URLParam(r, "deploymentId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid deployment ID")
		return
	}

	// Verify ownership
	project, err := h.projectService.GetProject(r.Context(), projectID, userID)
	if err != nil {
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

	// Cancel deployment in database (scoped atomically to project)
	err = h.deploymentService.CancelDeployment(r.Context(), projectID, deploymentID)
	if err != nil {
		if errors.Is(err, services.ErrDeploymentNotFound) {
			writeError(w, http.StatusNotFound, "deployment not found")
			return
		}
		if errors.Is(err, services.ErrCannotCancelRunningDeployment) {
			writeError(w, http.StatusConflict, "cannot cancel a deployment that is already running")
			return
		}
		if errors.Is(err, services.ErrDeploymentAlreadyTerminal) {
			writeError(w, http.StatusBadRequest, "deployment is already in a terminal state")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to cancel deployment: "+err.Error())
		return
	}

	// Cancel active container/build execution if currently in-flight
	if h.dockerEngine != nil {
		h.dockerEngine.CancelActiveDeployment(deploymentID)
	}

	h.recordAudit(r, userID, &projectID, project.Name, "deployment.cancel", "deployment", deploymentID.String(), models.AuditStatusSuccess, "Cancelled queued release deployment", nil)

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "deployment cancelled successfully",
	})
}
