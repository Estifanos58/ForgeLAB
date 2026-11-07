package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/handlers"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

func TestShareHandler_OwnershipEnforcement(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-share-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(nil, tempDir)
	projectSvc := services.NewProjectService(nil, nil, sourceSvc, nil)
	shareSvc := services.NewShareService(nil, projectSvc, nil, true, "http://localhost:8080", "http://localhost:8081")

	handler := handlers.NewShareHandler(shareSvc, projectSvc)
	router := chi.NewRouter()
	router.Get("/api/projects/{id}/share", handler.Get)
	router.Post("/api/projects/{id}/share", handler.Create)
	router.Delete("/api/projects/{id}/share", handler.Revoke)

	projectID := uuid.New()
	ownerID := uuid.New()
	differentUserID := uuid.New()

	t.Run("unauthenticated request returns 401 Unauthorized", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/share", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("invalid project UUID returns 400 Bad Request", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/projects/not-a-uuid/share", nil)
		req = req.WithContext(withTestUser(req.Context(), ownerID))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("GET /share returns 404 when project not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/share", nil)
		req = req.WithContext(withTestUser(req.Context(), ownerID))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("share endpoints return 403 Forbidden on ErrProjectNotOwned", func(t *testing.T) {
		// Mock project getter to simulate project owned by another user
		projectSvc.SetProjectGetterForTest(func(ctx context.Context, pid, uid uuid.UUID) (*models.Project, error) {
			if uid != ownerID {
				return nil, services.ErrProjectNotOwned
			}
			return nil, services.ErrProjectNotFound
		})

		// 1. GET /share
		reqGet := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/share", nil)
		reqGet = reqGet.WithContext(withTestUser(reqGet.Context(), differentUserID))
		recGet := httptest.NewRecorder()
		router.ServeHTTP(recGet, reqGet)
		assert.Equal(t, http.StatusForbidden, recGet.Code, "GET share must return 403 Forbidden for non-owner")

		// 2. POST /share
		reqCreate := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID.String()+"/share", nil)
		reqCreate = reqCreate.WithContext(withTestUser(reqCreate.Context(), differentUserID))
		recCreate := httptest.NewRecorder()
		router.ServeHTTP(recCreate, reqCreate)
		assert.Equal(t, http.StatusForbidden, recCreate.Code, "POST share must return 403 Forbidden for non-owner")

		// 3. DELETE /share
		reqRevoke := httptest.NewRequest(http.MethodDelete, "/api/projects/"+projectID.String()+"/share", nil)
		reqRevoke = reqRevoke.WithContext(withTestUser(reqRevoke.Context(), differentUserID))
		recRevoke := httptest.NewRecorder()
		router.ServeHTTP(recRevoke, reqRevoke)
		assert.Equal(t, http.StatusForbidden, recRevoke.Code, "DELETE share must return 403 Forbidden for non-owner")
	})
}
