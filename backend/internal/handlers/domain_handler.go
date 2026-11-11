package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/services"
)

// DomainHandler handles custom domain management and DNS/HTTPS verification endpoints.
type DomainHandler struct {
	domainService  *services.DomainService
	projectService *services.ProjectService
}

// NewDomainHandler creates a new DomainHandler.
func NewDomainHandler(domainService *services.DomainService, projectService *services.ProjectService) *DomainHandler {
	return &DomainHandler{
		domainService:  domainService,
		projectService: projectService,
	}
}

type addDomainRequest struct {
	Domain    string `json:"domain"`
	ServiceID string `json:"service_id,omitempty"`
}

func (h *DomainHandler) verifyProjectOwnership(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return uuid.Nil, false
	}

	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid project ID")
		return uuid.Nil, false
	}

	if h.projectService == nil {
		writeError(w, http.StatusInternalServerError, "project service unavailable")
		return uuid.Nil, false
	}

	if _, err := h.projectService.GetProject(r.Context(), projectID, userID); err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return uuid.Nil, false
		}
		if errors.Is(err, services.ErrProjectNotOwned) {
			writeError(w, http.StatusForbidden, "access denied")
			return uuid.Nil, false
		}
		writeError(w, http.StatusInternalServerError, "failed to verify project")
		return uuid.Nil, false
	}

	return projectID, true
}

// List returns all custom domains registered for a project.
func (h *DomainHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.verifyProjectOwnership(w, r)
	if !ok {
		return
	}
	if h.domainService == nil {
		writeError(w, http.StatusInternalServerError, "domain service unavailable")
		return
	}

	domains, err := h.domainService.ListDomains(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, domains)
}

// Add registers a custom domain for a project.
func (h *DomainHandler) Add(w http.ResponseWriter, r *http.Request) {
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

	if h.projectService == nil || h.domainService == nil {
		writeError(w, http.StatusInternalServerError, "domain service unavailable")
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
		writeError(w, http.StatusInternalServerError, "failed to verify project")
		return
	}

	var input addDomainRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var serviceIDPtr *uuid.UUID
	if input.ServiceID != "" {
		parsed, err := uuid.Parse(input.ServiceID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid service ID")
			return
		}
		serviceIDPtr = &parsed
	}

	created, err := h.domainService.AddDomain(r.Context(), project, input.Domain, serviceIDPtr)
	if err != nil {
		if errors.Is(err, services.ErrValidationFailed) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, services.ErrServiceNotFound) {
			writeError(w, http.StatusNotFound, "target service not found")
			return
		}
		if errors.Is(err, services.ErrDomainExists) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to add custom domain")
		return
	}

	writeJSON(w, http.StatusCreated, created)
}

// Verify performs DNS verification and Caddy HTTPS provisioning for a custom domain.
func (h *DomainHandler) Verify(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.verifyProjectOwnership(w, r)
	if !ok {
		return
	}

	domainID, err := uuid.Parse(chi.URLParam(r, "domainId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid domain ID")
		return
	}

	if h.domainService == nil {
		writeError(w, http.StatusInternalServerError, "domain service unavailable")
		return
	}

	dom, err := h.domainService.VerifyDomain(r.Context(), projectID, domainID)
	if err != nil {
		if errors.Is(err, services.ErrDomainNotFound) {
			writeError(w, http.StatusNotFound, "custom domain not found")
			return
		}
		if errors.Is(err, services.ErrDomainVerification) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error":  err.Error(),
				"domain": dom,
			})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":  err.Error(),
			"domain": dom,
		})
		return
	}

	writeJSON(w, http.StatusOK, dom)
}

// Remove deletes a custom domain from a project.
func (h *DomainHandler) Remove(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.verifyProjectOwnership(w, r)
	if !ok {
		return
	}

	domainID, err := uuid.Parse(chi.URLParam(r, "domainId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid domain ID")
		return
	}

	if h.domainService == nil {
		writeError(w, http.StatusInternalServerError, "domain service unavailable")
		return
	}

	if err := h.domainService.RemoveDomain(r.Context(), projectID, domainID); err != nil {
		if errors.Is(err, services.ErrDomainNotFound) {
			writeError(w, http.StatusNotFound, "custom domain not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to remove custom domain")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "custom domain removed"})
}
