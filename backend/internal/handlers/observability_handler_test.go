package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dockerpkg "github.com/forgelab/backend/internal/docker"
	"github.com/forgelab/backend/internal/handlers"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

func TestEnvHandler_Set_EmitsRedactedAuditLog(t *testing.T) {
	ctx := context.Background()
	ownerID := uuid.New()
	projectID := uuid.New()
	envID := uuid.New()

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectGetterForTest(func(_ context.Context, pid, uid uuid.UUID) (*models.Project, error) {
		if pid != projectID {
			return nil, services.ErrProjectNotFound
		}
		if uid != ownerID {
			return nil, services.ErrProjectNotOwned
		}
		return &models.Project{ID: projectID, OwnerID: ownerID, Name: "secret-app"}, nil
	})

	secretSvc := services.NewSecretService(nil, nil, projSvc)
	secretSvc.SetEnvVarForTest(func(_ context.Context, pid, uid uuid.UUID, input services.SetEnvVarInput) (*services.EnvVarResponse, error) {
		return &services.EnvVarResponse{
			ID:        envID,
			ProjectID: pid,
			Key:       input.Key,
			Value:     "••••••••",
			IsSecret:  input.IsSecret,
			Scope:     "runtime",
		}, nil
	})

	auditSvc := services.NewAuditService(nil)
	h := handlers.NewEnvHandler(secretSvc, projSvc)
	h.SetAuditService(auditSvc)

	r := chi.NewRouter()
	r.Post("/api/projects/{id}/env", h.Set)

	body, _ := json.Marshal(map[string]any{
		"key":       "DB_PASSWORD",
		"value":     "top-secret-password-999",
		"is_secret": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID.String()+"/env", bytes.NewReader(body))
	req = req.WithContext(withTestUser(req.Context(), ownerID))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	logs, err := auditSvc.List(ctx, ownerID, services.AuditListFilter{ProjectID: &projectID})
	require.NoError(t, err)
	require.Len(t, logs, 1)
	assert.Equal(t, "env.set", logs[0].Action)
	assert.Equal(t, "env_var", logs[0].ResourceType)
	assert.Equal(t, "secret-app", logs[0].ProjectName)
	assert.Equal(t, "DB_PASSWORD", logs[0].Metadata["key"])
	assert.Equal(t, true, logs[0].Metadata["is_secret"])

	encoded, err := json.Marshal(logs[0])
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "top-secret-password-999")
}

func TestAuditHandler_AndPostDeleteRetention(t *testing.T) {
	ctx := context.Background()
	ownerID := uuid.New()
	otherID := uuid.New()
	projectID := uuid.New()

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectGetterForTest(func(_ context.Context, pid, uid uuid.UUID) (*models.Project, error) {
		if pid != projectID {
			return nil, services.ErrProjectNotFound
		}
		if uid != ownerID {
			return nil, services.ErrProjectNotOwned
		}
		return &models.Project{ID: projectID, OwnerID: ownerID, Name: "doomed-app"}, nil
	})

	auditSvc := services.NewAuditService(nil)
	_, err := auditSvc.Record(ctx, services.AuditRecordInput{
		ActorID:      ownerID,
		ProjectID:    &projectID,
		ProjectName:  "doomed-app",
		Action:       "domain.verify",
		ResourceType: "domain",
		ResourceID:   "app.example.com",
		Status:       models.AuditStatusSuccess,
		Message:      "Verified domain app.example.com",
	})
	require.NoError(t, err)

	projH := handlers.NewProjectHandler(projSvc, nil, nil, nil)
	projH.SetAuditService(auditSvc)
	auditH := handlers.NewAuditHandler(auditSvc, projSvc)

	r := chi.NewRouter()
	r.Delete("/api/projects/{id}", projH.Delete)
	r.Get("/api/audit-logs", auditH.ListUserLogs)
	r.Get("/api/projects/{id}/audit-logs", auditH.ListProjectLogs)

	// 1. Unauthorized & Forbidden checks
	req := httptest.NewRequest(http.MethodGet, "/api/audit-logs", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/audit-logs", nil)
	req = req.WithContext(withTestUser(req.Context(), otherID))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// 2. Owner lists project audit logs
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/audit-logs", nil)
	req = req.WithContext(withTestUser(req.Context(), ownerID))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var listResp struct {
		Logs []models.AuditLog `json:"logs"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&listResp))
	require.Len(t, listResp.Logs, 1)
	assert.Equal(t, "domain.verify", listResp.Logs[0].Action)

	// 3. Delete project and verify project.delete audit survives in /api/audit-logs
	time.Sleep(2 * time.Millisecond)
	req = httptest.NewRequest(http.MethodDelete, "/api/projects/"+projectID.String(), nil)
	req = req.WithContext(withTestUser(req.Context(), ownerID))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/api/audit-logs?action=project.delete", nil)
	req = req.WithContext(withTestUser(req.Context(), ownerID))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	listResp.Logs = nil
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&listResp))
	require.Len(t, listResp.Logs, 1)
	assert.Equal(t, "project.delete", listResp.Logs[0].Action)
	assert.Contains(t, listResp.Logs[0].Message, "doomed-app")
}

func TestDiagnosticsHandler_SystemAndProjectEndpoints(t *testing.T) {
	ownerID := uuid.New()
	otherID := uuid.New()
	projectID := uuid.New()

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectsListerForTest(func(ctx context.Context, oid uuid.UUID, limit, offset int) ([]*models.Project, error) {
		return []*models.Project{{ID: projectID, OwnerID: ownerID, Name: "diag-proj"}}, nil
	})
	projSvc.SetProjectGetterForTest(func(_ context.Context, pid, uid uuid.UUID) (*models.Project, error) {
		if pid != projectID {
			return nil, services.ErrProjectNotFound
		}
		if uid != ownerID {
			return nil, services.ErrProjectNotOwned
		}
		return &models.Project{ID: projectID, OwnerID: ownerID, Name: "diag-proj"}, nil
	})
	projSvc.SetProjectByIDForTest(func(_ context.Context, pid uuid.UUID) (*models.Project, error) {
		return &models.Project{ID: projectID, OwnerID: ownerID, Name: "diag-proj"}, nil
	})

	eng := dockerpkg.NewEngine(nil, projSvc, nil, nil, nil, nil, nil, nil, nil, t.TempDir())
	h := handlers.NewDiagnosticsHandler(projSvc, nil, eng)

	r := chi.NewRouter()
	r.Get("/api/diagnostics", h.GetSystemDiagnostics)
	r.Get("/api/projects/{id}/diagnostics", h.GetProjectDiagnostics)

	// 1. Typed nil engine safety -> 503
	var nilEng *dockerpkg.Engine
	nilH := handlers.NewDiagnosticsHandler(projSvc, nil, nilEng)
	nr := chi.NewRouter()
	nr.Get("/api/diagnostics", nilH.GetSystemDiagnostics)
	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics", nil)
	req = req.WithContext(withTestUser(req.Context(), ownerID))
	rec := httptest.NewRecorder()
	nr.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	// 2. Unauthorized & Forbidden
	req = httptest.NewRequest(http.MethodGet, "/api/diagnostics", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/diagnostics", nil)
	req = req.WithContext(withTestUser(req.Context(), otherID))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// 3. System diagnostics 200
	req = httptest.NewRequest(http.MethodGet, "/api/diagnostics", nil)
	req = req.WithContext(withTestUser(req.Context(), ownerID))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var sysRep dockerpkg.DiagnosticsReport
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&sysRep))
	assert.Equal(t, "system", sysRep.Scope)
	assert.Equal(t, "critical", sysRep.OverallStatus)

	// 4. Project diagnostics 200
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/diagnostics", nil)
	req = req.WithContext(withTestUser(req.Context(), ownerID))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var projRep dockerpkg.DiagnosticsReport
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&projRep))
	assert.Equal(t, "project", projRep.Scope)
	assert.Equal(t, "diag-proj", projRep.ProjectName)
}
