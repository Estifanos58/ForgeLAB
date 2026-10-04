package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

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
		if errors.Is(err, services.ErrGitHubNotConnected) {
			writeError(w, http.StatusForbidden, "GitHub repository access has not been granted. Please authorize repository permissions.")
			return
		}
		slog.Error("failed to create project", "user_id", userID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create project")
		return
	}

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
		if errors.Is(err, services.ErrProjectSlugTaken) {
			writeError(w, http.StatusConflict, "a project with a similar name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update project")
		return
	}

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

	var cleanup func(ctx context.Context, id uuid.UUID)
	if h.dockerEngine != nil {
		cleanup = h.dockerEngine.CleanUpProjectContainers
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
		writeError(w, http.StatusInternalServerError, "failed to delete project")
		return
	}

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
		writeError(w, http.StatusInternalServerError, "failed to create deployment")
		return
	}

	if h.deployQueue != nil {
		if err := h.deployQueue.EnqueueDeployment(r.Context(), deployment.ID); err != nil {
			reason := "failed to enqueue deployment job: " + err.Error()
			_ = h.deploymentService.UpdateDeploymentStatus(r.Context(), deployment.ID, models.DeployStatusFailed, &reason)
			_ = h.projectService.UpdateProjectStatus(r.Context(), projectID, models.ProjectStatusFailed)
			writeError(w, http.StatusInternalServerError, "failed to enqueue deployment job")
			return
		}
	}

	writeJSON(w, http.StatusCreated, deployment)
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
		writeError(w, http.StatusInternalServerError, "failed to create rollback deployment: "+err.Error())
		return
	}

	if h.deployQueue != nil {
		if err := h.deployQueue.EnqueueDeployment(r.Context(), newDeploy.ID); err != nil {
			reason := "failed to enqueue rollback deployment: " + err.Error()
			_ = h.deploymentService.UpdateDeploymentStatus(r.Context(), newDeploy.ID, models.DeployStatusFailed, &reason)
			_ = h.projectService.UpdateProjectStatus(r.Context(), projectID, models.ProjectStatusFailed)
			writeError(w, http.StatusInternalServerError, "failed to enqueue rollback deployment")
			return
		}
	}

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
		if serviceUUID, parseErr := uuid.Parse(serviceIDStr); parseErr == nil {
			serviceUUIDPtr = &serviceUUID
		}
	}

	limit := 1000
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if lim, parseErr := strconv.Atoi(limitStr); parseErr == nil && lim > 0 {
			limit = lim
		}
	}

	var logs []*models.DeploymentLog
	cursorStr := r.URL.Query().Get("cursor")
	if cursorStr == "" {
		cursorStr = r.URL.Query().Get("after_id")
	}

	if cursorStr != "" {
		if cursorVal, parseErr := strconv.ParseInt(cursorStr, 10, 64); parseErr == nil && cursorVal > 0 {
			logs, err = h.deploymentService.GetDeploymentLogsAfter(r.Context(), deploymentID, serviceUUIDPtr, cursorVal, limit)
		} else {
			logs, err = h.deploymentService.GetDeploymentLogs(r.Context(), deploymentID)
		}
	} else if serviceUUIDPtr != nil {
		logs, err = h.deploymentService.GetServiceLogs(r.Context(), deploymentID, *serviceUUIDPtr)
	} else {
		logs, err = h.deploymentService.GetDeploymentLogs(r.Context(), deploymentID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get deployment logs")
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

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "deployment cancelled successfully",
	})
}
