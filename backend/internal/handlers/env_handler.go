package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/services"
)

type EnvHandler struct {
	secretService *services.SecretService
}

func NewEnvHandler(secretService *services.SecretService) *EnvHandler {
	return &EnvHandler{secretService: secretService}
}

// Set handles POST /api/projects/{id}/env
func (h *EnvHandler) Set(w http.ResponseWriter, r *http.Request) {
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

	var input services.SetEnvVarInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if input.Key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	envVar, err := h.secretService.SetEnvVar(r.Context(), projectID, userID, input)
	if err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if errors.Is(err, services.ErrProjectNotOwned) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to set environment variable")
		return
	}

	writeJSON(w, http.StatusOK, envVar)
}

// List handles GET /api/projects/{id}/env
func (h *EnvHandler) List(w http.ResponseWriter, r *http.Request) {
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

	envVars, err := h.secretService.ListEnvVars(r.Context(), projectID, userID)
	if err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if errors.Is(err, services.ErrProjectNotOwned) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to list environment variables")
		return
	}

	writeJSON(w, http.StatusOK, envVars)
}

// Delete handles DELETE /api/projects/{id}/env/{key}
func (h *EnvHandler) Delete(w http.ResponseWriter, r *http.Request) {
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

	key := chi.URLParam(r, "key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	err = h.secretService.DeleteEnvVar(r.Context(), projectID, userID, key)
	if err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if errors.Is(err, services.ErrProjectNotOwned) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		if errors.Is(err, services.ErrEnvVarNotFound) {
			writeError(w, http.StatusNotFound, "environment variable not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete environment variable")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "environment variable deleted"})
}
