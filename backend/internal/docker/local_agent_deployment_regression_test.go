package docker

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/agent"
	"github.com/forgelab/backend/internal/analyzer"
	"github.com/forgelab/backend/internal/crypto"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/network"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
)

func TestLocalAgent_DeploymentWorkflow_Regression(t *testing.T) {
	// 1. Selected Source Setup:
	// root/
	//   frontend/package.json
	//   backend/go.mod
	rootDir := t.TempDir()

	frontendDir := filepath.Join(rootDir, "frontend")
	require.NoError(t, os.MkdirAll(frontendDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(frontendDir, "package.json"), []byte(`{"name":"test-frontend","scripts":{"start":"node index.js"}}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(frontendDir, "index.js"), []byte(`console.log("frontend ready");`), 0644))

	backendDir := filepath.Join(rootDir, "backend")
	require.NoError(t, os.MkdirAll(backendDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(backendDir, "go.mod"), []byte("module test-backend\n\ngo 1.22\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(backendDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644))

	// 2. Agent Analysis:
	// Analyzes rootDir and detects 'frontend' and 'backend'
	analysisResult, err := analyzer.AnalyzeRepository(rootDir)
	require.NoError(t, err)
	require.Len(t, analysisResult.Services, 2, "analyzer must discover both frontend and backend services")

	servicePaths := make(map[string]string)
	for _, s := range analysisResult.Services {
		servicePaths[s.Name] = s.SourcePath
	}
	assert.Equal(t, "frontend", servicePaths["frontend"], "frontend service source_path must be relative 'frontend'")
	assert.Equal(t, "backend", servicePaths["backend"], "backend service source_path must be relative 'backend'")

	// 3. Setup Mock Agent HTTP Server tracking requested service_path queries
	var requestedServicePaths []string
	var reqMu sync.Mutex

	agentID := "agent-regression-test-456"
	agentServer := agent.NewAgentServer(agent.AgentServerConfig{
		AgentID:      agentID,
		AllowedRoots: []string{rootDir},
	})
	defer agentServer.Close()

	agentTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/stream-context") {
			sp := r.URL.Query().Get("service_path")
			reqMu.Lock()
			requestedServicePaths = append(requestedServicePaths, sp)
			reqMu.Unlock()
		}
		agentServer.Router().ServeHTTP(w, r)
	}))
	defer agentTS.Close()

	t.Setenv("FORGELAB_AGENT_URL", agentTS.URL)

	// Register session in agent
	sm := agent.GetGlobalSessionManager()
	ownerID := uuid.New()
	authSession, err := sm.CreateSession(ownerID, agentID, 15*time.Minute)
	require.NoError(t, err)

	// Register directory on agent
	regReq, err := http.NewRequest(http.MethodPost, agentTS.URL+"/api/agent/select-path", strings.NewReader(fmt.Sprintf(`{"path":%q}`, rootDir)))
	require.NoError(t, err)
	regReq.Header.Set("Content-Type", "application/json")
	regReq.Header.Set("Authorization", "Bearer "+authSession.Token)

	regResp, err := http.DefaultClient.Do(regReq)
	require.NoError(t, err)
	defer regResp.Body.Close()
	require.Equal(t, http.StatusOK, regResp.StatusCode)

	var regData struct {
		SourceID string `json:"source_id"`
	}
	require.NoError(t, json.NewDecoder(regResp.Body).Decode(&regData))
	sourceUUID, err := uuid.Parse(regData.SourceID)
	require.NoError(t, err)

	// Consume session in session manager as project creation does
	sess, err := sm.FindSessionBySourceID(sourceUUID)
	require.NoError(t, err)
	require.NoError(t, sm.MarkConsumed(sess.ID))
	require.NoError(t, agentServer.ConsumeSession(sourceUUID))

	// Setup SourceService with encrypted token
	cryptoKey := "12345678901234567890123456789012"
	encryptor, err := crypto.NewEncryptor(cryptoKey)
	require.NoError(t, err)

	sourceService := services.NewSourceService(nil, t.TempDir(), encryptor)
	encTok, err := sourceService.EncryptToken(authSession.Token)
	require.NoError(t, err)

	require.NoError(t, sourceService.SaveSource(context.Background(), &models.Source{
		ID:                    sourceUUID,
		OwnerID:               ownerID,
		SourceType:            models.SourceTypeLocalAgent,
		SourceReference:       sourceUUID.String(),
		AgentID:               agentID,
		EncryptedSessionToken: encTok,
	}))

	// Setup Mock Docker Daemon Server to capture image build tarballs
	var builtImages []string
	var receivedTarballs [][]byte
	var buildMu sync.Mutex

	mockDockerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/build"):
			bodyBytes, err := io.ReadAll(r.Body)
			if err == nil {
				buildMu.Lock()
				receivedTarballs = append(receivedTarballs, bodyBytes)
				builtImages = append(builtImages, r.URL.Query().Get("t"))
				buildMu.Unlock()
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"stream":"Step 1/1 : Successfully built\n"}`+"\n")
			_, _ = fmt.Fprintf(w, `{"aux":{"ID":"sha256:abcd1234ef56"}}`+"\n")
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1.41/images/"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"Id":"sha256:abcd1234ef56","RepoDigests":["forgelab/test:abcd1234ef56"]}`)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer mockDockerServer.Close()

	dockerCli, err := client.NewClientWithOpts(
		client.WithHost(mockDockerServer.URL),
		client.WithHTTPClient(mockDockerServer.Client()),
		client.WithVersion("1.41"),
	)
	require.NoError(t, err)
	defer dockerCli.Close()

	pathValidator := security.NewPathValidator([]string{rootDir})
	engine := NewEngine(
		dockerCli,
		nil,
		nil,
		nil,
		sourceService,
		nil,
		network.NewPortManager(10000, 20000),
		pathValidator,
		nil,
		t.TempDir(),
	)

	// 4. Project Creation & Service Persistence Model
	project := &models.Project{
		ID:              uuid.New(),
		OwnerID:         ownerID,
		SourceType:      models.SourceTypeLocalAgent,
		SourceReference: sourceUUID.String(),
	}

	frontendService := &models.Service{
		ID:            uuid.New(),
		ProjectID:     project.ID,
		Name:          "frontend",
		SourcePath:    "frontend", // RELATIVE, not /app/frontend
		RuntimeType:   "nodejs",
		InternalPort:  3000,
		BuildStrategy: models.BuildStrategyAuto,
	}

	backendService := &models.Service{
		ID:            uuid.New(),
		ProjectID:     project.ID,
		Name:          "backend",
		SourcePath:    "backend", // RELATIVE, not /app/backend
		RuntimeType:   "go",
		InternalPort:  8080,
		BuildStrategy: models.BuildStrategyAuto,
	}

	// 5. Test Frontend Deployment:
	// Verify backend does NOT lstat /app/frontend and requests service_path=frontend
	t.Run("DeployFrontend_StreamFromAgent", func(t *testing.T) {
		cleanRel, pathErr := pathValidator.ValidateRelativeServicePath(frontendService.SourcePath)
		require.NoError(t, pathErr)
		assert.Equal(t, "frontend", cleanRel)

		// Request agent stream using clean path
		decryptedToken, err := sourceService.GetDecryptedAgentToken(context.Background(), project.OwnerID, sourceUUID)
		require.NoError(t, err)

		reqURL := fmt.Sprintf("%s/api/agent/sources/%s/stream-context?service_path=%s&runtime=nodejs&port=3000",
			agentTS.URL,
			project.SourceReference,
			cleanRel,
		)
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, reqURL, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+decryptedToken)
		req.Header.Set("X-Agent-Session-Token", decryptedToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// Verify tarball contains frontend files
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
		assert.True(t, foundPackageJSON, "streamed frontend tarball must contain package.json")
		assert.True(t, foundIndexJS, "streamed frontend tarball must contain index.js")
	})

	// 6. Test Backend Deployment:
	// Verify backend requests service_path=backend and streams go.mod
	t.Run("DeployBackend_StreamFromAgent", func(t *testing.T) {
		cleanRel, pathErr := pathValidator.ValidateRelativeServicePath(backendService.SourcePath)
		require.NoError(t, pathErr)
		assert.Equal(t, "backend", cleanRel)

		decryptedToken, err := sourceService.GetDecryptedAgentToken(context.Background(), project.OwnerID, sourceUUID)
		require.NoError(t, err)

		reqURL := fmt.Sprintf("%s/api/agent/sources/%s/stream-context?service_path=%s&runtime=go&port=8080",
			agentTS.URL,
			project.SourceReference,
			cleanRel,
		)
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, reqURL, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+decryptedToken)
		req.Header.Set("X-Agent-Session-Token", decryptedToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		tr := tar.NewReader(resp.Body)
		var foundGoMod, foundMainGo bool
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			if hdr.Name == "go.mod" {
				foundGoMod = true
			}
			if hdr.Name == "main.go" {
				foundMainGo = true
			}
		}
		assert.True(t, foundGoMod, "streamed backend tarball must contain go.mod")
		assert.True(t, foundMainGo, "streamed backend tarball must contain main.go")
	})

	// 7. Verify requested service_paths sent to agent
	reqMu.Lock()
	assert.Contains(t, requestedServicePaths, "frontend")
	assert.Contains(t, requestedServicePaths, "backend")
	reqMu.Unlock()

	// 8. Test Docker Build with streamed archive
	t.Run("DockerBuildWithAgentStream", func(t *testing.T) {
		decryptedToken, err := sourceService.GetDecryptedAgentToken(context.Background(), project.OwnerID, sourceUUID)
		require.NoError(t, err)

		reqURL := fmt.Sprintf("%s/api/agent/sources/%s/stream-context?service_path=frontend&runtime=nodejs&port=3000",
			agentTS.URL,
			project.SourceReference,
		)
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, reqURL, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+decryptedToken)
		req.Header.Set("X-Agent-Session-Token", decryptedToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		buildOpts := types.ImageBuildOptions{
			Tags:       []string{"forgelab/test/frontend:1"},
			Dockerfile: "Dockerfile.forgelab",
			Remove:     true,
		}

		err = engine.buildImage(context.Background(), resp.Body, buildOpts, func(msg string) {})
		require.NoError(t, err, "Docker image build from local agent stream must succeed")
	})

	// 9. Rejection tests:
	// ../outside, ../../outside, /absolute/path, C:\outside, \\server\share, frontend/../../outside
	t.Run("RejectDangerousServicePaths", func(t *testing.T) {
		dangerousPaths := []string{
			"../outside",
			"../../outside",
			"/absolute/path",
			`C:\outside`,
			`\\server\share`,
			"frontend/../../outside",
			"//server/share",
			`\system32\cmd.exe`,
		}

		for _, badPath := range dangerousPaths {
			t.Run(badPath, func(t *testing.T) {
				_, err := pathValidator.ValidateRelativeServicePath(badPath)
				assert.Error(t, err, "pathValidator.ValidateRelativeServicePath must reject: %s", badPath)

				// Agent server must also reject dangerous service_path parameter with 403 Forbidden
				decryptedToken, err := sourceService.GetDecryptedAgentToken(context.Background(), project.OwnerID, sourceUUID)
				require.NoError(t, err)

				badReqURL := fmt.Sprintf("%s/api/agent/sources/%s/stream-context?service_path=%s",
					agentTS.URL,
					project.SourceReference,
					badPath,
				)
				badReq, err := http.NewRequestWithContext(context.Background(), http.MethodGet, badReqURL, nil)
				require.NoError(t, err)
				badReq.Header.Set("Authorization", "Bearer "+decryptedToken)
				badReq.Header.Set("X-Agent-Session-Token", decryptedToken)

				badResp, err := http.DefaultClient.Do(badReq)
				require.NoError(t, err)
				defer badResp.Body.Close()
				assert.Equal(t, http.StatusForbidden, badResp.StatusCode, "agent must reject dangerous service_path with 403: %s", badPath)
			})
		}
	})

	// 10. Regression test proving local_directory still performs normal backend filesystem validation
	t.Run("LocalDirectory_EnforcesBackendFilesystemValidation", func(t *testing.T) {
		// Valid local directory inside allowed root passes
		validDir := filepath.Join(rootDir, "frontend")
		canonical, err := pathValidator.ValidateSourcePath(validDir)
		require.NoError(t, err)
		assert.NotEmpty(t, canonical)

		// Directory outside allowed roots fails
		outsideDir := t.TempDir()
		_, err = pathValidator.ValidateSourcePath(outsideDir)
		assert.ErrorIs(t, err, security.ErrPathNotAllowed, "local_directory outside allowed root must be rejected")

		// Non-existent directory fails
		_, err = pathValidator.ValidateSourcePath(filepath.Join(rootDir, "does-not-exist"))
		assert.Error(t, err, "non-existent local_directory must be rejected")

		// Path escaping service root fails ValidateServiceBuildPaths
		_, _, err = pathValidator.ValidateServiceBuildPaths(rootDir, "../outside", ".", "Dockerfile", false)
		assert.Error(t, err, "service path escaping repository boundary must be rejected")
	})
}
