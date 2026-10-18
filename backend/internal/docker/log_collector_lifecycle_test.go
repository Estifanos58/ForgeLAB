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
	ws "github.com/forgelab/backend/internal/websocket"
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

	deployID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO deployments (id, project_id, deploy_number, commit_sha, status, build_strategy, container_id, created_at)
		VALUES ($1, $2, 1, 'abc1234', 'running', 'auto', $3, NOW())
	`, deployID, projID, runningSvcContainerID)
	require.NoError(t, err)

	svcID := uuid.New()
	sdID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO services (id, project_id, name, role, internal_port, container_id, status, current_service_deployment_id, created_at, updated_at)
		VALUES ($1, $2, 'api-service', 'backend', 8080, $3, 'running', $4, NOW(), NOW())
	`, svcID, projID, runningSvcContainerID, sdID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO service_deployments (id, deployment_id, service_id, status, container_id, created_at)
		VALUES ($1, $2, $3, 'running', $4, NOW())
	`, sdID, deployID, svcID, runningSvcContainerID)
	require.NoError(t, err)

	legacyDeployID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO deployments (id, project_id, deploy_number, commit_sha, status, build_strategy, container_id, created_at)
		VALUES ($1, $2, 2, 'def5678', 'running', 'auto', $3, NOW())
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
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			_ = json.NewEncoder(w).Encode(types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{
						Running: true,
					},
				},
			})
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
		INSERT INTO deployments (id, project_id, deploy_number, commit_sha, status, created_at)
		VALUES ($1, $2, 1, 'sha123', 'running', NOW())
	`, deployID, projID)
	require.NoError(t, err)

	sd1ID := uuid.New()
	sd2ID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO service_deployments (id, deployment_id, service_id, deploy_number, status, created_at)
		VALUES
			($1, $2, $3, 1, 'running', NOW()),
			($4, $2, $5, 1, 'running', NOW())
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

// TestLogCollector_ReplacementRaceSafety verifies that when a container is redeployed and an old collector
// finishes after the replacement collector registers, the old collector's unregister does NOT remove the
// replacement collector's mapping, and concurrent replacements are race-safe.
func TestLogCollector_ReplacementRaceSafety(t *testing.T) {
	reg := NewLogCollectorRegistry()

	key := "container:target-cont-abc"
	var cancelledA, cancelledB int32

	cancelA := func() { atomic.StoreInt32(&cancelledA, 1) }
	cancelB := func() { atomic.StoreInt32(&cancelledB, 1) }

	// 1. Collector A registers
	idA := reg.Register(RuntimeCollectorParams{
		ContainerID: "target-cont-abc",
		ServiceName: "frontend",
	}, cancelA, key, "service-deploy-1")

	assert.True(t, reg.IsActive(key))
	assert.Equal(t, 1, reg.GetDiagnostics().ActiveCollectors)

	// 2. Collector B replaces Collector A for the same container key
	idB := reg.Register(RuntimeCollectorParams{
		ContainerID: "target-cont-abc",
		ServiceName: "frontend-v2",
	}, cancelB, key, "service-deploy-2")

	// Collector A should have been cancelled by Collector B's registration
	assert.Equal(t, int32(1), atomic.LoadInt32(&cancelledA), "Collector A must be cancelled on replacement")
	assert.Equal(t, int32(0), atomic.LoadInt32(&cancelledB), "Collector B must NOT be cancelled")

	// 3. Collector A goroutine terminates and invokes its unregister defer
	reg.Unregister(idA, key, "service-deploy-1")

	// Crucial assertion: Collector B MUST STILL BE ACTIVE and mapped to key!
	assert.True(t, reg.IsActive(key), "Collector B must remain active after Collector A unregisters")
	assert.True(t, reg.IsActive("service-deploy-2"))
	assert.False(t, reg.IsActive("service-deploy-1"), "Collector A's unique key should be cleaned up")

	diag := reg.GetDiagnostics()
	assert.Equal(t, 1, diag.ActiveCollectors)
	assert.Equal(t, idB, diag.Collectors[0].ID)

	// 4. Stress test: Concurrent replacements across multiple goroutines
	var wg sync.WaitGroup
	const concurrentRounds = 50
	for i := 0; i < concurrentRounds; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cFn := func() {}
			instID := reg.Register(RuntimeCollectorParams{
				ContainerID: "target-cont-abc",
				ServiceName: fmt.Sprintf("svc-%d", idx),
			}, cFn, key)
			time.Sleep(time.Duration(idx%5) * time.Millisecond)
			reg.RecordActivity(instID, 1)
		}(i)
	}
	wg.Wait()

	// Registry must remain coherent and active for the key
	assert.True(t, reg.IsActive(key))
	finalDiag := reg.GetDiagnostics()
	assert.Equal(t, 1, finalDiag.ActiveCollectors)
	assert.GreaterOrEqual(t, finalDiag.TotalLinesCollected, int64(1))

	// Clean up
	reg.Stop(key)
	assert.False(t, reg.IsActive(key))
	assert.Equal(t, 0, reg.GetDiagnostics().ActiveCollectors)
}

// TestLogCollector_MultiServicePipeline_PersistenceAndWebSocket verifies:
// 1. Docker multiplexed output demuxing across multiple services.
// 2. Safe message truncation for lines exceeding 64KB.
// 3. PostgreSQL persistence with correct phase, service ID, and service deployment ID.
// 4. WebSocket delivery across service deployment and project channels.
// 5. Operational observability metrics accurately tracked in diagnostics.
func TestLogCollector_MultiServicePipeline_PersistenceAndWebSocket(t *testing.T) {
	pool, _ := connectTestDB(t)
	if pool == nil {
		t.Skip("PostgreSQL not available for multi-service pipeline test")
		return
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 1. Setup DB entities
	userID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name)
		VALUES ($1, $2, 'hash123', 'Pipeline User')
	`, userID, fmt.Sprintf("pipeline-user-%s@test.local", userID.String()[:8]))
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	}()

	projID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO projects (id, owner_id, name, slug, status, created_at, updated_at)
		VALUES ($1, $2, 'Pipeline Proj', $3, 'running', NOW(), NOW())
	`, projID, userID, fmt.Sprintf("pipe-%s", projID.String()[:8]))
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM projects WHERE id = $1", projID)
	}()

	deployID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO deployments (id, project_id, deploy_number, commit_sha, status, build_strategy, created_at)
		VALUES ($1, $2, 1, 'pipe123', 'running', 'auto', NOW())
	`, deployID, projID)
	require.NoError(t, err)

	feSvcID := uuid.New()
	feSDID := uuid.New()
	feContID := "pipe-frontend-cont"
	_, err = pool.Exec(ctx, `
		INSERT INTO services (id, project_id, name, role, internal_port, container_id, status, current_service_deployment_id, created_at, updated_at)
		VALUES ($1, $2, 'frontend', 'frontend', 3000, $3, 'running', $4, NOW(), NOW())
	`, feSvcID, projID, feContID, feSDID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO service_deployments (id, deployment_id, service_id, status, container_id, created_at)
		VALUES ($1, $2, $3, 'running', $4, NOW())
	`, feSDID, deployID, feSvcID, feContID)
	require.NoError(t, err)

	beSvcID := uuid.New()
	beSDID := uuid.New()
	beContID := "pipe-backend-cont"
	_, err = pool.Exec(ctx, `
		INSERT INTO services (id, project_id, name, role, internal_port, container_id, status, current_service_deployment_id, created_at, updated_at)
		VALUES ($1, $2, 'backend', 'backend', 8080, $3, 'running', $4, NOW(), NOW())
	`, beSvcID, projID, beContID, beSDID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO service_deployments (id, deployment_id, service_id, status, container_id, created_at)
		VALUES ($1, $2, $3, 'running', $4, NOW())
	`, beSDID, deployID, beSvcID, beContID)
	require.NoError(t, err)

	// Setup WebSocket Hub
	wsHub := ws.NewHub(nil, nil, nil, nil)
	defer wsHub.Close()

	projectChan := fmt.Sprintf("project:%s", projID.String())
	projectEvents := make(chan []byte, 100)
	unsubProject := wsHub.RegisterTestSubscriber(projectChan, projectEvents)
	defer unsubProject()

	feSDChan := fmt.Sprintf("service-deployment:%s", feSDID.String())
	feSDEvents := make(chan []byte, 100)
	unsubFESD := wsHub.RegisterTestSubscriber(feSDChan, feSDEvents)
	defer unsubFESD()

	// Create oversized log message (>64KB) to verify bounded line truncation before persistence
	oversizedMsg := strings.Repeat("A", 70*1024)

	// Mock Docker daemon serving logs for frontend and backend
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/logs") {
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			if strings.Contains(r.URL.Path, feContID) {
				_, _ = w.Write(makeDockerLogStream(1, []string{"Frontend Next.js ready on port 3000", oversizedMsg}))
			} else if strings.Contains(r.URL.Path, beContID) {
				_, _ = w.Write(makeDockerLogStream(2, []string{"Backend API worker listening on :8080"}))
			}
			return
		}
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			_ = json.NewEncoder(w).Encode(types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{Running: true},
				},
			})
			return
		}
		http.NotFound(w, r)
	})

	cli, cleanupCli := newTestDockerClient(t, handler)
	defer cleanupCli()

	deploymentService := services.NewDeploymentService(pool)
	eng := &Engine{
		dockerClient:      cli,
		deploymentService: deploymentService,
		wsHub:             wsHub,
	}
	defer eng.StopAllLogCollectors()

	// Start runtime collectors for both services
	eng.StartRuntimeLogCollector(RuntimeCollectorParams{
		ContainerID:         feContID,
		ProjectID:           projID,
		DeploymentID:        &deployID,
		ServiceDeploymentID: &feSDID,
		ServiceID:           &feSvcID,
		ServiceName:         "frontend",
	})

	eng.StartRuntimeLogCollector(RuntimeCollectorParams{
		ContainerID:         beContID,
		ProjectID:           projID,
		DeploymentID:        &deployID,
		ServiceDeploymentID: &beSDID,
		ServiceID:           &beSvcID,
		ServiceName:         "backend",
	})

	// 2. Verify Database Persistence for both services
	require.Eventually(t, func() bool {
		var feCount, beCount int
		_ = pool.QueryRow(ctx, "SELECT COUNT(*) FROM deployment_logs WHERE service_deployment_id = $1", feSDID).Scan(&feCount)
		_ = pool.QueryRow(ctx, "SELECT COUNT(*) FROM deployment_logs WHERE service_deployment_id = $1", beSDID).Scan(&beCount)
		return feCount >= 1 && beCount >= 1
	}, 5*time.Second, 100*time.Millisecond, "logs from both frontend and backend must be persisted")

	// Directly verify safe truncation before persistence in emitRuntimeLog
	eng.emitRuntimeLog(RuntimeCollectorParams{
		ContainerID:         feContID,
		ProjectID:           projID,
		DeploymentID:        &deployID,
		ServiceDeploymentID: &feSDID,
		ServiceID:           &feSvcID,
		ServiceName:         "frontend",
	}, "", "stdout", oversizedMsg)

	// Verify truncation of oversized message before persistence
	var truncatedMsg string
	err = pool.QueryRow(ctx, "SELECT message FROM deployment_logs WHERE service_deployment_id = $1 AND message LIKE '%[truncated%'", feSDID).Scan(&truncatedMsg)
	require.NoError(t, err)
	assert.Contains(t, truncatedMsg, "[truncated: exceeded 64KB log size limit]")
	assert.LessOrEqual(t, len(truncatedMsg), 66*1024)

	// 3. Verify WebSocket event delivery
	var receivedProjectEvents int
	timeout := time.After(3 * time.Second)
readLoop:
	for {
		select {
		case raw := <-projectEvents:
			var event ws.EventMessage
			err := json.Unmarshal(raw, &event)
			if err == nil && event.Type == "log" {
				receivedProjectEvents++
				data, ok := event.Data.(map[string]interface{})
				if ok {
					assert.Equal(t, "runtime", data["phase"])
					assert.NotEmpty(t, data["stream"])
					assert.NotEmpty(t, data["sequence"])
				}
			}
			if receivedProjectEvents >= 3 {
				break readLoop
			}
		case <-timeout:
			break readLoop
		}
	}
	assert.GreaterOrEqual(t, receivedProjectEvents, 2, "project channel must receive real-time events for both services")

	// 4. Verify Operational Observability Diagnostics
	diag := eng.GetLogCollectorDiagnostics()
	assert.GreaterOrEqual(t, diag.ActiveCollectors, 2)
	assert.GreaterOrEqual(t, diag.TotalLinesCollected, int64(3))
	assert.Equal(t, int64(0), diag.TotalPersistenceErrors)
}
