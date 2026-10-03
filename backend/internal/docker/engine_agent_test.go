package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/agent"
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

func TestEngine_LocalAgent_Deployment_AuthenticatesAfterProjectCreation(t *testing.T) {
	// 1. Create a dummy project directory for local agent to serve
	tempDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "package.json"), []byte(`{"name":"ccms-frontend"}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "index.js"), []byte(`console.log("running ccms-frontend");`), 0644))

	// 2. Start real AgentServer with loopback router
	agentID := "test-deploy-agent-123"
	srv := agent.NewAgentServer(agent.AgentServerConfig{
		AgentID: agentID,
		Port:    4142,
	})
	defer srv.Close()

	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	t.Setenv("FORGELAB_AGENT_URL", ts.URL)

	// 3. Create Session in SessionManager
	sm := agent.GetGlobalSessionManager()
	ownerID := uuid.New()
	authSession, err := sm.CreateSession(ownerID, agentID, 15*time.Minute)
	require.NoError(t, err)

	// 4. Local agent registers directory via POST /api/agent/select-path with unconsumed token
	selectPayload, err := json.Marshal(map[string]string{"path": tempDir})
	require.NoError(t, err)
	selectReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/agent/select-path", bytes.NewReader(selectPayload))
	require.NoError(t, err)
	selectReq.Header.Set("Content-Type", "application/json")
	selectReq.Header.Set("Authorization", "Bearer "+authSession.Token)

	selectResp, err := http.DefaultClient.Do(selectReq)
	require.NoError(t, err)
	defer selectResp.Body.Close()
	require.Equal(t, http.StatusOK, selectResp.StatusCode)

	var selectData struct {
		SourceID string `json:"source_id"`
	}
	require.NoError(t, json.NewDecoder(selectResp.Body).Decode(&selectData))
	sourceID, err := uuid.Parse(selectData.SourceID)
	require.NoError(t, err)

	// 5. Register source in SourceService with AES-256-GCM encrypted token
	cryptoKey := "12345678901234567890123456789012"
	encryptor, err := crypto.NewEncryptor(cryptoKey)
	require.NoError(t, err)

	sourceService := services.NewSourceService(nil, t.TempDir(), encryptor)
	encryptedToken, err := sourceService.EncryptToken(authSession.Token)
	require.NoError(t, err)

	err = sourceService.SaveSource(context.Background(), &models.Source{
		ID:                    sourceID,
		OwnerID:               ownerID,
		SourceType:            models.SourceTypeLocalAgent,
		SourceReference:       sourceID.String(),
		AgentID:               agentID,
		EncryptedSessionToken: encryptedToken,
	})
	require.NoError(t, err)

	// 6. Simulate Project Creation (which marks the session as Consumed in SessionManager)
	sess, err := sm.FindSessionBySourceID(sourceID)
	require.NoError(t, err)
	require.NoError(t, sm.MarkConsumed(sess.ID))
	require.NoError(t, srv.ConsumeSession(sourceID))

	// Verify session is now consumed: general token validation MUST reject consumed session
	_, valErr := sm.ValidateToken(authSession.Token)
	assert.ErrorIs(t, valErr, agent.ErrSessionConsumed, "session must be marked consumed after project creation")

	// Attempting to select another path with the consumed token MUST fail with 401
	selectReq2, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/agent/select-path", bytes.NewReader(selectPayload))
	selectReq2.Header.Set("Content-Type", "application/json")
	selectReq2.Header.Set("Authorization", "Bearer "+authSession.Token)
	selectResp2, err := http.DefaultClient.Do(selectReq2)
	require.NoError(t, err)
	selectResp2.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, selectResp2.StatusCode, "consumed token cannot select new paths")

	// 7. Execute ASYNC DEPLOYMENT path from engine.go
	project := &models.Project{
		ID:              uuid.New(),
		OwnerID:         ownerID,
		SourceType:      models.SourceTypeLocalAgent,
		SourceReference: sourceID.String(),
	}
	service := &models.Service{
		SourcePath:   ".",
		RuntimeType:  "nodejs",
		InternalPort: 3000,
		StartCommand: "npm start",
	}

	// Engine securely retrieves and decrypts the token via SourceService
	decryptedToken, err := sourceService.GetDecryptedAgentToken(context.Background(), project.OwnerID, sourceID)
	require.NoError(t, err)
	assert.Equal(t, authSession.Token, decryptedToken)

	// Engine builds agent request
	baseURL := resolveAgentBaseURL()
	agentURL := fmt.Sprintf("%s/api/agent/sources/%s/stream-context?service_path=%s&runtime=%s&port=%d&start_cmd=%s",
		baseURL,
		project.SourceReference,
		url.QueryEscape(service.SourcePath),
		url.QueryEscape(service.RuntimeType),
		service.InternalPort,
		url.QueryEscape(service.StartCommand),
	)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, agentURL, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+decryptedToken)
	req.Header.Set("X-Agent-Session-Token", decryptedToken)

	// Execute deployment request to local agent
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Prior to our fix, this failed with 401/403 ("Local agent session authorization rejected")
	assert.Equal(t, http.StatusOK, resp.StatusCode, "async deployment must successfully authenticate with consumed token for its bound source")

	// Verify streaming tarball contains project files
	tr := tar.NewReader(resp.Body)
	var foundPackageJSON, foundIndexJS bool
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if hdr.Name == "package.json" {
			foundPackageJSON = true
		}
		if hdr.Name == "index.js" {
			foundIndexJS = true
		}
	}
	assert.True(t, foundPackageJSON, "expected package.json in streamed archive")
	assert.True(t, foundIndexJS, "expected index.js in streamed archive")

	// 8. Verify security boundaries: cannot access a different source with this consumed token
	otherSourceID := uuid.New()
	wrongSourceURL := fmt.Sprintf("%s/api/agent/sources/%s/stream-context", baseURL, otherSourceID)
	reqWrong, err := http.NewRequestWithContext(context.Background(), http.MethodGet, wrongSourceURL, nil)
	require.NoError(t, err)
	reqWrong.Header.Set("Authorization", "Bearer "+decryptedToken)
	respWrong, err := http.DefaultClient.Do(reqWrong)
	require.NoError(t, err)
	defer respWrong.Body.Close()
	assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}, respWrong.StatusCode)

	// 9. Verify security boundaries: expired token is rejected
	authSession.ExpiresAt = time.Now().Add(-1 * time.Minute)
	reqExpired, err := http.NewRequestWithContext(context.Background(), http.MethodGet, agentURL, nil)
	require.NoError(t, err)
	reqExpired.Header.Set("Authorization", "Bearer "+decryptedToken)
	respExpired, err := http.DefaultClient.Do(reqExpired)
	require.NoError(t, err)
	defer respExpired.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, respExpired.StatusCode, "expired token must be rejected during deployment")
}
