package handlers_test

import (
	"context"
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
	handler := handlers.NewIntegrationHandler(ghSvc, nil, nil, "http://localhost:3000")

	r := chi.NewRouter()
	r.Get("/api/integrations/github/repositories/{owner}/{repo}/detect", handler.DetectRepository)

	req := httptest.NewRequest(http.MethodGet, "/api/integrations/github/repositories/test-owner/test-repo/detect?branch=main", nil)
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestIntegrationHandler_DetectRepository_InaccessibleRepo_Returns404NotFound(t *testing.T) {
	ghSvc := services.NewGitHubService(nil, nil, config.OAuthConfig{}, nil)
	handler := handlers.NewIntegrationHandler(ghSvc, nil, nil, "http://localhost:3000")
	testUser := uuid.New()

	r := chi.NewRouter()
	r.Get("/api/integrations/github/repositories/{owner}/{repo}/detect", handler.DetectRepository)

	req := httptest.NewRequest(http.MethodGet, "/api/integrations/github/repositories/test-owner/nonexistent-repo/detect?branch=main&root_dir=frontend", nil)
	req = req.WithContext(withTestUser(req.Context(), testUser))
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)
	// Inaccessible or non-existent repositories return 404 Not Found without silent fallback
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestIntegrationHandler_ListRepositories_NotConnected_Returns403Forbidden(t *testing.T) {
	ghSvc := services.NewGitHubService(nil, nil, config.OAuthConfig{}, nil)
	ghSvc.SetTokenGetterForTest(func(ctx context.Context, uid uuid.UUID) (string, string, error) {
		return "", "", services.ErrGitHubNotConnected
	})
	handler := handlers.NewIntegrationHandler(ghSvc, nil, nil, "http://localhost:3000")
	testUser := uuid.New()

	r := chi.NewRouter()
	r.Get("/api/integrations/github/repositories", handler.ListRepositories)

	req := httptest.NewRequest(http.MethodGet, "/api/integrations/github/repositories", nil)
	req = req.WithContext(withTestUser(req.Context(), testUser))
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
