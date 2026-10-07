package services_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/config"
	"github.com/forgelab/backend/internal/services"
)

func TestGitHubService_GetConnectURL_UsesRepositoryCallback(t *testing.T) {
	ghCfg := config.OAuthConfig{
		ClientID:        "test-github-client-id",
		ClientSecret:    "test-github-client-secret",
		RedirectURL:     "http://localhost:3000/api/auth/github/callback",
		RepoRedirectURL: "http://localhost:3000/api/integrations/github/callback",
	}

	svc := services.NewGitHubService(nil, nil, ghCfg, nil)
	userID := uuid.New()

	authURL, nonce, err := svc.GetConnectURL(context.Background(), userID)
	require.NoError(t, err)
	require.NotEmpty(t, authURL)
	require.NotEmpty(t, nonce)

	parsedURL, err := url.Parse(authURL)
	require.NoError(t, err)

	assert.Equal(t, "github.com", parsedURL.Host)
	assert.Equal(t, "/login/oauth/authorize", parsedURL.Path)

	q := parsedURL.Query()
	assert.Equal(t, "test-github-client-id", q.Get("client_id"))
	assert.Equal(t, "http://localhost:3000/api/integrations/github/callback", q.Get("redirect_uri"))
	assert.Equal(t, "repo,read:user", q.Get("scope"))
	assert.NotEmpty(t, q.Get("state"))
}

func TestOAuthService_GetGitHubAuthURL_UsesSignInCallback(t *testing.T) {
	googleCfg := config.OAuthConfig{ClientID: "g-id"}
	ghCfg := config.OAuthConfig{
		ClientID:        "test-github-client-id",
		ClientSecret:    "test-github-client-secret",
		RedirectURL:     "http://localhost:3000/api/auth/github/callback",
		RepoRedirectURL: "http://localhost:3000/api/integrations/github/callback",
	}

	oauthSvc := services.NewOAuthService(googleCfg, ghCfg, nil)

	authURL, nonce, err := oauthSvc.GetGitHubAuthURL(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, authURL)
	require.NotEmpty(t, nonce)

	parsedURL, err := url.Parse(authURL)
	require.NoError(t, err)

	q := parsedURL.Query()
	assert.Equal(t, "test-github-client-id", q.Get("client_id"))
	assert.Equal(t, "http://localhost:3000/api/auth/github/callback", q.Get("redirect_uri"))
	assert.Equal(t, "read:user user:email", q.Get("scope"))
	assert.NotEmpty(t, q.Get("state"))
}

func TestGitHubService_ListRepositories_PrivateAndOrgReposWithPagination(t *testing.T) {
	var requestedPath string
	var requestedAuth string
	var requestedAccept string
	var requestedAPIVersion string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.String()
		requestedAuth = r.Header.Get("Authorization")
		requestedAccept = r.Header.Get("Accept")
		requestedAPIVersion = r.Header.Get("X-GitHub-Api-Version")

		// Return 2 repositories: 1 public, 1 private
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Link", `<https://api.github.com/user/repos?page=2&per_page=30>; rel="next"`)
		w.Header().Set("X-OAuth-Scopes", "repo, read:user")
		w.WriteHeader(http.StatusOK)

		_, _ = w.Write([]byte(`[
			{
				"id": 101,
				"name": "public-repo",
				"full_name": "testuser/public-repo",
				"private": false,
				"default_branch": "main",
				"description": "Public Repo",
				"html_url": "https://github.com/testuser/public-repo",
				"updated_at": "2026-03-01T00:00:00Z",
				"owner": {"login": "testuser"}
			},
			{
				"id": 102,
				"name": "private-org-repo",
				"full_name": "testorg/private-org-repo",
				"private": true,
				"default_branch": "develop",
				"description": "Confidential Repo",
				"html_url": "https://github.com/testorg/private-org-repo",
				"updated_at": "2026-03-02T00:00:00Z",
				"owner": {"login": "testorg"}
			}
		]`))
	}))
	defer mockServer.Close()

	svc := services.NewGitHubService(nil, nil, config.OAuthConfig{}, nil)
	userID := uuid.New()

	// Intercept HTTP requests to redirect api.github.com to mockServer
	customClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			u, _ := url.Parse(mockServer.URL + req.URL.RequestURI())
			req.URL = u
			return http.DefaultTransport.RoundTrip(req)
		}),
	}
	svc.SetHTTPClient(customClient)
	svc.SetTokenGetterForTest(func(ctx context.Context, uid uuid.UUID) (string, string, error) {
		return "gho_test_secret_token", "repo,read:user", nil
	})

	repos, hasMore, err := svc.ListRepositories(context.Background(), userID, 1, 30)
	require.NoError(t, err)
	assert.True(t, hasMore, "must recognize rel=next as having more pages")
	require.Len(t, repos, 2)

	// Verify query parameters enforce visibility=all and full affiliations
	assert.Contains(t, requestedPath, "visibility=all")
	assert.Contains(t, requestedPath, "affiliation=owner,collaborator,organization_member")
	assert.Contains(t, requestedPath, "page=1")
	assert.Contains(t, requestedPath, "per_page=30")

	// Verify headers
	assert.Equal(t, "Bearer gho_test_secret_token", requestedAuth)
	assert.Equal(t, "application/vnd.github+json", requestedAccept)
	assert.Equal(t, "2022-11-28", requestedAPIVersion)

	// Verify repository attributes
	assert.False(t, repos[0].Private)
	assert.Equal(t, "testuser/public-repo", repos[0].FullName)

	assert.True(t, repos[1].Private, "private repository must be retained and marked private")
	assert.Equal(t, "testorg/private-org-repo", repos[1].FullName)
	assert.Equal(t, "testorg", repos[1].Owner)
}

func TestGitHubService_ListRepositories_NeedsReauthWhenMissingRepoScope(t *testing.T) {
	svc := services.NewGitHubService(nil, nil, config.OAuthConfig{}, nil)
	userID := uuid.New()

	// Simulate old token issued with only read:user,user:email
	svc.SetTokenGetterForTest(func(ctx context.Context, uid uuid.UUID) (string, string, error) {
		return "gho_old_token", "read:user,user:email", nil
	})

	repos, hasMore, err := svc.ListRepositories(context.Background(), userID, 1, 30)
	require.Error(t, err)
	assert.False(t, hasMore)
	assert.Nil(t, repos)
	assert.ErrorIs(t, err, services.ErrGitHubNeedsReauth)
}

func TestGitHubService_GetStatus_DetectsNeedsReauth(t *testing.T) {
	svc := services.NewGitHubService(nil, nil, config.OAuthConfig{}, nil)
	userID := uuid.New()

	// Status with repo scope
	svc.SetStatusGetterForTest(func(ctx context.Context, uid uuid.UUID) (*services.GitHubStatus, error) {
		return &services.GitHubStatus{
			Connected:   true,
			Username:    "octocat",
			Scopes:      []string{"repo", "read:user"},
			NeedsReauth: false,
		}, nil
	})
	status, err := svc.GetStatus(context.Background(), userID)
	require.NoError(t, err)
	assert.True(t, status.Connected)
	assert.False(t, status.NeedsReauth)

	// Status lacking repo scope
	svc.SetStatusGetterForTest(func(ctx context.Context, uid uuid.UUID) (*services.GitHubStatus, error) {
		return &services.GitHubStatus{
			Connected:   true,
			Username:    "octocat",
			Scopes:      []string{"read:user"},
			NeedsReauth: true,
		}, nil
	})
	statusOld, err := svc.GetStatus(context.Background(), userID)
	require.NoError(t, err)
	assert.True(t, statusOld.Connected)
	assert.True(t, statusOld.NeedsReauth)
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
