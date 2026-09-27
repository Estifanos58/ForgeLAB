package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/handlers"
	"github.com/forgelab/backend/internal/middleware"
	"github.com/forgelab/backend/internal/services"
)

func withTestUser(ctx context.Context, userID uuid.UUID) context.Context {
	return context.WithValue(ctx, middleware.UserIDKey, userID)
}

func TestProjectHandler_Create_ErrorMappings(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "forgelab-handler-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sourceSvc := services.NewSourceService(tempDir)
	projectSvc := services.NewProjectService(nil, nil, sourceSvc, nil)
	handler := handlers.NewProjectHandler(projectSvc, nil, nil, nil)

	testUser := uuid.New()

	t.Run("400 Bad Request on missing name", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"name":        "",
			"source_type": "local",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader(body))
		req = req.WithContext(withTestUser(req.Context(), testUser))
		rec := httptest.NewRecorder()

		handler.Create(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var errRes map[string]interface{}
		_ = json.NewDecoder(rec.Body).Decode(&errRes)
		assert.Contains(t, errRes["error"], "project name is required")
	})

	t.Run("422 Unprocessable Entity on invalid local source reference", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"name":             "Test App",
			"source_type":      "local",
			"source_reference": "not-a-valid-uuid",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader(body))
		req = req.WithContext(withTestUser(req.Context(), testUser))
		rec := httptest.NewRecorder()

		handler.Create(rec, req)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

		var errRes map[string]interface{}
		_ = json.NewDecoder(rec.Body).Decode(&errRes)
		assert.Contains(t, errRes["error"], "invalid source upload ID")
	})

	t.Run("422 Unprocessable Entity on missing local source", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"name":        "Test App",
			"source_type": "local",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader(body))
		req = req.WithContext(withTestUser(req.Context(), testUser))
		rec := httptest.NewRecorder()

		handler.Create(rec, req)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

		var errRes map[string]interface{}
		_ = json.NewDecoder(rec.Body).Decode(&errRes)
		assert.Contains(t, errRes["error"], "local source requires uploaded files or valid host repository path")
	})
}
