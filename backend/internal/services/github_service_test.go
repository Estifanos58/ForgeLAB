package services_test

import (
	"context"
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

	authURL, err := svc.GetConnectURL(context.Background(), userID)
	require.NoError(t, err)
	require.NotEmpty(t, authURL)

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

	authURL, err := oauthSvc.GetGitHubAuthURL(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, authURL)

	parsedURL, err := url.Parse(authURL)
	require.NoError(t, err)

	q := parsedURL.Query()
	assert.Equal(t, "test-github-client-id", q.Get("client_id"))
	assert.Equal(t, "http://localhost:3000/api/auth/github/callback", q.Get("redirect_uri"))
	assert.Equal(t, "read:user user:email", q.Get("scope"))
	assert.NotEmpty(t, q.Get("state"))
}
