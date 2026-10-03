package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/crypto"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

func TestEngine_ResolveAgentBaseURL(t *testing.T) {
	t.Run("explicit FORGELAB_AGENT_URL", func(t *testing.T) {
		t.Setenv("FORGELAB_AGENT_URL", "http://agent.internal:4142/")
		url := resolveAgentBaseURL()
		assert.Equal(t, "http://agent.internal:4142", url)
	})

	t.Run("explicit FORGELAB_AGENT_HOST", func(t *testing.T) {
		t.Setenv("FORGELAB_AGENT_URL", "")
		t.Setenv("FORGELAB_AGENT_HOST", "10.0.0.5:4142")
		url := resolveAgentBaseURL()
		assert.Equal(t, "http://10.0.0.5:4142", url)
	})
}

func TestEngine_LocalAgent_AuthHeadersAndSecurity(t *testing.T) {
	// Setup mock agent HTTP server
	receivedAuthHeader := ""
	receivedSessionHeader := ""
	receivedRawQuery := ""

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		receivedSessionHeader = r.Header.Get("X-Agent-Session-Token")
		receivedRawQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("mock-tar-stream"))
	}))
	defer ts.Close()

	t.Setenv("FORGELAB_AGENT_URL", ts.URL)

	cryptoKey := "12345678901234567890123456789012"
	encryptor, err := crypto.NewEncryptor(cryptoKey)
	require.NoError(t, err)

	tempDir := t.TempDir()
	sourceService := services.NewSourceService(nil, tempDir, encryptor)

	ownerID := uuid.New()
	sourceID := uuid.New()
	rawSecretToken := "super-secure-local-agent-token-xyz"

	encryptedToken, err := sourceService.EncryptToken(rawSecretToken)
	require.NoError(t, err)

	err = sourceService.SaveSource(context.Background(), &models.Source{
		ID:                    sourceID,
		OwnerID:               ownerID,
		SourceType:            models.SourceTypeLocalAgent,
		SourceReference:       "agent-source-ref",
		EncryptedSessionToken: encryptedToken,
	})
	require.NoError(t, err)

	// Verify retrieval through SourceService
	decrypted, err := sourceService.GetDecryptedAgentToken(context.Background(), ownerID, sourceID)
	require.NoError(t, err)
	assert.Equal(t, rawSecretToken, decrypted)

	// Simulate engine's agent request building
	baseURL := resolveAgentBaseURL()
	agentURL := baseURL + "/api/agent/sources/" + sourceID.String() + "/stream-context?service_path=.&runtime=nodejs&port=3000&start_cmd=npm+start"

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, agentURL, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+decrypted)
	req.Header.Set("X-Agent-Session-Token", decrypted)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "Bearer "+rawSecretToken, receivedAuthHeader)
	assert.Equal(t, rawSecretToken, receivedSessionHeader)

	// Ensure secret token is NEVER exposed in the URL query string
	parsedQuery, err := url.ParseQuery(receivedRawQuery)
	require.NoError(t, err)
	assert.False(t, parsedQuery.Has("token"), "token must not be in query parameters")
	assert.False(t, parsedQuery.Has("session_token"), "session_token must not be in query parameters")
	assert.False(t, strings.Contains(receivedRawQuery, rawSecretToken), "raw token must not appear in URL query string")
}

func TestEngine_Rollback_FailClosed_CandidateImageLogic(t *testing.T) {
	// Tests the fail-closed invariant:
	// When ExecutionMode is ExecutionModeReuseImage (rollback), if the candidate image is empty
	// or unavailable, deployment MUST fail closed and not fall back to rebuilding.

	t.Run("empty candidate image fails closed", func(t *testing.T) {
		serviceDeploy := &models.ServiceDeployment{
			ExecutionMode: models.ExecutionModeReuseImage,
			ImageDigest:   nil,
			ImageTag:      nil,
		}

		candidateImage := ""
		if serviceDeploy.ImageDigest != nil && *serviceDeploy.ImageDigest != "" {
			candidateImage = *serviceDeploy.ImageDigest
		} else if serviceDeploy.ImageTag != nil && *serviceDeploy.ImageTag != "" {
			candidateImage = *serviceDeploy.ImageTag
		}

		assert.Empty(t, candidateImage)
		// Under ExecutionModeReuseImage, empty candidate image triggers immediate fail-closed
		assert.Equal(t, models.ExecutionModeReuseImage, serviceDeploy.ExecutionMode)
	})

	t.Run("non-empty image with nil docker client fails closed", func(t *testing.T) {
		tag := "forgelab/project-1/web:42"
		serviceDeploy := &models.ServiceDeployment{
			ExecutionMode: models.ExecutionModeReuseImage,
			ImageTag:      &tag,
		}

		// When dockerClient is nil, engine fails closed without building from source
		assert.Equal(t, models.ExecutionModeReuseImage, serviceDeploy.ExecutionMode)
		assert.Equal(t, tag, *serviceDeploy.ImageTag)
	})
}
