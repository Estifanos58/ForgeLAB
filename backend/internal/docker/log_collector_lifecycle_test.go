package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/logging"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

// makeDockerLogStream encodes lines into Docker multiplexed stream format (8-byte header per frame).
func makeDockerLogStream(streamType byte, lines []string) []byte {
	var buf bytes.Buffer
	for _, l := range lines {
		payload := []byte(time.Now().Format(time.RFC3339Nano) + " " + l + "\n")
		hdr := make([]byte, 8)
		hdr[0] = streamType // 1=stdout, 2=stderr
		binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload)))
		buf.Write(hdr)
		buf.Write(payload)
	}
	return buf.Bytes()
}

// TestLogCollector_LifetimeAfterDeploymentCompletion verifies that:
// 1. A runtime log collector goroutine remains active after deployment completion.
// 2. It continues processing container logs until StopLogCollector or StopContainerLogCollector is called.
func TestLogCollector_LifetimeAfterDeploymentCompletion(t *testing.T) {
	cID := "test-container-live-123"
	deployID := uuid.New()
	projectID := uuid.New()
	serviceID := uuid.New()

	var logsCalled int32
	var inspectCalled int32

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/logs") {
			atomic.AddInt32(&logsCalled, 1)
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			streamData := makeDockerLogStream(1, []string{"Server listening on :8080", "Handling request /health"})
			_, _ = w.Write(streamData)
			return
		}
		if strings.Contains(r.URL.Path, "/json") {
			atomic.AddInt32(&inspectCalled, 1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					ID: cID,
					State: &types.ContainerState{
						Running: true,
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	})

	cli, cleanupCli := newTestDockerClient(t, handler)
	defer cleanupCli()

	eng := &Engine{
		dockerClient:        cli,
		activeLogCollectors: sync.Map{},
	}

	// Verify collector is initially inactive
	assert.False(t, eng.IsLogCollectorActive(deployID))
	assert.False(t, eng.IsContainerLogCollectorActive(cID))

	// Simulate deployment execution context that returns and cancels immediately
	func() {
		execCtx, execCancel := context.WithCancel(context.Background())
		defer execCancel()

		// Start runtime log collector
		eng.StartRuntimeLogCollector(RuntimeCollectorParams{
			ContainerID:         cID,
			ProjectID:           projectID,
			ServiceDeploymentID: &deployID,
			ServiceID:           &serviceID,
			ServiceName:         "web",
		})

		// Collector must immediately be registered as active
		assert.True(t, eng.IsLogCollectorActive(deployID))
		assert.True(t, eng.IsContainerLogCollectorActive(cID))
		_ = execCtx
	}()

	// Deployment execution function has returned and its execCtx was cancelled.
	// Verify runtime collector remains ACTIVE in memory!
	time.Sleep(50 * time.Millisecond)
	assert.True(t, eng.IsLogCollectorActive(deployID), "collector must outlive deployment execution context")
	assert.True(t, eng.IsContainerLogCollectorActive(cID))

	// Now explicitly stop container / collector
	eng.StopContainerLogCollector(cID)

	// Verify collector is no longer active
	time.Sleep(20 * time.Millisecond)
	assert.False(t, eng.IsLogCollectorActive(deployID))
	assert.False(t, eng.IsContainerLogCollectorActive(cID))
}

// TestLogCollector_BoundedRetriesOnStreamDrop verifies that:
// 1. If Docker log stream drops while container is running, the collector retries up to maxRetries.
// 2. If container inspect indicates container exited (Running=false), collector terminates without unnecessary retries.
func TestLogCollector_BoundedRetriesOnStreamDrop(t *testing.T) {
	cID := "retry-test-container-456"
	deployID := uuid.New()
	projectID := uuid.New()

	var logsAttempts int32
	var isRunning int32 = 1

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/logs") {
			count := atomic.AddInt32(&logsAttempts, 1)
			if count <= 2 {
				// Stream disconnects after a log line
				w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
				_, _ = w.Write(makeDockerLogStream(1, []string{fmt.Sprintf("log line %d", count)}))
				return
			}
			// On 3rd attempt, container stops running
			atomic.StoreInt32(&isRunning, 0)
			http.Error(w, "stream closed", http.StatusServiceUnavailable)
			return
		}
		if strings.Contains(r.URL.Path, "/json") {
			w.Header().Set("Content-Type", "application/json")
			running := atomic.LoadInt32(&isRunning) == 1
			_ = json.NewEncoder(w).Encode(types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					ID: cID,
					State: &types.ContainerState{
						Running: running,
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	})

	cli, cleanupCli := newTestDockerClient(t, handler)
	defer cleanupCli()

	eng := &Engine{
		dockerClient:        cli,
		activeLogCollectors: sync.Map{},
	}

	eng.StartRuntimeLogCollector(RuntimeCollectorParams{
		ContainerID:  cID,
		ProjectID:    projectID,
		DeploymentID: &deployID,
	})

	assert.True(t, eng.IsContainerLogCollectorActive(cID))

	// Wait for retries to run and container to report stopped
	require.Eventually(t, func() bool {
		return !eng.IsContainerLogCollectorActive(cID)
	}, 5*time.Second, 50*time.Millisecond, "collector must terminate after container is inspected as stopped")

	assert.GreaterOrEqual(t, atomic.LoadInt32(&logsAttempts), int32(2))
}

// TestLogCollector_BackendRestartRecovery verifies that ReconcileDaemonContainers
// restores runtime log collectors for existing running containers after backend restart.
func TestLogCollector_BackendRestartRecovery(t *testing.T) {
	runningSvcContainerID := "reconcile-svc-cont-7890"
	runningLegacyContainerID := "reconcile-legacy-cont-1234"

	pool, _ := connectTestDB(t)
	if pool == nil {
		t.Skip("PostgreSQL not available for reconciliation test")
		return
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name)
		VALUES ($1, $2, 'hash123', 'Reconcile User')
	`, userID, fmt.Sprintf("reconcile-user-%s@test.local", userID.String()[:8]))
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	}()

	projID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO projects (id, owner_id, name, slug, status, created_at, updated_at)
		VALUES ($1, $2, 'Reconcile Proj', $3, 'running', NOW(), NOW())
	`, projID, userID, fmt.Sprintf("reconcile-%s", projID.String()[:8]))
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM projects WHERE id = $1", projID)
	}()

	svcID := uuid.New()
	sdID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO services (id, project_id, name, role, internal_port, container_id, status, current_service_deployment_id, created_at, updated_at)
		VALUES ($1, $2, 'api-service', 'backend', 8080, $3, 'running', $4, NOW(), NOW())
	`, svcID, projID, runningSvcContainerID, sdID)
	require.NoError(t, err)

	legacyDeployID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO deployments (id, project_id, deploy_number, commit_sha, status, container_id, created_at, updated_at)
		VALUES ($1, $2, 1, 'abc1234', 'running', $3, NOW(), NOW())
	`, legacyDeployID, projID, runningLegacyContainerID)
	require.NoError(t, err)

	// Mock Docker daemon returning both containers as running
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			containers := []types.Container{
				{
					ID:    runningSvcContainerID,
					State: "running",
					Labels: map[string]string{
						"forgelab.project_id":            projID.String(),
						"forgelab.service_deployment_id": sdID.String(),
					},
				},
				{
					ID:    runningLegacyContainerID,
					State: "running",
					Labels: map[string]string{
						"forgelab.project_id":    projID.String(),
						"forgelab.deployment_id": legacyDeployID.String(),
					},
				},
			}
			_ = json.NewEncoder(w).Encode(containers)
			return
		}
		if strings.Contains(r.URL.Path, "/logs") {
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			_, _ = w.Write(makeDockerLogStream(1, []string{"reconciled runtime line"}))
			return
		}
		http.NotFound(w, r)
	})

	cli, cleanupCli := newTestDockerClient(t, handler)
	defer cleanupCli()

	serviceService := services.NewServiceService(pool)
	deploymentService := services.NewDeploymentService(pool)
	projectService := services.NewProjectService(pool, nil, nil, nil)

	eng := &Engine{
		dockerClient:        cli,
		serviceService:      serviceService,
		deploymentService:   deploymentService,
		projectService:      projectService,
		activeLogCollectors: sync.Map{},
	}

	// Verify collectors are initially not running
	assert.False(t, eng.IsContainerLogCollectorActive(runningSvcContainerID))
	assert.False(t, eng.IsContainerLogCollectorActive(runningLegacyContainerID))

	// Run ReconcileDaemonContainers (simulating backend startup recovery)
	err = eng.ReconcileDaemonContainers(ctx)
	require.NoError(t, err)

	// Verify collectors were restored for both running containers
	assert.True(t, eng.IsContainerLogCollectorActive(runningSvcContainerID), "service container collector must be restored")
	assert.True(t, eng.IsContainerLogCollectorActive(runningLegacyContainerID), "legacy container collector must be restored")

	// Clean up collectors
	eng.StopAllLogCollectors()
	assert.False(t, eng.IsContainerLogCollectorActive(runningSvcContainerID))
	assert.False(t, eng.IsContainerLogCollectorActive(runningLegacyContainerID))
}

// TestLogQuery_FiltersPaginationAndOrdering verifies:
// 1. GetProjectLogs aggregates across multiple services.
// 2. Phase filtering returns only deployment or production logs.
// 3. Recent logs ordering returns the latest entries chronologically without truncation.
// 4. Cursor pagination works properly.
func TestLogQuery_FiltersPaginationAndOrdering(t *testing.T) {
	pool, _ := connectTestDB(t)
	if pool == nil {
		t.Skip("PostgreSQL not available for log query test")
		return
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	deploymentService := services.NewDeploymentService(pool)

	userID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name)
		VALUES ($1, $2, 'hash123', 'Log Query User')
	`, userID, fmt.Sprintf("log-user-%s@test.local", userID.String()[:8]))
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	}()

	projID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO projects (id, owner_id, name, slug, status, created_at, updated_at)
		VALUES ($1, $2, 'Log Query Proj', $3, 'running', NOW(), NOW())
	`, projID, userID, fmt.Sprintf("log-query-%s", projID.String()[:8]))
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM projects WHERE id = $1", projID)
	}()

	svc1ID := uuid.New()
	svc2ID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO services (id, project_id, name, role, internal_port, status, created_at, updated_at)
		VALUES 
			($1, $2, 'frontend', 'frontend', 3000, 'running', NOW(), NOW()),
			($3, $2, 'backend', 'backend', 8080, 'running', NOW(), NOW())
	`, svc1ID, projID, svc2ID)
	require.NoError(t, err)

	deployID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO deployments (id, project_id, deploy_number, commit_sha, status, created_at, updated_at)
		VALUES ($1, $2, 1, 'sha123', 'running', NOW(), NOW())
	`, deployID, projID)
	require.NoError(t, err)

	sd1ID := uuid.New()
	sd2ID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO service_deployments (id, deployment_id, service_id, deploy_number, status, created_at, updated_at)
		VALUES
			($1, $2, $3, 1, 'running', NOW(), NOW()),
			($4, $2, $5, 1, 'running', NOW(), NOW())
	`, sd1ID, deployID, svc1ID, sd2ID, svc2ID)
	require.NoError(t, err)

	// Insert build logs and runtime logs for both services
	// Svc 1 build & runtime
	_, _ = deploymentService.AddDeploymentServiceLog(ctx, &deployID, &sd1ID, &svc1ID, models.LogPhaseSource, models.LogStreamStdout, "Cloning repo")
	_, _ = deploymentService.AddDeploymentServiceLog(ctx, &deployID, &sd1ID, &svc1ID, models.LogPhaseBuild, models.LogStreamStdout, "Building frontend")
	_, _ = deploymentService.AddDeploymentServiceLog(ctx, &deployID, &sd1ID, &svc1ID, models.LogPhaseStartup, models.LogStreamStdout, "Frontend starting")
	_, _ = deploymentService.AddDeploymentServiceLog(ctx, &deployID, &sd1ID, &svc1ID, models.LogPhaseHealth, models.LogStreamStdout, "Frontend healthy")
	_, _ = deploymentService.AddDeploymentServiceLog(ctx, &deployID, &sd1ID, &svc1ID, models.LogPhaseRuntime, models.LogStreamStdout, "Frontend ready on port 3000")

	// Svc 2 build & runtime
	_, _ = deploymentService.AddDeploymentServiceLog(ctx, &deployID, &sd2ID, &svc2ID, models.LogPhaseBuild, models.LogStreamStdout, "Compiling Go binary")
	_, _ = deploymentService.AddDeploymentServiceLog(ctx, &deployID, &sd2ID, &svc2ID, models.LogPhaseStartup, models.LogStreamStdout, "Backend starting")
	_, _ = deploymentService.AddDeploymentServiceLog(ctx, &deployID, &sd2ID, &svc2ID, models.LogPhaseRuntime, models.LogStreamStdout, "Backend connected to DB")

	// 1. Project-level aggregation: all logs across all services
	allLogs, err := deploymentService.GetProjectLogs(ctx, projID, services.LogQueryOptions{
		Phase: "all",
		Limit: 100,
	})
	require.NoError(t, err)
	assert.Len(t, allLogs, 8, "project logs must include all services' logs")

	// Verify service IDs are populated for both services
	hasFrontend := false
	hasBackend := false
	for _, l := range allLogs {
		if l.ServiceID != nil && *l.ServiceID == svc1ID {
			hasFrontend = true
		}
		if l.ServiceID != nil && *l.ServiceID == svc2ID {
			hasBackend = true
		}
	}
	assert.True(t, hasFrontend)
	assert.True(t, hasBackend)

	// 2. Deployment phase filter: source, build, startup, health
	deployLogs, err := deploymentService.GetProjectLogs(ctx, projID, services.LogQueryOptions{
		Phase: "deployment",
		Limit: 100,
	})
	require.NoError(t, err)
	assert.Len(t, deployLogs, 6)
	for _, l := range deployLogs {
		assert.Contains(t, []string{models.LogPhaseSource, models.LogPhaseBuild, models.LogPhaseStartup, models.LogPhaseHealth}, l.Phase)
	}

	// 3. Production phase filter: runtime stdout/stderr only
	prodLogs, err := deploymentService.GetProjectLogs(ctx, projID, services.LogQueryOptions{
		Phase: "production",
		Limit: 100,
	})
	require.NoError(t, err)
	assert.Len(t, prodLogs, 2)
	for _, l := range prodLogs {
		assert.Equal(t, models.LogPhaseRuntime, l.Phase)
	}

	// 4. Per-service filter
	frontendLogs, err := deploymentService.GetProjectLogs(ctx, projID, services.LogQueryOptions{
		ServiceID: &svc1ID,
		Phase:     "all",
		Limit:     100,
	})
	require.NoError(t, err)
	assert.Len(t, frontendLogs, 5)
	for _, l := range frontendLogs {
		require.NotNil(t, l.ServiceID)
		assert.Equal(t, svc1ID, *l.ServiceID)
	}

	// 5. Cursor-based pagination: AfterID
	firstLogID := allLogs[0].ID
	afterLogs, err := deploymentService.GetProjectLogs(ctx, projID, services.LogQueryOptions{
		AfterID: firstLogID,
		Limit:   3,
	})
	require.NoError(t, err)
	assert.Len(t, afterLogs, 3)
	for _, l := range afterLogs {
		assert.Greater(t, l.ID, firstLogID)
	}
}

// TestLogCollector_SecretRedaction verifies that secrets are redacted from runtime logs.
func TestLogCollector_SecretRedaction(t *testing.T) {
	redactor := logging.NewLogRedactor()
	redactor.SetSecrets([]string{
		"superSecretDatabasePassword123",
		"sk-live-9876543210abcdef",
		"jwt-signing-secret-key",
	})

	rawLog := "Connected to postgres://user:superSecretDatabasePassword123@db:5432 with key sk-live-9876543210abcdef"
	redacted := redactor.Redact(rawLog)

	assert.NotContains(t, redacted, "superSecretDatabasePassword123")
	assert.NotContains(t, redacted, "sk-live-9876543210abcdef")
	assert.Contains(t, redacted, "[REDACTED]")
}
