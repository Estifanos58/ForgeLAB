package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/services"
)

const maxWebhookBodyBytes = 1 << 20 // 1 MiB

// WebhookHandler exposes project push-to-deploy settings and the public GitHub webhook receiver.
type WebhookHandler struct {
	webhookService *services.WebhookService
	projectService *services.ProjectService
}

// NewWebhookHandler creates a new WebhookHandler.
func NewWebhookHandler(webhookService *services.WebhookService, projectService *services.ProjectService) *WebhookHandler {
	return &WebhookHandler{
		webhookService: webhookService,
		projectService: projectService,
	}
}

func requestOriginBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	if r.Host != "" {
		return fmt.Sprintf("%s://%s", scheme, r.Host)
	}
	return ""
}

// GetConfig handles GET /api/projects/{id}/webhook.
func (h *WebhookHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
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

	if h.projectService == nil || h.webhookService == nil {
		writeError(w, http.StatusInternalServerError, "webhook service unavailable")
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

	cfg, err := h.webhookService.GetWebhookConfig(r.Context(), project, requestOriginBaseURL(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// UpdateConfig handles PATCH /api/projects/{id}/webhook.
func (h *WebhookHandler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
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

	if h.projectService == nil || h.webhookService == nil {
		writeError(w, http.StatusInternalServerError, "webhook service unavailable")
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

	var input services.UpdateWebhookConfigInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	updated, err := h.webhookService.UpdateWebhookConfig(r.Context(), project, input, requestOriginBaseURL(r))
	if err != nil {
		if errors.Is(err, services.ErrValidationFailed) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, updated)
}

// ReceiveGitHub handles POST /api/webhooks/github (unauthenticated; authenticated via HMAC-SHA256).
func (h *WebhookHandler) ReceiveGitHub(w http.ResponseWriter, r *http.Request) {
	if h.webhookService == nil {
		writeError(w, http.StatusServiceUnavailable, "webhook receiver unavailable")
		return
	}

	var projectIDHint *uuid.UUID
	if rawPID := strings.TrimSpace(r.URL.Query().Get("project_id")); rawPID != "" {
		parsed, err := uuid.Parse(rawPID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid project_id query parameter")
			return
		}
		projectIDHint = &parsed
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "webhook payload exceeds 1 MiB limit")
			return
		}
		writeError(w, http.StatusBadRequest, "failed to read webhook payload")
		return
	}

	eventType := r.Header.Get("X-GitHub-Event")
	deliveryID := r.Header.Get("X-GitHub-Delivery")
	signature := r.Header.Get("X-Hub-Signature-256")

	res, err := h.webhookService.HandleGitHubWebhook(r.Context(), projectIDHint, eventType, deliveryID, signature, payload)
	if err != nil {
		if errors.Is(err, services.ErrInvalidWebhookSignature) {
			writeError(w, http.StatusUnauthorized, "invalid or missing webhook signature")
			return
		}
		if errors.Is(err, services.ErrWebhookSecretUnavailable) {
			writeError(w, http.StatusUnauthorized, "webhook verification unavailable")
			return
		}
		if errors.Is(err, services.ErrValidationFailed) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, services.ErrActiveDeployment) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, services.ErrDeployQueueUnavailable) {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if res != nil && res.Triggered {
		writeJSON(w, http.StatusAccepted, res)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
