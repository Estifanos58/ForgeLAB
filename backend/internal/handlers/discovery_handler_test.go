package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/agent"
	"github.com/forgelab/backend/internal/handlers"
	"github.com/forgelab/backend/internal/middleware"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/security"
)

func TestDiscoveryHandler_GeneratePlan_LocalAgent_AuthoritativeDiscovery(t *testing.T) {
	// 1. Create a workspace folder with docker-compose.yml and subservices on the "host"
	hostDir, err := os.MkdirTemp("", "forgelab-host-workspace-*")
	require.NoError(t, err)
	defer os.RemoveAll(hostDir)

	webDir := filepath.Join(hostDir, "web")
	require.NoError(t, os.MkdirAll(webDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(webDir, "Dockerfile"), []byte("FROM alpine\nCMD [\"echo\", \"web\"]"), 0644))

	composeContent := `services:
  postgres:
    image: postgres:16-alpine
    restart: unless-stopped
    ports:
      - "5432:5432"
  redis:
    image: redis:7-alpine
    restart: unless-stopped
    ports:
      - "6379:6379"
  web:
    build:
      context: ./web
      dockerfile: Dockerfile
    ports:
      - "3000:3000"
    depends_on:
      - postgres
      - redis
`
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "docker-compose.yml"), []byte(composeContent), 0644))

	testUserID := uuid.New()
	testToken := "test-agent-token-" + uuid.New().String()

	// 2. Set up Agent Session in backend SessionManager
	sm := agent.GetGlobalSessionManager()
	require.NotNil(t, sm)
	authSess, err := sm.CreateSession(testUserID, "agent-host-test", 30*time.Minute)
	require.NoError(t, err)
	testToken = authSess.Token

	// 3. Start Agent server as a separate HTTP process
	agentSrv := agent.NewAgentServer(agent.AgentServerConfig{
		AgentID:      "agent-host-test",
		Port:         0,
		AllowedRoots: []string{hostDir},
		SessionValidator: func(token, agentID string) (*agent.AgentSession, error) {
			if token == testToken {
				return authSess, nil
			}
			return nil, errorsNew("invalid token")
		},
		SourceSessionValidator: func(token string, sourceID uuid.UUID, agentID string) (*agent.AgentSession, error) {
			if token == testToken {
				return authSess, nil
			}
			return nil, errorsNew("invalid token")
		},
	})
	defer agentSrv.Close()

	agentTS := httptest.NewServer(agentSrv.Router())
	defer agentTS.Close()

	t.Setenv("FORGELAB_AGENT_URL", agentTS.URL)

	// Register directory with the Agent via HTTP endpoint
	selectReqBody, _ := json.Marshal(map[string]string{"path": hostDir})
	selReq, err := http.NewRequest(http.MethodPost, agentTS.URL+"/api/agent/select-path", bytes.NewReader(selectReqBody))
	require.NoError(t, err)
	selReq.Header.Set("Authorization", "Bearer "+testToken)
	selReq.Header.Set("Content-Type", "application/json")

	selResp, err := http.DefaultClient.Do(selReq)
	require.NoError(t, err)
	defer selResp.Body.Close()
	require.Equal(t, http.StatusOK, selResp.StatusCode)

	var regData map[string]interface{}
	require.NoError(t, json.NewDecoder(selResp.Body).Decode(&regData))
	sourceIDStr, ok := regData["source_id"].(string)
	require.True(t, ok)
	sourceUUID, err := uuid.Parse(sourceIDStr)
	require.NoError(t, err)

	// Bind source on session manager
	_, err = sm.BindSource(testToken, sourceUUID, filepath.Base(hostDir), "agent-host-test")
	require.NoError(t, err)

	// 4. Set up backend DiscoveryHandler
	pv := security.NewPathValidator([]string{})
	discoveryHandler := handlers.NewDiscoveryHandler(nil, nil, pv, nil)

	// 5. Execute GeneratePlan without repository_path
	planReqBody, _ := json.Marshal(handlers.GeneratePlanRequest{
		SourceType:      models.SourceTypeLocalAgent,
		SourceReference: sourceIDStr,
		AgentID:         "agent-host-test",
		AgentToken:      testToken,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/discovery/plan", bytes.NewReader(planReqBody))
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserIDKey, testUserID))
	rec := httptest.NewRecorder()

	discoveryHandler.GeneratePlan(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "Response: %s", rec.Body.String())

	var planResp handlers.GeneratePlanResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&planResp))

	require.NotNil(t, planResp.Discovery)
	require.NotNil(t, planResp.Plan)

	assert.Equal(t, "compose", planResp.Plan.Strategy)
	assert.Len(t, planResp.Plan.Services, 3)

	serviceMap := make(map[string]interface{})
	for _, svc := range planResp.Plan.Services {
		serviceMap[svc.Name] = svc
		if svc.Name == "postgres" {
			assert.Equal(t, "image", svc.BuildStrategy)
			assert.Equal(t, "postgres:16-alpine", svc.Image)
			assert.Equal(t, "infrastructure", svc.Classification)
		}
		if svc.Name == "redis" {
			assert.Equal(t, "image", svc.BuildStrategy)
			assert.Equal(t, "redis:7-alpine", svc.Image)
			assert.Equal(t, "infrastructure", svc.Classification)
		}
		if svc.Name == "web" {
			assert.Equal(t, "dockerfile", svc.BuildStrategy)
			assert.Contains(t, svc.DependsOn, "postgres")
			assert.Contains(t, svc.DependsOn, "redis")
		}
	}

	assert.Contains(t, serviceMap, "postgres")
	assert.Contains(t, serviceMap, "redis")
	assert.Contains(t, serviceMap, "web")
	assert.NotContains(t, serviceMap, "project", "no bogus project service should be generated")
}

func TestDiscoveryHandler_GeneratePlan_LocalAgent_SessionUnavailableOrExpired(t *testing.T) {
	testUserID := uuid.New()
	pv := security.NewPathValidator([]string{})
	discoveryHandler := handlers.NewDiscoveryHandler(nil, nil, pv, nil)

	// Set invalid agent URL to simulate agent offline / unavailable
	t.Setenv("FORGELAB_AGENT_URL", "http://127.0.0.1:49999")

	planReqBody, _ := json.Marshal(handlers.GeneratePlanRequest{
		SourceType:      models.SourceTypeLocalAgent,
		SourceReference: uuid.New().String(),
		AgentID:         "agent-offline",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/discovery/plan", bytes.NewReader(planReqBody))
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserIDKey, testUserID))
	rec := httptest.NewRecorder()

	discoveryHandler.GeneratePlan(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	var errResp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	assert.Contains(t, fmt.Sprint(errResp["error"]), "Local agent source session is unavailable or expired. Please reselect the folder.")
}

func errorsNew(msg string) error {
	return fmt.Errorf("%s", msg)
}
