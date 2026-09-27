package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/forgelab/backend/internal/config"
	"github.com/forgelab/backend/internal/handlers"
	"github.com/forgelab/backend/internal/services"
)

func TestIntegrationHandler_DetectRepository_GET_AuthRequired(t *testing.T) {
	ghSvc := services.NewGitHubService(nil, nil, config.OAuthConfig{}, nil)
	handler := handlers.NewIntegrationHandler(ghSvc, "http://localhost:3000")

	r := chi.NewRouter()
	r.Get("/api/integrations/github/repositories/{owner}/{repo}/detect", handler.DetectRepository)

	req := httptest.NewRequest(http.MethodGet, "/api/integrations/github/repositories/test-owner/test-repo/detect?branch=main", nil)
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestIntegrationHandler_DetectRepository_GET_NotConnected(t *testing.T) {
	ghSvc := services.NewGitHubService(nil, nil, config.OAuthConfig{}, nil)
	handler := handlers.NewIntegrationHandler(ghSvc, "http://localhost:3000")
	testUser := uuid.New()

	r := chi.NewRouter()
	r.Get("/api/integrations/github/repositories/{owner}/{repo}/detect", handler.DetectRepository)

	req := httptest.NewRequest(http.MethodGet, "/api/integrations/github/repositories/test-owner/test-repo/detect?branch=main&root_dir=frontend", nil)
	req = req.WithContext(withTestUser(req.Context(), testUser))
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)
	// Without connected GitHub account, returns 403 Forbidden (not 405 Method Not Allowed or 400)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
