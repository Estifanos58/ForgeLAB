package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/docker"
	"github.com/forgelab/backend/internal/services"
)

type ServiceHandler struct {
	serviceService *services.ServiceService
	projectService *services.ProjectService
	dockerEngine   *docker.Engine
}

func NewServiceHandler(
	serviceService *services.ServiceService,
	projectService *services.ProjectService,
	dockerEngine *docker.Engine,
) *ServiceHandler {
	return &ServiceHandler{
		serviceService: serviceService,
		projectService: projectService,
		dockerEngine:   dockerEngine,
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
