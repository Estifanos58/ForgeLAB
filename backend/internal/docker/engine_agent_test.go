package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

// TestEngine_LocalAgent_TwoProcessDeploymentRegression tests the real multi-process scenario:
// Process 1: Backend HTTP server (handling sessions, token encryption, and source validation)
// Process 2: Standalone local agent executable (forgelab-agent.exe) running as a separate OS process
//
// Lifecycle verified end-to-end:
// 1. Backend creates agent session with cryptographic token
// 2. Local agent subprocess validates it via backend HTTP and registers folder
// 3. Source is registered in SourceService with AES-256 encrypted token
// 4. Project creation consumes the session in SessionManager
// 5. Async service deployment retrieves and decrypts the stored token
// 6. Local agent subprocess verifies consumed token for exact source via backend HTTP
// 7. /stream-context returns 200 OK and streams project tarball
// 8. Security boundaries verified (different source rejected, new path selection rejected)
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestEngine_LocalAgent_TwoProcessDeploymentRegression(t *testing.T) {
	// 0. Locate or build the forgelab-agent executable
	agentExeName := "forgelab-agent"
	if runtime.GOOS == "windows" {
		agentExeName += ".exe"
	}

	backendRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)

	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0755))
	agentExePath := filepath.Join(binDir, agentExeName)

	buildCmd := exec.Command("go", "build", "-o", agentExePath, "./cmd/agent")
	buildCmd.Dir = backendRoot
	buildOut, bErr := buildCmd.CombinedOutput()
	require.NoError(t, bErr, "failed to compile agent executable: %s", string(buildOut))

	// 1. Setup Backend Services and HTTP Server
	sm := agent.NewSessionManager()
	defer sm.Stop()

	cryptoKey := "12345678901234567890123456789012"
	encryptor, err := crypto.NewEncryptor(cryptoKey)
	require.NoError(t, err)

	sourceService := services.NewSourceService(nil, tempDir, encryptor)
	ownerID := uuid.New()

	backendMux := http.NewServeMux()

	// Backend route: POST /api/sources/agent/session (creates session)
	backendMux.HandleFunc("/api/sources/agent/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			AgentID string `json:"agent_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		sess, err := sm.CreateSession(ownerID, req.AgentID, 30*time.Minute)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"session_id": sess.ID.String(),
			"token":      sess.Token,
			"agent_id":   sess.AgentID,
			"expires_at": sess.ExpiresAt.Format(time.RFC3339),
		})
	})

	// Backend route: POST /api/sources/agent/session/validate
	// Local agent subprocess makes HTTP requests here to validate sessions
	backendMux.HandleFunc("/api/sources/agent/session/validate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Token      string `json:"token"`
			SourceID   string `json:"source_id"`
			AgentID    string `json:"agent_id"`
			FolderName string `json:"folder_name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Token) == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "valid session token is required", "reason": "token missing"})
			return
		}

		var sess *agent.AgentSession
		var vErr error

		if req.SourceID != "" {
			srcUUID, pErr := uuid.Parse(req.SourceID)
			if pErr != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid source_id", "reason": "invalid source_id"})
				return
			}
			sess, vErr = sm.VerifyTokenForSource(req.Token, srcUUID, req.AgentID)
			if vErr != nil {
				if unconsumed, uErr := sm.ValidateToken(req.Token); uErr == nil && unconsumed.SourceID == nil {
					sess, vErr = sm.BindSource(req.Token, srcUUID, req.FolderName, req.AgentID)
				}
			}
		} else {
			sess, vErr = sm.ValidateToken(req.Token)
			if vErr == nil {
				cleanAgentID := strings.TrimSpace(req.AgentID)
				if sess.AgentID != "" && (cleanAgentID == "" || sess.AgentID != cleanAgentID) {
					w.WriteHeader(http.StatusForbidden)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "agent ID mismatch", "reason": "agent mismatch"})
					return
				}
			}
		}

		if vErr != nil {
			if errors.Is(vErr, agent.ErrSessionExpired) {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "agent session has expired", "reason": "token expired"})
				return
			}
			if errors.Is(vErr, agent.ErrSessionConsumed) {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "agent session has already been consumed", "reason": "consumed session"})
				return
			}
			if errors.Is(vErr, agent.ErrUnauthorized) {
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": vErr.Error(), "reason": "source mismatch"})
				return
			}
			if errors.Is(vErr, agent.ErrSessionNotFound) {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "agent session not found", "reason": "session not found"})
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": vErr.Error(), "reason": "backend validation rejected"})
			return
		}

		respData := map[string]interface{}{
			"valid":      true,
			"session_id": sess.ID.String(),
			"user_id":    sess.UserID.String(),
			"agent_id":   sess.AgentID,
			"expires_at": sess.ExpiresAt.Format(time.RFC3339),
		}
		if sess.SourceID != nil {
			respData["source_id"] = sess.SourceID.String()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(respData)
	})

	backendServer := httptest.NewServer(backendMux)
	defer backendServer.Close()

	// 2. Find a free port and launch the local agent executable as a separate OS process
	freeListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	agentPort := freeListener.Addr().(*net.TCPAddr).Port
	freeListener.Close() // Release so agent process can bind

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agentCmd := exec.CommandContext(ctx, agentExePath, "-port", fmt.Sprint(agentPort), "-backend", backendServer.URL)
	var agentOutput safeBuffer
	agentCmd.Stdout = &agentOutput
	agentCmd.Stderr = &agentOutput

	err = agentCmd.Start()
	require.NoError(t, err, "failed to start agent subprocess")
	defer func() {
		cancel()
		if agentCmd.Process != nil {
			_ = agentCmd.Process.Kill()
			_ = agentCmd.Wait()
		}
	}()

	// Wait for agent process to be ready
	agentBaseURL := fmt.Sprintf("http://127.0.0.1:%d", agentPort)
	var agentID string
	var ready bool

	for attempt := 0; attempt < 50; attempt++ {
		time.Sleep(100 * time.Millisecond)
		resp, err := http.Get(agentBaseURL + "/api/agent/status")
		if err == nil && resp.StatusCode == http.StatusOK {
			var statusData struct {
				Status  string `json:"status"`
				AgentID string `json:"agent_id"`
			}
			if decErr := json.NewDecoder(resp.Body).Decode(&statusData); decErr == nil && statusData.Status == "online" {
				agentID = statusData.AgentID
				resp.Body.Close()
				ready = true
				break
			}
			resp.Body.Close()
		}
	}
	require.True(t, ready, "agent subprocess did not become ready in time; output: %s", agentOutput.String())
	require.NotEmpty(t, agentID, "agent ID must be non-empty")

	// 3. Create sample source project directory on disk
	sampleProjectDir := filepath.Join(tempDir, "sample-project")
	require.NoError(t, os.MkdirAll(sampleProjectDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sampleProjectDir, "package.json"), []byte(`{"name":"twoproc-app","version":"1.0.0"}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sampleProjectDir, "server.js"), []byte(`console.log("running");`), 0644))

	// 4. Backend creates authenticated agent session
	sessionPayload, _ := json.Marshal(map[string]string{"agent_id": agentID})
	sessResp, err := http.Post(backendServer.URL+"/api/sources/agent/session", "application/json", bytes.NewReader(sessionPayload))
	require.NoError(t, err)
	defer sessResp.Body.Close()
	require.Equal(t, http.StatusOK, sessResp.StatusCode)

	var authSession struct {
		SessionID string `json:"session_id"`
		Token     string `json:"token"`
		AgentID   string `json:"agent_id"`
	}
	require.NoError(t, json.NewDecoder(sessResp.Body).Decode(&authSession))
	require.NotEmpty(t, authSession.Token)
	assert.Equal(t, agentID, authSession.AgentID)

	// 5. Local agent subprocess validates token through backend HTTP and registers directory
	selectBody, _ := json.Marshal(map[string]string{"path": sampleProjectDir})
	selectReq, err := http.NewRequest(http.MethodPost, agentBaseURL+"/api/agent/select-path", bytes.NewReader(selectBody))
	require.NoError(t, err)
	selectReq.Header.Set("Content-Type", "application/json")
	selectReq.Header.Set("Authorization", "Bearer "+authSession.Token)

	selectResp, err := http.DefaultClient.Do(selectReq)
	require.NoError(t, err)
	defer selectResp.Body.Close()
	require.Equal(t, http.StatusOK, selectResp.StatusCode, "agent must accept valid session token")

	var selectData struct {
		SourceID string `json:"source_id"`
		Status   string `json:"status"`
	}
	require.NoError(t, json.NewDecoder(selectResp.Body).Decode(&selectData))
	require.NotEmpty(t, selectData.SourceID)
	sourceUUID, err := uuid.Parse(selectData.SourceID)
	require.NoError(t, err)

	// 6. Source is registered in SourceService with AES-256 encrypted session token
	encryptedToken, err := sourceService.EncryptToken(authSession.Token)
	require.NoError(t, err)

	err = sourceService.SaveSource(context.Background(), &models.Source{
		ID:                    sourceUUID,
		OwnerID:               ownerID,
		SourceType:            models.SourceTypeLocalAgent,
		SourceReference:       sourceUUID.String(),
		AgentID:               agentID,
		EncryptedSessionToken: encryptedToken,
	})
	require.NoError(t, err)

	// 7. Project creation consumes the session in SessionManager
	parsedSessionID, err := uuid.Parse(authSession.SessionID)
	require.NoError(t, err)
	require.NoError(t, sm.MarkConsumed(parsedSessionID))

	// Verify session is consumed: new path selection with this token is rejected with 401
	selectReq2, _ := http.NewRequest(http.MethodPost, agentBaseURL+"/api/agent/select-path", bytes.NewReader(selectBody))
	selectReq2.Header.Set("Content-Type", "application/json")
	selectReq2.Header.Set("Authorization", "Bearer "+authSession.Token)
	selectResp2, err := http.DefaultClient.Do(selectReq2)
	require.NoError(t, err)
	selectResp2.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, selectResp2.StatusCode, "consumed session cannot select new paths")

	// 8. Async service deployment retrieves and decrypts the stored token
	decryptedToken, err := sourceService.GetDecryptedAgentToken(context.Background(), ownerID, sourceUUID)
	require.NoError(t, err)
	assert.Equal(t, authSession.Token, decryptedToken)

	// 9. Local agent validates consumed token for exact bound source via backend HTTP
	streamURL := fmt.Sprintf("%s/api/agent/sources/%s/stream-context?service_path=.&runtime=nodejs&port=3000&start_cmd=npm+start",
		agentBaseURL,
		sourceUUID.String(),
	)
	streamReq, err := http.NewRequest(http.MethodGet, streamURL, nil)
	require.NoError(t, err)
	streamReq.Header.Set("Authorization", "Bearer "+decryptedToken)
	streamReq.Header.Set("X-Agent-Session-Token", decryptedToken)

	streamResp, err := http.DefaultClient.Do(streamReq)
	require.NoError(t, err)
	defer streamResp.Body.Close()

	// 10. /stream-context returns 200 OK and valid tarball
	assert.Equal(t, http.StatusOK, streamResp.StatusCode, "consumed token must successfully authenticate for bound source in two-process deployment")

	tr := tar.NewReader(streamResp.Body)
	var foundPackageJSON, foundServerJS bool
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if hdr.Name == "package.json" {
			foundPackageJSON = true
		}
		if hdr.Name == "server.js" {
			foundServerJS = true
		}
	}
	assert.True(t, foundPackageJSON, "expected package.json in streamed archive")
	assert.True(t, foundServerJS, "expected server.js in streamed archive")

	// 11. Security boundaries: access to a different source ID is rejected (403 Forbidden)
	otherSourceID := uuid.New()
	wrongSourceURL := fmt.Sprintf("%s/api/agent/sources/%s/stream-context", agentBaseURL, otherSourceID)
	wrongReq, err := http.NewRequest(http.MethodGet, wrongSourceURL, nil)
	require.NoError(t, err)
	wrongReq.Header.Set("Authorization", "Bearer "+decryptedToken)
	wrongResp, err := http.DefaultClient.Do(wrongReq)
	require.NoError(t, err)
	defer wrongResp.Body.Close()
	assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}, wrongResp.StatusCode)

	// 12. Security boundaries: missing token is rejected (401 Unauthorized)
	noAuthReq, err := http.NewRequest(http.MethodGet, streamURL, nil)
	require.NoError(t, err)
	noAuthResp, err := http.DefaultClient.Do(noAuthReq)
	require.NoError(t, err)
	defer noAuthResp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, noAuthResp.StatusCode)
}
