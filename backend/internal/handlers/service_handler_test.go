package handlers_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/database"
	"github.com/forgelab/backend/internal/handlers"
	"github.com/forgelab/backend/internal/services"
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
func getTestDatabaseURL() string {
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return url
	}
	return "postgres://forgelab:forgelab_dev_password@localhost:5432/forgelab?sslmode=disable"
}

func connectTestDB(t *testing.T) (*pgxpool.Pool, string) {
	dbURL := getTestDatabaseURL()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := database.Connect(ctx, dbURL)
	if err != nil {
		t.Skipf("Skipping integration test; cannot connect to database at %s: %v", dbURL, err)
		return nil, ""
	}
	return pool, dbURL
}

func TestServiceHandler_CrossProjectRejection(t *testing.T) {
	pool, _ := connectTestDB(t)
	if pool == nil {
		t.Skip("PostgreSQL not available")
		return
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name)
		VALUES ($1, $2, 'hash123', 'User Cross')
	`, userID, fmt.Sprintf("cross-user-%s@test.local", userID.String()[:8]))
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	}()

	// Project A
	projA := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO projects (id, owner_id, name, slug, status, created_at, updated_at)
		VALUES ($1, $2, 'Proj A', $3, 'running', NOW(), NOW())
	`, projA, userID, fmt.Sprintf("proj-a-%s", projA.String()[:8]))
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM projects WHERE id = $1", projA)
	}()

	// Project B
	projB := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO projects (id, owner_id, name, slug, status, created_at, updated_at)
		VALUES ($1, $2, 'Proj B', $3, 'running', NOW(), NOW())
	`, projB, userID, fmt.Sprintf("proj-b-%s", projB.String()[:8]))
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM projects WHERE id = $1", projB)
	}()

	// Service in Project B
	svcB := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO services (id, project_id, name, role, internal_port, status, created_at, updated_at)
		VALUES ($1, $2, 'svc-b', 'web', 3000, 'running', NOW(), NOW())
	`, svcB, projB)
	require.NoError(t, err)

	projectSvc := services.NewProjectService(pool, nil, nil, nil)
	serviceSvc := services.NewServiceService(pool)
	deploySvc := services.NewDeploymentService(pool)
	handler := handlers.NewServiceHandler(serviceSvc, projectSvc, deploySvc, nil, nil)

	makeRequest := func(method, path string, body io.Reader) *http.Request {
		req := httptest.NewRequest(method, path, body)
		req = req.WithContext(withTestUser(req.Context(), userID))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", projA.String())
		rctx.URLParams.Add("serviceId", svcB.String())
		return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	}

	t.Run("Stop rejected on cross-project service", func(t *testing.T) {
		req := makeRequest(http.MethodPost, "/api/projects/"+projA.String()+"/services/"+svcB.String()+"/stop", nil)
		rec := httptest.NewRecorder()
		handler.Stop(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "service does not belong to project")
	})

	t.Run("Start rejected on cross-project service", func(t *testing.T) {
		req := makeRequest(http.MethodPost, "/api/projects/"+projA.String()+"/services/"+svcB.String()+"/start", nil)
		rec := httptest.NewRecorder()
		handler.Start(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "service does not belong to project")
	})

	t.Run("Restart rejected on cross-project service", func(t *testing.T) {
		req := makeRequest(http.MethodPost, "/api/projects/"+projA.String()+"/services/"+svcB.String()+"/restart", nil)
		rec := httptest.NewRecorder()
		handler.Restart(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "service does not belong to project")
	})

	t.Run("ListDeployments rejected on cross-project service", func(t *testing.T) {
		req := makeRequest(http.MethodGet, "/api/projects/"+projA.String()+"/services/"+svcB.String()+"/deployments", nil)
		rec := httptest.NewRecorder()
		handler.ListDeployments(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "service does not belong to project")
	})

	t.Run("Deploy rejected on cross-project service", func(t *testing.T) {
		req := makeRequest(http.MethodPost, "/api/projects/"+projA.String()+"/services/"+svcB.String()+"/deploy", nil)
		rec := httptest.NewRecorder()
		handler.Deploy(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "service does not belong to project")
	})

	t.Run("Rollback rejected on cross-project service", func(t *testing.T) {
		req := makeRequest(http.MethodPost, "/api/projects/"+projA.String()+"/services/"+svcB.String()+"/rollback", nil)
		rec := httptest.NewRecorder()
		handler.Rollback(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "service does not belong to project")
	})
}
