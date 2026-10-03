package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/forgelab/backend/internal/handlers"
)

func TestServiceHandler_CancelDeployment_Validation(t *testing.T) {
	handler := handlers.NewServiceHandler(nil, nil, nil, nil, nil)

	t.Run("401 Unauthorized without auth", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/123/services/456/deployments/789/cancel", nil)
		rec := httptest.NewRecorder()
		handler.CancelDeployment(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("400 Bad Request on invalid project ID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/invalid-uuid/services/456/deployments/789/cancel", nil)
		req = req.WithContext(withTestUser(req.Context(), uuid.New()))
		rec := httptest.NewRecorder()
		handler.CancelDeployment(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("400 Bad Request on invalid service ID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+uuid.New().String()+"/services/invalid-service/deployments/789/cancel", nil)
		req = req.WithContext(withTestUser(req.Context(), uuid.New()))
		rec := httptest.NewRecorder()
		handler.CancelDeployment(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("400 Bad Request on invalid deployment ID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+uuid.New().String()+"/services/"+uuid.New().String()+"/deployments/invalid-deployment/cancel", nil)
		req = req.WithContext(withTestUser(req.Context(), uuid.New()))
		rec := httptest.NewRecorder()
		handler.CancelDeployment(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}
