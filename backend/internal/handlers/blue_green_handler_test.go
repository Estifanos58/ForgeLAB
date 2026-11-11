package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/handlers"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

func TestServiceHandler_UpdateDeploymentMode(t *testing.T) {
	ownerID := uuid.New()
	otherUserID := uuid.New()
	projectID := uuid.New()
	otherProjectID := uuid.New()
	serviceID := uuid.New()

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectGetterForTest(func(ctx context.Context, pID, reqUserID uuid.UUID) (*models.Project, error) {
		if pID != projectID {
			return nil, services.ErrProjectNotFound
		}
		if reqUserID != ownerID {
			return nil, services.ErrProjectNotOwned
		}
		return &models.Project{
			ID:             projectID,
			OwnerID:        ownerID,
			Name:           "bg-handler-project",
			DeploymentMode: models.DeploymentModeStandard,
		}, nil
	})

	var persistedMode string
	svcSvc := services.NewServiceService(nil)
	svcSvc.SetGetServiceForTest(func(ctx context.Context, sID uuid.UUID) (*models.Service, error) {
		if sID != serviceID {
			return nil, services.ErrServiceNotFound
		}
		return &models.Service{
			ID:             serviceID,
			ProjectID:      projectID,
			Name:           "web",
			DeploymentMode: models.DeploymentModeStandard,
		}, nil
	})
	svcSvc.SetUpdateServiceDeploymentModeForTest(func(ctx context.Context, sID uuid.UUID, mode string) error {
		persistedMode = mode
		return nil
	})

	handler := handlers.NewServiceHandler(svcSvc, projSvc, nil, nil, nil)

	newRouter := func(userID *uuid.UUID) *chi.Mux {
		r := chi.NewRouter()
		r.Patch("/api/projects/{id}/services/{serviceId}/deployment-mode", func(w http.ResponseWriter, req *http.Request) {
			if userID != nil {
				req = req.WithContext(withTestUser(req.Context(), *userID))
			}
			handler.UpdateDeploymentMode(w, req)
		})
		return r
	}

	t.Run("401 unauthorized without user", func(t *testing.T) {
		r := newRouter(nil)
		req := httptest.NewRequest(http.MethodPatch, "/api/projects/"+projectID.String()+"/services/"+serviceID.String()+"/deployment-mode", bytes.NewBufferString(`{"deployment_mode":"blue_green"}`))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("403 forbidden for non-owner", func(t *testing.T) {
		r := newRouter(&otherUserID)
		req := httptest.NewRequest(http.MethodPatch, "/api/projects/"+projectID.String()+"/services/"+serviceID.String()+"/deployment-mode", bytes.NewBufferString(`{"deployment_mode":"blue_green"}`))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("404 when service not found", func(t *testing.T) {
		r := newRouter(&ownerID)
		req := httptest.NewRequest(http.MethodPatch, "/api/projects/"+projectID.String()+"/services/"+uuid.New().String()+"/deployment-mode", bytes.NewBufferString(`{"deployment_mode":"blue_green"}`))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("404 when project not found", func(t *testing.T) {
		r := newRouter(&ownerID)
		req := httptest.NewRequest(http.MethodPatch, "/api/projects/"+otherProjectID.String()+"/services/"+serviceID.String()+"/deployment-mode", bytes.NewBufferString(`{"deployment_mode":"blue_green"}`))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("400 invalid deployment mode", func(t *testing.T) {
		r := newRouter(&ownerID)
		req := httptest.NewRequest(http.MethodPatch, "/api/projects/"+projectID.String()+"/services/"+serviceID.String()+"/deployment-mode", bytes.NewBufferString(`{"deployment_mode":"canary"}`))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("200 updates service deployment mode to blue_green", func(t *testing.T) {
		r := newRouter(&ownerID)
		req := httptest.NewRequest(http.MethodPatch, "/api/projects/"+projectID.String()+"/services/"+serviceID.String()+"/deployment-mode", bytes.NewBufferString(`{"deployment_mode":"blue_green"}`))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		var updated models.Service
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &updated))
		assert.Equal(t, models.DeploymentModeBlueGreen, updated.DeploymentMode)
		assert.Equal(t, models.DeploymentModeBlueGreen, persistedMode)
	})
}
