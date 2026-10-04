package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/docker"
	"github.com/forgelab/backend/internal/queue"
	"github.com/forgelab/backend/internal/services"
)

type ServiceHandler struct {
	serviceService    *services.ServiceService
	projectService    *services.ProjectService
	deploymentService *services.DeploymentService
	dockerEngine      *docker.Engine
	deployQueue       *queue.DeploymentQueue
}

func NewServiceHandler(
	serviceService *services.ServiceService,
	projectService *services.ProjectService,
	deploymentService *services.DeploymentService,
	dockerEngine *docker.Engine,
	deployQueue *queue.DeploymentQueue,
) *ServiceHandler {
	return &ServiceHandler{
		serviceService:    serviceService,
		projectService:    projectService,
		deploymentService: deploymentService,
		dockerEngine:      dockerEngine,
		deployQueue:       deployQueue,
	}
}

// List handles GET /api/projects/{id}/services
func (h *ServiceHandler) List(w http.ResponseWriter, r *http.Request) {
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

	// Verify project ownership
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

	svcs, err := h.serviceService.ListServices(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list services")
		return
	}

	writeJSON(w, http.StatusOK, svcs)
}

// Stop handles POST /api/projects/{id}/services/{serviceId}/stop
func (h *ServiceHandler) Stop(w http.ResponseWriter, r *http.Request) {
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

	serviceID, err := uuid.Parse(chi.URLParam(r, "serviceId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service ID")
		return
	}

	if err := h.dockerEngine.StopService(r.Context(), projectID, serviceID, userID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to stop service: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// Start handles POST /api/projects/{id}/services/{serviceId}/start
func (h *ServiceHandler) Start(w http.ResponseWriter, r *http.Request) {
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

	serviceID, err := uuid.Parse(chi.URLParam(r, "serviceId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service ID")
		return
	}

	if err := h.dockerEngine.StartService(r.Context(), projectID, serviceID, userID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start service: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "running"})
}

// Restart handles POST /api/projects/{id}/services/{serviceId}/restart
func (h *ServiceHandler) Restart(w http.ResponseWriter, r *http.Request) {
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

	serviceID, err := uuid.Parse(chi.URLParam(r, "serviceId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service ID")
		return
	}

	if err := h.dockerEngine.RestartService(r.Context(), projectID, serviceID, userID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to restart service: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "running"})
}

// Deploy handles POST /api/projects/{id}/services/{serviceId}/deploy
func (h *ServiceHandler) Deploy(w http.ResponseWriter, r *http.Request) {
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

	serviceID, err := uuid.Parse(chi.URLParam(r, "serviceId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service ID")
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

	svc, err := h.serviceService.GetService(r.Context(), serviceID)
	if err != nil {
		if errors.Is(err, services.ErrServiceNotFound) {
			writeError(w, http.StatusNotFound, "service not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get service")
		return
	}

	if svc.ProjectID != projectID {
		writeError(w, http.StatusBadRequest, "service does not belong to project")
		return
	}

	deployment, err := h.deploymentService.CreateServiceDeployment(r.Context(), project, svc)
	if err != nil {
		if errors.Is(err, services.ErrActiveDeployment) {
			writeError(w, http.StatusConflict, "a deployment is already in progress for this service")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create service deployment: "+err.Error())
		return
	}

	if h.deployQueue != nil {
		if err := h.deployQueue.EnqueueServiceDeployment(r.Context(), deployment.ID); err != nil {
			_ = h.deploymentService.FailServiceDeployment(r.Context(), deployment.ID, "failed to enqueue deployment job: "+err.Error())
			writeError(w, http.StatusInternalServerError, "failed to enqueue deployment job")
			return
		}
	} else if h.dockerEngine != nil {
		go func() {
			_ = h.dockerEngine.ExecuteServiceDeployment(context.Background(), deployment.ID)
		}()
	}

	writeJSON(w, http.StatusCreated, deployment)
}

// Rollback handles POST /api/projects/{id}/services/{serviceId}/rollback
func (h *ServiceHandler) Rollback(w http.ResponseWriter, r *http.Request) {
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

	serviceID, err := uuid.Parse(chi.URLParam(r, "serviceId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service ID")
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

	svc, err := h.serviceService.GetService(r.Context(), serviceID)
	if err != nil {
		if errors.Is(err, services.ErrServiceNotFound) {
			writeError(w, http.StatusNotFound, "service not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get service")
		return
	}

	if svc.ProjectID != projectID {
		writeError(w, http.StatusBadRequest, "service does not belong to project")
		return
	}

	rollbackDeploy, err := h.deploymentService.RollbackServiceDeployment(r.Context(), project, svc)
	if err != nil {
		if errors.Is(err, services.ErrActiveDeployment) {
			writeError(w, http.StatusConflict, "a deployment is already in progress for this service")
			return
		}
		if errors.Is(err, services.ErrNoDeploymentToRollback) {
			writeError(w, http.StatusBadRequest, "no previous successful deployment found to roll back to")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to rollback service: "+err.Error())
		return
	}

	if h.deployQueue != nil {
		if err := h.deployQueue.EnqueueServiceDeployment(r.Context(), rollbackDeploy.ID); err != nil {
			_ = h.deploymentService.FailServiceDeployment(r.Context(), rollbackDeploy.ID, "failed to enqueue rollback job: "+err.Error())
			writeError(w, http.StatusInternalServerError, "failed to enqueue rollback job")
			return
		}
	} else if h.dockerEngine != nil {
		go func() {
			_ = h.dockerEngine.ExecuteServiceDeployment(context.Background(), rollbackDeploy.ID)
		}()
	}

	writeJSON(w, http.StatusCreated, rollbackDeploy)
}

// ListDeployments handles GET /api/projects/{id}/services/{serviceId}/deployments
func (h *ServiceHandler) ListDeployments(w http.ResponseWriter, r *http.Request) {
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

	serviceID, err := uuid.Parse(chi.URLParam(r, "serviceId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service ID")
		return
	}

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

	deployments, err := h.deploymentService.ListServiceDeploymentsByService(r.Context(), serviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list service deployments: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, deployments)
}

// GetDeployment handles GET /api/projects/{id}/services/{serviceId}/deployments/{deploymentId}
func (h *ServiceHandler) GetDeployment(w http.ResponseWriter, r *http.Request) {
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

	serviceID, err := uuid.Parse(chi.URLParam(r, "serviceId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service ID")
		return
	}

	deploymentID, err := uuid.Parse(chi.URLParam(r, "deploymentId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid deployment ID")
		return
	}

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

	sd, err := h.deploymentService.GetServiceDeployment(r.Context(), deploymentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "service deployment not found")
		return
	}

	if sd.ServiceID != serviceID {
		writeError(w, http.StatusBadRequest, "deployment does not belong to specified service")
		return
	}

	writeJSON(w, http.StatusOK, sd)
}

// GetLogs handles GET /api/projects/{id}/services/{serviceId}/deployments/{deploymentId}/logs
func (h *ServiceHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
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

	serviceID, err := uuid.Parse(chi.URLParam(r, "serviceId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service ID")
		return
	}

	deploymentID, err := uuid.Parse(chi.URLParam(r, "deploymentId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid deployment ID")
		return
	}

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

	sd, err := h.deploymentService.GetServiceDeployment(r.Context(), deploymentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "service deployment not found")
		return
	}

	if sd.ServiceID != serviceID {
		writeError(w, http.StatusBadRequest, "deployment does not belong to specified service")
		return
	}

	logs, err := h.deploymentService.GetServiceDeploymentLogs(r.Context(), deploymentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get service deployment logs: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, logs)
}

// UpdateResourcesInput represents the JSON body for configuring service resource limits.
type UpdateResourcesInput struct {
	CpuMillicores      *int `json:"cpu_millicores,omitempty"`
	MemoryMB           *int `json:"memory_mb,omitempty"`
	PidsLimit          *int `json:"pids_limit,omitempty"`
	EphemeralStorageMB *int `json:"ephemeral_storage_mb,omitempty"`
}

// UpdateResources handles PATCH /api/projects/{id}/services/{serviceId}/resources
func (h *ServiceHandler) UpdateResources(w http.ResponseWriter, r *http.Request) {
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

	serviceID, err := uuid.Parse(chi.URLParam(r, "serviceId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service ID")
		return
	}

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

	svc, err := h.serviceService.GetService(r.Context(), serviceID)
	if err != nil {
		if errors.Is(err, services.ErrServiceNotFound) {
			writeError(w, http.StatusNotFound, "service not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get service")
		return
	}

	if svc.ProjectID != projectID {
		writeError(w, http.StatusBadRequest, "service does not belong to specified project")
		return
	}

	var input UpdateResourcesInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	cfg := svc.ResourceConfig
	if input.CpuMillicores != nil {
		if *input.CpuMillicores < 100 || *input.CpuMillicores > 16000 {
			writeError(w, http.StatusBadRequest, "cpu_millicores must be between 100 and 16000 (0.1 to 16 cores)")
			return
		}
		cfg.CpuMillicores = *input.CpuMillicores
	}
	if input.MemoryMB != nil {
		if *input.MemoryMB < 64 || *input.MemoryMB > 32768 {
			writeError(w, http.StatusBadRequest, "memory_mb must be between 64 and 32768 (64MB to 32GB)")
			return
		}
		cfg.MemoryMB = *input.MemoryMB
	}
	if input.PidsLimit != nil {
		if *input.PidsLimit < 16 || *input.PidsLimit > 4096 {
			writeError(w, http.StatusBadRequest, "pids_limit must be between 16 and 4096")
			return
		}
		cfg.PidsLimit = *input.PidsLimit
	}
	if input.EphemeralStorageMB != nil && *input.EphemeralStorageMB > 0 {
		writeError(w, http.StatusBadRequest, "ephemeral_storage_mb is not supported on this host environment; remove or leave empty")
		return
	}

	if err := h.serviceService.UpdateServiceResources(r.Context(), serviceID, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update service resources: "+err.Error())
		return
	}

	svc.ResourceConfig = cfg
	writeJSON(w, http.StatusOK, svc)
}

// CancelDeployment handles POST /api/projects/{id}/services/{serviceId}/deployments/{deploymentId}/cancel
func (h *ServiceHandler) CancelDeployment(w http.ResponseWriter, r *http.Request) {
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

	serviceID, err := uuid.Parse(chi.URLParam(r, "serviceId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service ID")
		return
	}

	deploymentID, err := uuid.Parse(chi.URLParam(r, "deploymentId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid deployment ID")
		return
	}

	// Verify project ownership
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

	// Verify service belongs to project
	svc, err := h.serviceService.GetService(r.Context(), serviceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "service not found")
		return
	}
	if svc.ProjectID != projectID {
		writeError(w, http.StatusBadRequest, "service does not belong to project")
		return
	}

	// Cancel service deployment in database (scoped atomically to project and service)
	err = h.deploymentService.CancelServiceDeployment(r.Context(), projectID, serviceID, deploymentID)
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
		"message": "service deployment cancelled successfully",
	})
}
