package agent

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/analyzer"
	"github.com/forgelab/backend/internal/security"
)

// setupMockAgentServer creates a test AgentServer with a sample repository directory structure
func setupMockRepoWithEnv(t *testing.T) (string, uuid.UUID, string, *AgentServer, *httptest.Server) {
	repoDir := t.TempDir()

	// Root .env
	rootEnv := "DATABASE_URL=postgres://root:rootpass@localhost:5432/main\nAPI_URL=https://api.root.com\nSHARED_CONFIG=default\n"
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".env"), []byte(rootEnv), 0644))

	// Root .env.example (should be ignored)
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".env.example"), []byte("EXAMPLE_KEY=do_not_import\n"), 0644))

	// Frontend directory and .env
	feDir := filepath.Join(repoDir, "frontend")
	require.NoError(t, os.MkdirAll(feDir, 0755))
	feEnv := "API_URL=https://frontend-specific.example.com\nNEXT_PUBLIC_APP_NAME=MyFrontend\n"
	require.NoError(t, os.WriteFile(filepath.Join(feDir, ".env"), []byte(feEnv), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(feDir, "package.json"), []byte(`{"name":"fe"}`), 0644))

	// Backend directory and .env
	beDir := filepath.Join(repoDir, "backend")
	require.NoError(t, os.MkdirAll(beDir, 0755))
	beEnv := "DATABASE_URL=postgres://be_user:bepass@localhost:5432/be\nJWT_SECRET=super-secret-jwt-token-12345\n"
	require.NoError(t, os.WriteFile(filepath.Join(beDir, ".env"), []byte(beEnv), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(beDir, "go.mod"), []byte("module be\n"), 0644))

	// Register session in agent server
	token := "valid-token-123"
	sourceID := uuid.New()

	agentSrv := NewAgentServer(AgentServerConfig{
		Port: 4142,
		SessionValidator: func(tok, agentID string) (*AgentSession, error) {
			if tok != token {
				return nil, ErrUnauthorized
			}
			return &AgentSession{
				Token:    tok,
				AgentID:  agentID,
				SourceID: &sourceID,
			}, nil
		},
	})

	session := &LocalSourceSession{
		SourceID:      sourceID,
		CanonicalPath: repoDir,
		FolderName:    "test-repo",
		Status:        "ready",
		CreatedAt:     time.Now(),
		ExpiresAt:     time.Now().Add(1 * time.Hour),
		Token:         token,
	}

	agentSrv.mu.Lock()
	agentSrv.sessions[sourceID] = session
	agentSrv.mu.Unlock()

	ts := httptest.NewServer(agentSrv.Router())

	return repoDir, sourceID, token, agentSrv, ts
}

func TestLocalAgentPathBehavior(t *testing.T) {
	validPaths := []string{
		"frontend",
		"backend",
		".",
		"apps/web",
	}

	for _, p := range validPaths {
		clean, err := security.ValidateRelativeServicePath(p)
		assert.NoError(t, err, "path %q should be valid", p)
		assert.NotEmpty(t, clean)
	}

	invalidPaths := []string{
		"../outside",
		"../../outside",
		"/absolute",
		`C:\outside`,
		`\\server\share`,
		"frontend/../../outside",
	}

	for _, p := range invalidPaths {
		_, err := security.ValidateRelativeServicePath(p)
		assert.Error(t, err, "path %q must be rejected", p)
	}
}

func TestAgentEnvironmentEndpoint_DiscoveryAndOverrides(t *testing.T) {
	_, sourceID, token, _, ts := setupMockRepoWithEnv(t)
	defer ts.Close()

	// 1. Request environment with token
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/agent/sources/%s/environment?service_path=frontend&service_path=backend", ts.URL, sourceID), nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var envResp struct {
		SourceID uuid.UUID                    `json:"source_id"`
		Root     map[string]string            `json:"root"`
		Services map[string]map[string]string `json:"services"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&envResp))

	// Verify root .env discovered
	assert.Equal(t, "postgres://root:rootpass@localhost:5432/main", envResp.Root["DATABASE_URL"])
	assert.Equal(t, "https://api.root.com", envResp.Root["API_URL"])
	assert.Equal(t, "default", envResp.Root["SHARED_CONFIG"])

	// Verify .env.example was NOT imported
	assert.NotContains(t, envResp.Root, "EXAMPLE_KEY")

	// Verify frontend .env discovered
	require.Contains(t, envResp.Services, "frontend")
	assert.Equal(t, "https://frontend-specific.example.com", envResp.Services["frontend"]["API_URL"])
	assert.Equal(t, "MyFrontend", envResp.Services["frontend"]["NEXT_PUBLIC_APP_NAME"])

	// Verify backend .env discovered
	require.Contains(t, envResp.Services, "backend")
	assert.Equal(t, "postgres://be_user:bepass@localhost:5432/be", envResp.Services["backend"]["DATABASE_URL"])
	assert.Equal(t, "super-secret-jwt-token-12345", envResp.Services["backend"]["JWT_SECRET"])
}

func TestAgentEnvironmentEndpoint_SecurityAndAuthorization(t *testing.T) {
	repoDir, sourceID, token, _, ts := setupMockRepoWithEnv(t)
	defer ts.Close()

	// 1. Missing token -> 401 Unauthorized
	req1, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/agent/sources/%s/environment", ts.URL, sourceID), nil)
	resp1, err := http.DefaultClient.Do(req1)
	require.NoError(t, err)
	defer resp1.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp1.StatusCode)

	// 2. Wrong token -> 403 Forbidden
	req2, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/agent/sources/%s/environment", ts.URL, sourceID), nil)
	req2.Header.Set("Authorization", "Bearer wrong-token")
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp2.StatusCode)

	// 3. Different source ID with token bound to first source -> 403 Forbidden
	diffSourceID := uuid.New()
	req3, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/agent/sources/%s/environment", ts.URL, diffSourceID), nil)
	req3.Header.Set("Authorization", "Bearer "+token)
	resp3, err := http.DefaultClient.Do(req3)
	require.NoError(t, err)
	defer resp3.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp3.StatusCode)

	// 4. Path traversal attempt in service_path
	req4, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/agent/sources/%s/environment?service_path=../outside", ts.URL, sourceID), nil)
	req4.Header.Set("Authorization", "Bearer "+token)
	resp4, err := http.DefaultClient.Do(req4)
	require.NoError(t, err)
	defer resp4.Body.Close()
	require.Equal(t, http.StatusOK, resp4.StatusCode)

	var envResp struct {
		Services map[string]map[string]string `json:"services"`
	}
	_ = json.NewDecoder(resp4.Body).Decode(&envResp)
	assert.NotContains(t, envResp.Services, "../outside", "traversal path must be ignored")

	// 5. Source analysis response must NEVER contain .env contents
	analysis, err := analyzer.AnalyzeRepository(repoDir)
	require.NoError(t, err)
	analysisJSON, _ := json.Marshal(analysis)
	assert.False(t, strings.Contains(string(analysisJSON), "rootpass"), "source analysis must not leak secrets")
	assert.False(t, strings.Contains(string(analysisJSON), "super-secret-jwt-token-12345"), "source analysis must not leak secrets")

	// 6. Docker tar context must NEVER contain .env files
	streamURL := fmt.Sprintf("%s/api/agent/sources/%s/stream-context?service_path=frontend", ts.URL, sourceID)
	streamReq, _ := http.NewRequest(http.MethodGet, streamURL, nil)
	streamReq.Header.Set("Authorization", "Bearer "+token)
	streamResp, err := http.DefaultClient.Do(streamReq)
	require.NoError(t, err)
	defer streamResp.Body.Close()

	tarReader := tar.NewReader(streamResp.Body)
	for {
		hdr, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		assert.False(t, strings.HasSuffix(hdr.Name, ".env"), ".env must never be included in Docker tar context: %s", hdr.Name)
		assert.False(t, strings.Contains(hdr.Name, ".env."), ".env.* must never be included in Docker tar context: %s", hdr.Name)
	}
}
