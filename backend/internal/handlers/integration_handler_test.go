package handlers_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/config"
	"github.com/forgelab/backend/internal/handlers"
	"github.com/forgelab/backend/internal/models"
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
	ghSvc.SetHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader(`{"message":"Not Found"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})
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

func generateTestRSAPEM(t *testing.T) string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der := x509.MarshalPKCS1PrivateKey(key)
	pemBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: der,
	}
	return string(pem.EncodeToMemory(pemBlock))
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestIntegrationHandler_GetAppInstallURL(t *testing.T) {
	appCfg := config.GitHubAppConfig{
		AppID:      "12345",
		Slug:       "forgelab-test-app",
		PrivateKey: generateTestRSAPEM(t),
	}
	appMgr := services.NewGitHubAppManager(appCfg, nil, nil)
	handler := handlers.NewIntegrationHandler(nil, appMgr, nil, "http://localhost:3000")

	t.Run("guest user rejected with 403 Forbidden", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/integrations/github/app/install", nil)
		req = req.WithContext(withTestUser(req.Context(), models.GuestUserID))
		rec := httptest.NewRecorder()

		handler.GetAppInstallURL(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("registered user generates cryptographically bound single-use state", func(t *testing.T) {
		regUser := uuid.New()
		req := httptest.NewRequest(http.MethodGet, "/api/integrations/github/app/install", nil)
		req = req.WithContext(withTestUser(req.Context(), regUser))
		rec := httptest.NewRecorder()

		handler.GetAppInstallURL(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp map[string]string
		err := json.NewDecoder(rec.Body).Decode(&resp)
		require.NoError(t, err)

		installURL := resp["url"]
		require.NotEmpty(t, installURL)
		parsedURL, err := url.Parse(installURL)
		require.NoError(t, err)

		assert.Equal(t, "github.com", parsedURL.Host)
		assert.Equal(t, "/apps/forgelab-test-app/installations/new", parsedURL.Path)

		stateParam := parsedURL.Query().Get("state")
		require.NotEmpty(t, stateParam)
		assert.Len(t, stateParam, 64, "state must be 32 cryptographically secure hex bytes")

		// Verify state is bound to registered user and consumable
		boundUser, err := appMgr.ValidateAndConsumeInstallState(context.Background(), stateParam)
		require.NoError(t, err)
		assert.Equal(t, regUser, boundUser)

		// Verify single-use: cannot be consumed again
		_, err = appMgr.ValidateAndConsumeInstallState(context.Background(), stateParam)
		assert.Error(t, err, "state must be single-use and invalid after consumption")
	})
}

func TestIntegrationHandler_GitHubAppCallback_ValidationAndSecurity(t *testing.T) {
	testUser := uuid.New()
	rsaPEM := generateTestRSAPEM(t)

	t.Run("missing state redirects with error", func(t *testing.T) {
		appCfg := config.GitHubAppConfig{AppID: "12345", Slug: "forgelab-app", PrivateKey: rsaPEM}
		appMgr := services.NewGitHubAppManager(appCfg, nil, nil)
		handler := handlers.NewIntegrationHandler(nil, appMgr, nil, "http://localhost:3000")

		req := httptest.NewRequest(http.MethodGet, "/api/integrations/github/app/callback?installation_id=9876", nil)
		rec := httptest.NewRecorder()

		handler.GitHubAppCallback(rec, req)
		assert.Equal(t, http.StatusFound, rec.Code)
		assert.Contains(t, rec.Header().Get("Location"), "error=missing_installation_state")
	})

	t.Run("invalid or expired state redirects with error", func(t *testing.T) {
		appCfg := config.GitHubAppConfig{AppID: "12345", Slug: "forgelab-app", PrivateKey: rsaPEM}
		appMgr := services.NewGitHubAppManager(appCfg, nil, nil)
		handler := handlers.NewIntegrationHandler(nil, appMgr, nil, "http://localhost:3000")

		req := httptest.NewRequest(http.MethodGet, "/api/integrations/github/app/callback?state=invalid-state-token&installation_id=9876", nil)
		rec := httptest.NewRecorder()

		handler.GitHubAppCallback(rec, req)
		assert.Equal(t, http.StatusFound, rec.Code)
		assert.Contains(t, rec.Header().Get("Location"), "error=invalid_or_expired_installation_state")
	})

	t.Run("missing installation_id redirects with error", func(t *testing.T) {
		appCfg := config.GitHubAppConfig{AppID: "12345", Slug: "forgelab-app", PrivateKey: rsaPEM}
		appMgr := services.NewGitHubAppManager(appCfg, nil, nil)
		state, err := appMgr.CreateInstallState(context.Background(), testUser)
		require.NoError(t, err)

		handler := handlers.NewIntegrationHandler(nil, appMgr, nil, "http://localhost:3000")
		req := httptest.NewRequest(http.MethodGet, "/api/integrations/github/app/callback?state="+state, nil)
		rec := httptest.NewRecorder()

		handler.GitHubAppCallback(rec, req)
		assert.Equal(t, http.StatusFound, rec.Code)
		assert.Contains(t, rec.Header().Get("Location"), "error=missing_installation_id")
	})

	t.Run("failed github api verification redirects with error", func(t *testing.T) {
		mockClient := &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(strings.NewReader(`{"message":"Not Found"}`)),
					Header:     make(http.Header),
				}, nil
			}),
		}

		appCfg := config.GitHubAppConfig{AppID: "12345", Slug: "forgelab-app", PrivateKey: rsaPEM}
		appMgr := services.NewGitHubAppManager(appCfg, nil, mockClient)
		state, err := appMgr.CreateInstallState(context.Background(), testUser)
		require.NoError(t, err)

		handler := handlers.NewIntegrationHandler(nil, appMgr, nil, "http://localhost:3000")
		req := httptest.NewRequest(http.MethodGet, "/api/integrations/github/app/callback?state="+state+"&installation_id=9876", nil)
		rec := httptest.NewRecorder()

		handler.GitHubAppCallback(rec, req)
		assert.Equal(t, http.StatusFound, rec.Code)
		assert.Contains(t, rec.Header().Get("Location"), "error=failed_to_verify_github_installation")
	})

	t.Run("successful verification binds installation and redirects", func(t *testing.T) {
		mockClient := &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				// GitHub App installations API response
				jsonResp := `{
					"id": 9876,
					"account": {
						"login": "testorg",
						"id": 5555,
						"type": "Organization",
						"avatar_url": "https://avatars.githubusercontent.com/u/5555"
					},
					"repository_selection": "all",
					"permissions": {"metadata": "read", "contents": "read"}
				}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(jsonResp)),
					Header:     make(http.Header),
				}, nil
			}),
		}

		appCfg := config.GitHubAppConfig{AppID: "12345", Slug: "forgelab-app", PrivateKey: rsaPEM}
		appMgr := services.NewGitHubAppManager(appCfg, nil, mockClient)
		state, err := appMgr.CreateInstallState(context.Background(), testUser)
		require.NoError(t, err)

		handler := handlers.NewIntegrationHandler(nil, appMgr, nil, "http://localhost:3000")
		req := httptest.NewRequest(http.MethodGet, "/api/integrations/github/app/callback?state="+state+"&installation_id=9876", nil)
		rec := httptest.NewRecorder()

		handler.GitHubAppCallback(rec, req)
		assert.Equal(t, http.StatusFound, rec.Code)
		assert.Equal(t, "http://localhost:3000/dashboard?github_app_installed=true", rec.Header().Get("Location"))

		// Verify replaying the callback with the same state is rejected
		recReplay := httptest.NewRecorder()
		handler.GitHubAppCallback(recReplay, req)
		assert.Equal(t, http.StatusFound, recReplay.Code)
		assert.Contains(t, recReplay.Header().Get("Location"), "error=invalid_or_expired_installation_state")
	})
}

func TestIntegrationHandler_ImportsLifecycle_CancelRetryAndConflicts(t *testing.T) {
	tempDir := t.TempDir()
	testUser := uuid.New()
	inFlightEntered := make(chan struct{})
	releaseRequest := make(chan struct{})

	ghSvc := services.NewGitHubService(nil, nil, config.OAuthConfig{}, nil)
	ghSvc.SetHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/repos/acme/demo") {
				select {
				case <-inFlightEntered:
				default:
					close(inFlightEntered)
				}
				select {
				case <-releaseRequest:
				case <-req.Context().Done():
					return nil, req.Context().Err()
				}
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(strings.NewReader(`{"message":"Not Found"}`)),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader(`{}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	impSvc := services.NewRepositoryImportService(nil, tempDir, ghSvc)
	t.Cleanup(func() {
		close(releaseRequest)
		_ = impSvc.Stop(context.Background())
	})
	handler := handlers.NewIntegrationHandler(ghSvc, nil, impSvc, "http://localhost:3000")

	r := chi.NewRouter()
	r.Get("/api/integrations/github/imports", handler.ListImports)
	r.Post("/api/integrations/github/imports", handler.StartImport)
	r.Get("/api/integrations/github/imports/{id}", handler.GetImport)
	r.Post("/api/integrations/github/imports/{id}/cancel", handler.CancelImport)
	r.Post("/api/integrations/github/imports/{id}/retry", handler.RetryImport)

	// Start import
	startReq := httptest.NewRequest(http.MethodPost, "/api/integrations/github/imports", strings.NewReader(`{"repository":"acme/demo","branch":"main"}`))
	startReq = startReq.WithContext(withTestUser(startReq.Context(), testUser))
	startRec := httptest.NewRecorder()
	r.ServeHTTP(startRec, startReq)
	require.Equal(t, http.StatusCreated, startRec.Code)

	var started models.RepositoryImport
	require.NoError(t, json.NewDecoder(startRec.Body).Decode(&started))

	select {
	case <-inFlightEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for background import call")
	}

	// Retry while active -> 409 Conflict
	retryActiveReq := httptest.NewRequest(http.MethodPost, "/api/integrations/github/imports/"+started.ID.String()+"/retry", nil)
	retryActiveReq = retryActiveReq.WithContext(withTestUser(retryActiveReq.Context(), testUser))
	retryActiveRec := httptest.NewRecorder()
	r.ServeHTTP(retryActiveRec, retryActiveReq)
	assert.Equal(t, http.StatusConflict, retryActiveRec.Code)

	// List imports -> includes started import
	listReq := httptest.NewRequest(http.MethodGet, "/api/integrations/github/imports", nil)
	listReq = listReq.WithContext(withTestUser(listReq.Context(), testUser))
	listRec := httptest.NewRecorder()
	r.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code)
	var listResp struct {
		Imports []models.RepositoryImport `json:"imports"`
	}
	require.NoError(t, json.NewDecoder(listRec.Body).Decode(&listResp))
	assert.Len(t, listResp.Imports, 1)

	// Cancel active import -> 200 OK
	cancelReq := httptest.NewRequest(http.MethodPost, "/api/integrations/github/imports/"+started.ID.String()+"/cancel", nil)
	cancelReq = cancelReq.WithContext(withTestUser(cancelReq.Context(), testUser))
	cancelRec := httptest.NewRecorder()
	r.ServeHTTP(cancelRec, cancelReq)
	require.Equal(t, http.StatusOK, cancelRec.Code)
	var cancelled models.RepositoryImport
	require.NoError(t, json.NewDecoder(cancelRec.Body).Decode(&cancelled))
	assert.Equal(t, models.ImportStatusCancelled, cancelled.Status)

	// Mark completed -> Cancel & Retry both return 409 Conflict
	require.NoError(t, impSvc.MarkCompleted(context.Background(), testUser, started.ID, uuid.New()))

	cancelCompReq := httptest.NewRequest(http.MethodPost, "/api/integrations/github/imports/"+started.ID.String()+"/cancel", nil)
	cancelCompReq = cancelCompReq.WithContext(withTestUser(cancelCompReq.Context(), testUser))
	cancelCompRec := httptest.NewRecorder()
	r.ServeHTTP(cancelCompRec, cancelCompReq)
	assert.Equal(t, http.StatusConflict, cancelCompRec.Code)

	retryCompReq := httptest.NewRequest(http.MethodPost, "/api/integrations/github/imports/"+started.ID.String()+"/retry", nil)
	retryCompReq = retryCompReq.WithContext(withTestUser(retryCompReq.Context(), testUser))
	retryCompRec := httptest.NewRecorder()
	r.ServeHTTP(retryCompRec, retryCompReq)
	assert.Equal(t, http.StatusConflict, retryCompRec.Code)
}
