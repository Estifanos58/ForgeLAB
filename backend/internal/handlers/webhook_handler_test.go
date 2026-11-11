package handlers_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/crypto"
	"github.com/forgelab/backend/internal/handlers"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

type fakeWebhookQueue struct {
	jobs []uuid.UUID
}

func (f *fakeWebhookQueue) EnqueueDeployment(_ context.Context, deploymentID uuid.UUID) error {
	f.jobs = append(f.jobs, deploymentID)
	return nil
}

func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestWebhookHandler_ConfigAndReceiveGitHub(t *testing.T) {
	ownerID := uuid.New()
	otherID := uuid.New()
	projectID := uuid.New()
	commit := "abcdef0123456789abcdef0123456789abcdef01"

	enc, err := crypto.NewEncryptor("dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q=")
	require.NoError(t, err)

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectGetterForTest(func(_ context.Context, pID, uID uuid.UUID) (*models.Project, error) {
		if pID != projectID {
			return nil, services.ErrProjectNotFound
		}
		if uID != ownerID {
			return nil, services.ErrProjectNotOwned
		}
		return &models.Project{
			ID:              projectID,
			OwnerID:         ownerID,
			Name:            "webhook-demo",
			SourceType:      models.SourceTypeGitHub,
			SourceReference: "org/webhook-demo",
			Branch:          "main",
		}, nil
	})

	depSvc := services.NewDeploymentService(nil)
	depSvc.SetCreateDeploymentWithCommitForTest(func(_ context.Context, p *models.Project, commitSHA *string) (*models.Deployment, error) {
		return &models.Deployment{
			ID:        uuid.New(),
			ProjectID: p.ID,
			CommitSHA: commitSHA,
		}, nil
	})

	q := &fakeWebhookQueue{}
	ws := services.NewWebhookService(nil, enc, projSvc, depSvc, q, "http://localhost:8080")
	ws.RegisterProjectForTest(&models.Project{
		ID:              projectID,
		OwnerID:         ownerID,
		Name:            "webhook-demo",
		SourceType:      models.SourceTypeGitHub,
		SourceReference: "org/webhook-demo",
		Branch:          "main",
	})

	wh := handlers.NewWebhookHandler(ws, projSvc)

	r := chi.NewRouter()
	r.Post("/api/webhooks/github", wh.ReceiveGitHub)
	r.Group(func(pr chi.Router) {
		pr.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if uidStr := req.Header.Get("X-Test-User"); uidStr != "" {
					uid, _ := uuid.Parse(uidStr)
					req = req.WithContext(withTestUser(req.Context(), uid))
				}
				next.ServeHTTP(w, req)
			})
		})
		pr.Get("/api/projects/{id}/webhook", wh.GetConfig)
		pr.Patch("/api/projects/{id}/webhook", wh.UpdateConfig)
	})

	// 1. Auth checks on webhook config endpoints
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/webhook", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/webhook", nil)
	req.Header.Set("X-Test-User", otherID.String())
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// 2. Enable push-to-deploy -> generates secret once
	req = httptest.NewRequest(http.MethodPatch, "/api/projects/"+projectID.String()+"/webhook", bytes.NewBufferString(`{"auto_deploy":true,"auto_deploy_branch":"main"}`))
	req.Header.Set("X-Test-User", ownerID.String())
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var cfg services.WebhookConfigView
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cfg))
	require.NotEmpty(t, cfg.WebhookSecret)
	secret := cfg.WebhookSecret

	// Subsequent GET must never expose plaintext secret
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/webhook", nil)
	req.Header.Set("X-Test-User", ownerID.String())
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	var getCfg services.WebhookConfigView
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &getCfg))
	assert.Empty(t, getCfg.WebhookSecret)
	assert.True(t, getCfg.HasWebhookSecret)

	// 3. Oversized payload >1 MiB rejected with 413
	huge := bytes.Repeat([]byte("a"), (1<<20)+64)
	req = httptest.NewRequest(http.MethodPost, "/api/webhooks/github?project_id="+projectID.String(), bytes.NewReader(huge))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "del-huge")
	req.Header.Set("X-Hub-Signature-256", "sha256=00")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	// 4. Forged signature rejected with 401
	payload := []byte(fmt.Sprintf(`{"ref":"refs/heads/main","after":"%s","repository":{"full_name":"org/webhook-demo"}}`, commit))
	req = httptest.NewRequest(http.MethodPost, "/api/webhooks/github?project_id="+projectID.String(), bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "del-forged")
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, q.jobs)

	// 5. Signed valid push triggers 202 Accepted and enqueues deployment
	req = httptest.NewRequest(http.MethodPost, "/api/webhooks/github?project_id="+projectID.String(), bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "del-valid-1")
	req.Header.Set("X-Hub-Signature-256", signBody(secret, payload))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Len(t, q.jobs, 1)

	var handleRes services.WebhookHandleResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &handleRes))
	assert.True(t, handleRes.Triggered)
	assert.Equal(t, commit, handleRes.CommitSHA)
	assert.NotNil(t, handleRes.DeploymentID)
}
