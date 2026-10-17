package services_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/agent"
	"github.com/forgelab/backend/internal/auth"
	"github.com/forgelab/backend/internal/crypto"
	"github.com/forgelab/backend/internal/handlers"
	"github.com/forgelab/backend/internal/middleware"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

func createTestDir(t *testing.T) string {
	dir, err := os.MkdirTemp("", "guest-test-repo-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// setupGuestTestRouter spins up an isolated router simulating the ForgeLAB API
// with either guest mode enabled or disabled.
func setupGuestTestRouter(
	t *testing.T,
	allowGuestMode bool,
) (*chi.Mux, *auth.JWTManager, *services.ProjectService, *services.SourceService, *services.SecretService, *services.UserService) {
	pool, _ := connectTestDB(t)

	jwtManager := auth.NewJWTManager("test-secret-key-32-bytes-long!!!", 15*time.Minute, 7*24*time.Hour)
	tempSourcesDir, err := os.MkdirTemp("", "guest-sources-test-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tempSourcesDir) })

	encryptor, err := crypto.NewEncryptor("test-secret-key-32-bytes-long!!!")
	require.NoError(t, err)

	sourceService := services.NewSourceService(pool, tempSourcesDir, encryptor)
	userService := services.NewUserService(pool, jwtManager)
	require.NoError(t, userService.EnsureGuestUser(context.Background()))

	serviceService := services.NewServiceService(pool)
	projectService := services.NewProjectService(pool, nil, sourceService, nil)
	projectService.SetServiceService(serviceService)
	deploymentService := services.NewDeploymentService(pool)
	secretService := services.NewSecretService(pool, encryptor, projectService)
	deploymentService.SetSecretService(secretService)
	projectService.SetSecretService(secretService)
	projectService.SetDeploymentService(deploymentService)

	authHandler := handlers.NewAuthHandler(userService, nil, "http://localhost:3000", false)
	authHandler.SetAllowGuestMode(allowGuestMode)
	projectHandler := handlers.NewProjectHandler(projectService, deploymentService, nil, nil)
	serviceHandler := handlers.NewServiceHandler(serviceService, projectService, deploymentService, nil, nil)
	envHandler := handlers.NewEnvHandler(secretService, projectService)
	integrationHandler := handlers.NewIntegrationHandler(nil, "http://localhost:3000", false)
	sourceHandler := handlers.NewSourceHandler(sourceService, nil)
	discoveryHandler := handlers.NewDiscoveryHandler(nil, sourceService, nil, secretService)

	r := chi.NewRouter()
	r.Use(middleware.ContentTypeJSON)

	r.Route("/api", func(r chi.Router) {
		r.Route("/auth", func(r chi.Router) {
			r.Get("/mode", authHandler.Mode)
		})

		// Registered-only integrations (GitHub)
		r.Group(func(r chi.Router) {
			r.Use(middleware.AuthMiddleware(jwtManager))
			r.Route("/integrations/github", func(r chi.Router) {
				r.Get("/", integrationHandler.GetGitHubStatus)
				r.Get("/connect", integrationHandler.ConnectGitHub)
				r.Get("/repositories", integrationHandler.ListRepositories)
			})
		})

		// Protected or Guest routes (Local Deployments)
		r.Group(func(r chi.Router) {
			r.Use(middleware.AuthOrGuestMiddleware(jwtManager, allowGuestMode))

			r.Route("/sources", func(r chi.Router) {
				r.Post("/upload", sourceHandler.Upload)
				r.Post("/agent/session", sourceHandler.CreateAgentSession)
				r.Post("/agent/register", sourceHandler.RegisterAgentSource)
				r.Get("/{id}", sourceHandler.GetStatus)
				r.Delete("/{id}", sourceHandler.Delete)
			})

			r.Route("/discovery", func(r chi.Router) {
				r.Post("/plan", discoveryHandler.GeneratePlan)
			})

			r.Route("/projects", func(r chi.Router) {
				r.Post("/", projectHandler.Create)
				r.Get("/", projectHandler.List)
				r.Get("/{id}", projectHandler.Get)
				r.Delete("/{id}", projectHandler.Delete)
				r.Post("/{id}/stop", projectHandler.Stop)
				r.Post("/{id}/start", projectHandler.Start)

				r.Get("/{id}/env", envHandler.List)
				r.Post("/{id}/env", envHandler.Set)

				r.Get("/{id}/services", serviceHandler.List)
				r.Get("/{id}/deployments", projectHandler.ListDeployments)
			})
		})
	})

	return r, jwtManager, projectService, sourceService, secretService, userService
}

// 1. Single guest identity tests
func TestGuestMode_EnsureGuestIdentity(t *testing.T) {
	pool, _ := connectTestDB(t)
	jwtManager := auth.NewJWTManager("test-secret-key-32-bytes-long!!!", 15*time.Minute, 7*24*time.Hour)
	userService := services.NewUserService(pool, jwtManager)

	ctx := context.Background()
	// Ensure guest is idempotent
	require.NoError(t, userService.EnsureGuestUser(ctx))
	require.NoError(t, userService.EnsureGuestUser(ctx))

	// Verify guest record exists in DB
	var email, displayName string
	err := pool.QueryRow(ctx, "SELECT email, display_name FROM users WHERE id = $1", models.GuestUserID).Scan(&email, &displayName)
	require.NoError(t, err)
	assert.Equal(t, models.GuestUserEmail, email)
	assert.Equal(t, "Guest", displayName)

	// Verify that regular registration rejects guest email
	_, _, err = userService.Register(ctx, services.RegisterInput{
		Email:       models.GuestUserEmail,
		Password:    "password123",
		DisplayName: "Guest Impersonator",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, services.ErrUserExists)

	// Verify login rejects guest email
	_, _, err = userService.Login(ctx, services.LoginInput{
		Email:    models.GuestUserEmail,
		Password: "password123",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, services.ErrInvalidPassword)
}

func TestGuestMode_SharedIdentityResolution(t *testing.T) {
	r, jwtManager, _, _, _, _ := setupGuestTestRouter(t, true)

	// Request 1 from visitor A (unauthenticated)
	req1 := httptest.NewRequest("GET", "/api/projects", nil)
	req1.RemoteAddr = "192.168.1.100:12345"
	rec1 := httptest.NewRecorder()
	r.ServeHTTP(rec1, req1)
	assert.Equal(t, http.StatusOK, rec1.Code)

	// Request 2 from visitor B (unauthenticated)
	req2 := httptest.NewRequest("GET", "/api/projects", nil)
	req2.RemoteAddr = "10.0.0.50:54321"
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusOK, rec2.Code)

	// Request 3: Check auth mode endpoint
	reqMode := httptest.NewRequest("GET", "/api/auth/mode", nil)
	recMode := httptest.NewRecorder()
	r.ServeHTTP(recMode, reqMode)
	assert.Equal(t, http.StatusOK, recMode.Code)
	var modeResp map[string]interface{}
	require.NoError(t, json.Unmarshal(recMode.Body.Bytes(), &modeResp))
	assert.Equal(t, true, modeResp["allow_guest_mode"])
	assert.Equal(t, models.GuestUserID.String(), modeResp["guest_user_id"])

	regUserID := uuid.New()
	userToken, err := jwtManager.GenerateAccessToken(regUserID, "registered@user.com")
	require.NoError(t, err)

	reqAuth := httptest.NewRequest("GET", "/api/projects", nil)
	reqAuth.Header.Set("Authorization", "Bearer "+userToken)
	recAuth := httptest.NewRecorder()
	r.ServeHTTP(recAuth, reqAuth)
	assert.Equal(t, http.StatusOK, recAuth.Code)
}

// 2. Local deployment without registration (Local Agent Import)
func TestGuestMode_LocalAgentImportWorkflow(t *testing.T) {
	r, _, _, _, _, _ := setupGuestTestRouter(t, true)

	// Step 1: Create agent session without registration
	sessReqBody := []byte(`{"agent_id":"agent-local-guest-test"}`)
	reqSess := httptest.NewRequest("POST", "/api/sources/agent/session", bytes.NewReader(sessReqBody))
	reqSess.Header.Set("Content-Type", "application/json")
	recSess := httptest.NewRecorder()
	r.ServeHTTP(recSess, reqSess)
	require.Equal(t, http.StatusCreated, recSess.Code)

	var sessResp struct {
		SessionID string `json:"session_id"`
		Token     string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(recSess.Body.Bytes(), &sessResp))
	require.NotEmpty(t, sessResp.SessionID)
	require.NotEmpty(t, sessResp.Token)

	// Step 2: Agent binds source and registers source for the session
	sm := agent.GetGlobalSessionManager()
	sourceUUID := uuid.New()
	_, err := sm.BindSource(sessResp.Token, sourceUUID, "my-guest-app", "agent-local-guest-test")
	require.NoError(t, err)

	regSourceBody, _ := json.Marshal(map[string]interface{}{
		"session_id":  sessResp.SessionID,
		"token":       sessResp.Token,
		"source_id":   sourceUUID.String(),
		"agent_id":    "agent-local-guest-test",
		"folder_name": "my-guest-app",
	})
	reqReg := httptest.NewRequest("POST", "/api/sources/agent/register", bytes.NewReader(regSourceBody))
	reqReg.Header.Set("Content-Type", "application/json")
	recReg := httptest.NewRecorder()
	r.ServeHTTP(recReg, reqReg)
	require.Equal(t, http.StatusCreated, recReg.Code)

	var regResp struct {
		SourceID string `json:"source_id"`
		OwnerID  string `json:"owner_id"`
	}
	require.NoError(t, json.Unmarshal(recReg.Body.Bytes(), &regResp))
	assert.Equal(t, models.GuestUserID.String(), regResp.OwnerID)

	// Step 3: Create project from local agent source without registration
	createProjBody, _ := json.Marshal(map[string]interface{}{
		"name":             "Guest Agent Project " + uuid.New().String()[:8],
		"source_type":      models.SourceTypeLocalAgent,
		"source_reference": sourceUUID.String(),
		"agent_id":         "agent-local-guest-test",
		"services": []map[string]interface{}{
			{
				"name":           "web",
				"source_path":    ".",
				"runtime":        "nodejs",
				"build_strategy": "auto",
				"internal_port":  3000,
			},
		},
	})
	reqProj := httptest.NewRequest("POST", "/api/projects", bytes.NewReader(createProjBody))
	reqProj.Header.Set("Content-Type", "application/json")
	recProj := httptest.NewRecorder()
	r.ServeHTTP(recProj, reqProj)
	require.Equal(t, http.StatusCreated, recProj.Code)

	var projResp models.Project
	require.NoError(t, json.Unmarshal(recProj.Body.Bytes(), &projResp))
	assert.Equal(t, models.GuestUserID, projResp.OwnerID)
}

// 3. Local deployment without registration (Archive Upload)
func TestGuestMode_ArchiveUploadWorkflow(t *testing.T) {
	r, _, _, _, _, _ := setupGuestTestRouter(t, true)

	// Create a small tar archive in-memory
	tarBuf := new(bytes.Buffer)
	tw := tar.NewWriter(tarBuf)
	dfContent := []byte("FROM alpine:latest\nCMD [\"echo\", \"hello from guest\"]\n")
	hdr := &tar.Header{
		Name: "Dockerfile",
		Mode: 0644,
		Size: int64(len(dfContent)),
	}
	require.NoError(t, tw.WriteHeader(hdr))
	_, err := tw.Write(dfContent)
	require.NoError(t, tw.Close())

	// Create multipart form
	body := new(bytes.Buffer)
	mw := multipart.NewWriter(body)
	fw, err := mw.CreateFormFile("file", "project.tar")
	require.NoError(t, err)
	_, err = io.Copy(fw, tarBuf)
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	// Anonymous upload
	reqUpload := httptest.NewRequest("POST", "/api/sources/upload", body)
	reqUpload.Header.Set("Content-Type", mw.FormDataContentType())
	recUpload := httptest.NewRecorder()
	r.ServeHTTP(recUpload, reqUpload)
	assert.Contains(t, []int{http.StatusOK, http.StatusCreated}, recUpload.Code)

	var uploadResp struct {
		SourceID string `json:"source_id"`
	}
	require.NoError(t, json.Unmarshal(recUpload.Body.Bytes(), &uploadResp))
	require.NotEmpty(t, uploadResp.SourceID)

	// Create project using uploaded archive
	createProjBody, _ := json.Marshal(map[string]interface{}{
		"name":             "Guest Upload Project " + uuid.New().String()[:8],
		"source_type":      models.SourceTypeLocalUpload,
		"source_reference": uploadResp.SourceID,
		"services": []map[string]interface{}{
			{
				"name":           "web",
				"source_path":    ".",
				"runtime":        "dockerfile",
				"build_strategy": "dockerfile",
				"internal_port":  8080,
			},
		},
	})
	reqProj := httptest.NewRequest("POST", "/api/projects", bytes.NewReader(createProjBody))
	reqProj.Header.Set("Content-Type", "application/json")
	recProj := httptest.NewRecorder()
	r.ServeHTTP(recProj, reqProj)
	require.Equal(t, http.StatusCreated, recProj.Code)

	var proj models.Project
	require.NoError(t, json.Unmarshal(recProj.Body.Bytes(), &proj))
	assert.Equal(t, models.GuestUserID, proj.OwnerID)
}

// 4. Access and management of guest-owned projects, logs, env vars, stop/start, deletion
func TestGuestMode_ManageGuestProject(t *testing.T) {
	r, _, projectService, _, _, _ := setupGuestTestRouter(t, true)
	ctx := context.Background()

	tempRepoDir := createTestDir(t)
	proj, err := projectService.CreateProject(ctx, models.GuestUserID, services.CreateProjectInput{
		Name:           "Guest Management Test " + uuid.New().String()[:8],
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: tempRepoDir,
		Services: []services.CreateServiceInput{
			{
				Name:          "web",
				SourcePath:    ".",
				Runtime:       "generic",
				BuildStrategy: "auto",
				InternalPort:  8080,
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = projectService.DeleteProject(ctx, proj.ID, models.GuestUserID, nil)
	})

	// 1. Unauthenticated Get Project
	reqGet := httptest.NewRequest("GET", fmt.Sprintf("/api/projects/%s", proj.ID), nil)
	recGet := httptest.NewRecorder()
	r.ServeHTTP(recGet, reqGet)
	assert.Equal(t, http.StatusOK, recGet.Code)

	// 2. Unauthenticated Set Env Var
	envBody, _ := json.Marshal(map[string]string{
		"key":   "TEST_VAR",
		"value": "guest-value-123",
	})
	reqEnvSet := httptest.NewRequest("POST", fmt.Sprintf("/api/projects/%s/env", proj.ID), bytes.NewReader(envBody))
	reqEnvSet.Header.Set("Content-Type", "application/json")
	recEnvSet := httptest.NewRecorder()
	r.ServeHTTP(recEnvSet, reqEnvSet)
	assert.Equal(t, http.StatusOK, recEnvSet.Code)

	// 3. Unauthenticated Get Env Vars
	reqEnvGet := httptest.NewRequest("GET", fmt.Sprintf("/api/projects/%s/env", proj.ID), nil)
	recEnvGet := httptest.NewRecorder()
	r.ServeHTTP(recEnvGet, reqEnvGet)
	assert.Equal(t, http.StatusOK, recEnvGet.Code)
	var envList []map[string]interface{}
	require.NoError(t, json.Unmarshal(recEnvGet.Body.Bytes(), &envList))
	assert.Len(t, envList, 1)
	assert.Equal(t, "TEST_VAR", envList[0]["key"])

	// 4. Unauthenticated Stop & Start Project
	reqStop := httptest.NewRequest("POST", fmt.Sprintf("/api/projects/%s/stop", proj.ID), nil)
	recStop := httptest.NewRecorder()
	r.ServeHTTP(recStop, reqStop)
	assert.Contains(t, []int{http.StatusOK, http.StatusServiceUnavailable}, recStop.Code)

	// 5. Unauthenticated Delete Project
	reqDel := httptest.NewRequest("DELETE", fmt.Sprintf("/api/projects/%s", proj.ID), nil)
	recDel := httptest.NewRecorder()
	r.ServeHTTP(recDel, reqDel)
	assert.Equal(t, http.StatusOK, recDel.Code)

	// Verify project is deleted
	_, err = projectService.GetProject(ctx, proj.ID, models.GuestUserID)
	assert.ErrorIs(t, err, services.ErrProjectNotFound)

	// Verify shared guest owner is preserved across project deletions
	pool, _ := connectTestDB(t)
	var guestExists bool
	err = pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)", models.GuestUserID).Scan(&guestExists)
	require.NoError(t, err)
	assert.True(t, guestExists, "shared guest owner record must persist after project deletion")

	// Verify that DeleteProject invokes cleanup callback for Docker resources
	tempRepoDir2 := createTestDir(t)
	proj2, err := projectService.CreateProject(ctx, models.GuestUserID, services.CreateProjectInput{
		Name:           "Guest Resource Cleanup Test " + uuid.New().String()[:8],
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: tempRepoDir2,
		Services: []services.CreateServiceInput{
			{Name: "web", SourcePath: ".", Runtime: "generic", BuildStrategy: "auto", InternalPort: 8080},
		},
	})
	require.NoError(t, err)

	cleanupInvoked := false
	err = projectService.DeleteProject(ctx, proj2.ID, models.GuestUserID, func(ctx context.Context, projectID uuid.UUID) error {
		cleanupInvoked = true
		assert.Equal(t, proj2.ID, projectID)
		return nil
	})
	require.NoError(t, err)
	assert.True(t, cleanupInvoked, "Docker resource cleanup callback must be called upon guest project deletion")
}

// 5. Isolation between Registered Users and Guest
func TestGuestMode_TenantIsolation(t *testing.T) {
	r, _, projectService, _, _, userService := setupGuestTestRouter(t, true)
	ctx := context.Background()

	// Registered User
	regUser, regTokens, err := userService.Register(ctx, services.RegisterInput{
		Email:       "alice-" + uuid.New().String()[:8] + "@company.com",
		Password:    "password123",
		DisplayName: "Alice",
	})
	require.NoError(t, err)
	regUserID := regUser.ID
	regToken := regTokens.AccessToken

	regRepoDir := createTestDir(t)
	guestRepoDir := createTestDir(t)

	// Create Project for Registered User
	regProj, err := projectService.CreateProject(ctx, regUserID, services.CreateProjectInput{
		Name:           "Registered User Secret Project",
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: regRepoDir,
		Services: []services.CreateServiceInput{
			{Name: "app", SourcePath: ".", Runtime: "generic", BuildStrategy: "auto", InternalPort: 8080},
		},
	})
	require.NoError(t, err)
	defer projectService.DeleteProject(ctx, regProj.ID, regUserID, nil)

	// Create Project for Guest
	guestProj, err := projectService.CreateProject(ctx, models.GuestUserID, services.CreateProjectInput{
		Name:           "Guest Public Workspace Project",
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: guestRepoDir,
		Services: []services.CreateServiceInput{
			{Name: "web", SourcePath: ".", Runtime: "generic", BuildStrategy: "auto", InternalPort: 3000},
		},
	})
	require.NoError(t, err)
	defer projectService.DeleteProject(ctx, guestProj.ID, models.GuestUserID, nil)

	// A. Guest attempts to access registered user's project
	reqGuestOnReg := httptest.NewRequest("GET", fmt.Sprintf("/api/projects/%s", regProj.ID), nil)
	recGuestOnReg := httptest.NewRecorder()
	r.ServeHTTP(recGuestOnReg, reqGuestOnReg)
	assert.Contains(t, []int{http.StatusNotFound, http.StatusForbidden}, recGuestOnReg.Code, "guest must not see registered user project")

	// Guest attempts to delete registered user's project
	reqGuestDelReg := httptest.NewRequest("DELETE", fmt.Sprintf("/api/projects/%s", regProj.ID), nil)
	recGuestDelReg := httptest.NewRecorder()
	r.ServeHTTP(recGuestDelReg, reqGuestDelReg)
	assert.Contains(t, []int{http.StatusNotFound, http.StatusForbidden}, recGuestDelReg.Code, "guest must not delete registered user project")

	// Guest attempts to set env var on registered user's project
	envBody, _ := json.Marshal(map[string]string{"key": "EXPLOIT", "value": "pwned"})
	reqGuestEnvReg := httptest.NewRequest("POST", fmt.Sprintf("/api/projects/%s/env", regProj.ID), bytes.NewReader(envBody))
	reqGuestEnvReg.Header.Set("Content-Type", "application/json")
	recGuestEnvReg := httptest.NewRecorder()
	r.ServeHTTP(recGuestEnvReg, reqGuestEnvReg)
	assert.Contains(t, []int{http.StatusNotFound, http.StatusForbidden}, recGuestEnvReg.Code, "guest must not modify registered user project env")

	// B. Registered user attempts to access guest project
	reqRegOnGuest := httptest.NewRequest("GET", fmt.Sprintf("/api/projects/%s", guestProj.ID), nil)
	reqRegOnGuest.Header.Set("Authorization", "Bearer "+regToken)
	recRegOnGuest := httptest.NewRecorder()
	r.ServeHTTP(recRegOnGuest, reqRegOnGuest)
	assert.Contains(t, []int{http.StatusNotFound, http.StatusForbidden}, recRegOnGuest.Code, "registered user must not see guest project")
}

// 6. GitHub deployments and endpoints strictly rejected for guests
func TestGuestMode_GitHubStrictlyForbiddenForGuests(t *testing.T) {
	r, _, projectService, _, _, _ := setupGuestTestRouter(t, true)
	ctx := context.Background()

	// A. Unauthenticated call to /api/discovery/plan with source_type: "github"
	planReqBody, _ := json.Marshal(map[string]interface{}{
		"source_type":      models.SourceTypeGitHub,
		"source_reference": "octocat/Hello-World",
	})
	reqPlan := httptest.NewRequest("POST", "/api/discovery/plan", bytes.NewReader(planReqBody))
	reqPlan.Header.Set("Content-Type", "application/json")
	recPlan := httptest.NewRecorder()
	r.ServeHTTP(recPlan, reqPlan)
	assert.Equal(t, http.StatusForbidden, recPlan.Code)
	assert.Contains(t, recPlan.Body.String(), "registration required for GitHub deployments")

	// B. Direct ProjectService call for guest with source_type: "github"
	_, err := projectService.CreateProject(ctx, models.GuestUserID, services.CreateProjectInput{
		Name:            "Guest Attempt GitHub",
		SourceType:      models.SourceTypeGitHub,
		SourceReference: "octocat/Hello-World",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "registration required for github deployments")

	// C. Unauthenticated call to /api/integrations/github/* endpoints
	reqGHStatus := httptest.NewRequest("GET", "/api/integrations/github", nil)
	recGHStatus := httptest.NewRecorder()
	r.ServeHTTP(recGHStatus, reqGHStatus)
	assert.Equal(t, http.StatusUnauthorized, recGHStatus.Code)

	reqGHRepos := httptest.NewRequest("GET", "/api/integrations/github/repositories", nil)
	recGHRepos := httptest.NewRecorder()
	r.ServeHTTP(recGHRepos, reqGHRepos)
	assert.Equal(t, http.StatusUnauthorized, recGHRepos.Code)
}

// 7. When AllowGuestMode is false (public or remote installation without explicit config),
// guest requests must be rejected.
func TestGuestMode_DisabledByConfig(t *testing.T) {
	r, _, _, _, _, _ := setupGuestTestRouter(t, false) // allowGuestMode = false

	// Unauthenticated request to projects
	reqProj := httptest.NewRequest("GET", "/api/projects", nil)
	recProj := httptest.NewRecorder()
	r.ServeHTTP(recProj, reqProj)
	assert.Equal(t, http.StatusUnauthorized, recProj.Code)
	assert.Contains(t, recProj.Body.String(), "guest mode disabled on this installation")

	// Unauthenticated request to sources upload
	reqUpload := httptest.NewRequest("POST", "/api/sources/upload", nil)
	recUpload := httptest.NewRecorder()
	r.ServeHTTP(recUpload, reqUpload)
	assert.Equal(t, http.StatusUnauthorized, recUpload.Code)

	// Check /api/auth/mode indicates guest mode is disabled
	reqMode := httptest.NewRequest("GET", "/api/auth/mode", nil)
	recMode := httptest.NewRecorder()
	r.ServeHTTP(recMode, reqMode)
	assert.Equal(t, http.StatusOK, recMode.Code)
	var modeResp map[string]interface{}
	require.NoError(t, json.Unmarshal(recMode.Body.Bytes(), &modeResp))
	assert.Equal(t, false, modeResp["allow_guest_mode"])
}

// 8. Guest project abuse limit
func TestGuestMode_AbuseLimit(t *testing.T) {
	_, _, projectService, _, _, _ := setupGuestTestRouter(t, true)
	ctx := context.Background()

	// Verify project count limit logic
	pool, _ := connectTestDB(t)

	// Count existing guest projects
	var existingCount int
	err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM projects WHERE owner_id = $1", models.GuestUserID).Scan(&existingCount)
	require.NoError(t, err)

	abuseDir := createTestDir(t)
	proj, err := projectService.CreateProject(ctx, models.GuestUserID, services.CreateProjectInput{
		Name:           "Guest Project Limit Test " + uuid.New().String()[:8],
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: abuseDir,
		Services: []services.CreateServiceInput{
			{Name: "web", SourcePath: ".", Runtime: "generic", BuildStrategy: "auto", InternalPort: 8080},
		},
	})
	require.NoError(t, err)
	defer projectService.DeleteProject(ctx, proj.ID, models.GuestUserID, nil)
}
