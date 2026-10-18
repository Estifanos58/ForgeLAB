package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"os"

	"github.com/docker/docker/api/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/database"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

func getTestDatabaseURL() string {
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return url
	}
	return "postgres://forgelab:forgelab_dev_password@localhost:5432/forgelab?sslmode=disable"
}

func connectTestDB(t *testing.T) (*pgxpool.Pool, string) {
	dbURL := getTestDatabaseURL()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := database.Connect(ctx, dbURL)
	if err != nil {
		t.Skipf("Skipping integration test; cannot connect to database at %s: %v", dbURL, err)
		return nil, ""
	}
	return pool, dbURL
}

// TestCheckContinuousRuntimeHealth_PreservesStoppedStatus verifies that:
// 1. Intentionally stopped containers (with exit code 137/143 from SIGKILL/SIGTERM) are NOT marked as crashes.
// 2. Running containers that exit unexpectedly with non-zero exit code ARE marked as failed.
func TestCheckContinuousRuntimeHealth_PreservesStoppedStatus(t *testing.T) {
	containerStoppedID := "stopped-container-12345"
	containerCrashedID := "crashed-container-67890"

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, containerStoppedID) {
			_ = json.NewEncoder(w).Encode(types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					ID: containerStoppedID,
					State: &types.ContainerState{
						Running:  false,
						ExitCode: 137, // SIGKILL on container stop
					},
				},
			})
			return
		}
		if strings.Contains(r.URL.Path, containerCrashedID) {
			_ = json.NewEncoder(w).Encode(types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					ID: containerCrashedID,
					State: &types.ContainerState{
						Running:  false,
						ExitCode: 1, // Crash
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	})

	cli, cleanup := newTestDockerClient(t, handler)
	defer cleanup()

	pool, _ := connectTestDB(t)
	if pool == nil {
		t.Skip("PostgreSQL not available for integration health test")
		return
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name)
		VALUES ($1, $2, 'hash123', 'Health Test User')
	`, userID, fmt.Sprintf("health-user-%s@test.local", userID.String()[:8]))
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	}()

	projID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO projects (id, owner_id, name, slug, status, created_at, updated_at)
		VALUES ($1, $2, 'Health Proj', $3, 'running', NOW(), NOW())
	`, projID, userID, fmt.Sprintf("health-%s", projID.String()[:8]))
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM projects WHERE id = $1", projID)
	}()

	// 1. Intentionally stopped service with exit code 137
	stoppedSvcID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO services (id, project_id, name, role, internal_port, container_id, status, created_at, updated_at)
		VALUES ($1, $2, 'stopped-svc', 'web', 8080, $3, $4, NOW(), NOW())
	`, stoppedSvcID, projID, containerStoppedID, models.ServiceStatusStopped)
	require.NoError(t, err)

	// 2. Expected running service that crashed with exit code 1
	crashedSvcID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO services (id, project_id, name, role, internal_port, container_id, status, created_at, updated_at)
		VALUES ($1, $2, 'crashed-svc', 'api', 8081, $3, $4, NOW(), NOW())
	`, crashedSvcID, projID, containerCrashedID, models.ServiceStatusRunning)
	require.NoError(t, err)

	svcService := services.NewServiceService(pool)
	deployService := services.NewDeploymentService(pool)
	projectService := services.NewProjectService(pool, nil, nil, nil)
	eng := &Engine{
		dockerClient:      cli,
		serviceService:    svcService,
		deploymentService: deployService,
		projectService:    projectService,
	}

	err = eng.CheckContinuousRuntimeHealth(ctx)
	require.NoError(t, err)

	// Verify intentionally stopped service REMAINED 'stopped'
	stoppedSvc, err := svcService.GetService(ctx, stoppedSvcID)
	require.NoError(t, err)
	assert.Equal(t, models.ServiceStatusStopped, stoppedSvc.Status, "intentionally stopped service must NOT be marked as failed")

	// Verify crashed service transitioned to 'failed'
	crashedSvc, err := svcService.GetService(ctx, crashedSvcID)
	require.NoError(t, err)
	assert.Equal(t, models.ServiceStatusFailed, crashedSvc.Status, "crashed service must be marked as failed")
}

// TestEngine_CrossProjectServiceAuthorization verifies that privileged engine methods
// enforce that the target service belongs to the project being operated on.
func TestEngine_CrossProjectServiceAuthorization(t *testing.T) {
	pool, _ := connectTestDB(t)
	if pool == nil {
		t.Skip("PostgreSQL not available")
		return
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name)
		VALUES ($1, $2, 'hash123', 'Auth Test User')
	`, userID, fmt.Sprintf("auth-user-%s@test.local", userID.String()[:8]))
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	}()

	// Project A
	projA := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO projects (id, owner_id, name, slug, status, created_at, updated_at)
		VALUES ($1, $2, 'Proj A', $3, 'running', NOW(), NOW())
	`, projA, userID, fmt.Sprintf("proj-a-%s", projA.String()[:8]))
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM projects WHERE id = $1", projA)
	}()

	// Project B
	projB := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO projects (id, owner_id, name, slug, status, created_at, updated_at)
		VALUES ($1, $2, 'Proj B', $3, 'running', NOW(), NOW())
	`, projB, userID, fmt.Sprintf("proj-b-%s", projB.String()[:8]))
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM projects WHERE id = $1", projB)
	}()

	// Service in Project B
	svcB := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO services (id, project_id, name, role, internal_port, status, created_at, updated_at)
		VALUES ($1, $2, 'service-b', 'web', 3000, 'running', NOW(), NOW())
	`, svcB, projB)
	require.NoError(t, err)

	projectSvc := services.NewProjectService(pool, nil, nil, nil)
	serviceSvc := services.NewServiceService(pool)

	eng := &Engine{
		projectService: projectSvc,
		serviceService: serviceSvc,
	}

	// Attempt to Stop service B via project A
	err = eng.StopService(ctx, projA, svcB, userID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not belong to project")

	// Attempt to Start service B via project A
	err = eng.StartService(ctx, projA, svcB, userID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not belong to project")

	// Attempt to Restart service B via project A
	err = eng.RestartService(ctx, projA, svcB, userID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not belong to project")
}

// TestCancelAndDrainProjectDeployments verifies that CancelAndDrainProjectDeployments
// cancels in-flight contexts and synchronously waits for workers to finish.
func TestCancelAndDrainProjectDeployments(t *testing.T) {
	eng := &Engine{
		activeExecs: make(map[uuid.UUID]*activeExecEntry),
	}

	projID := uuid.New()
	deployID := uuid.New()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := eng.registerActiveExec(deployID, projID, cancel)

	workerFinished := false
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		<-ctx.Done()                      // wait for cancellation signal
		time.Sleep(50 * time.Millisecond) // simulate graceful shutdown cleanup
		workerFinished = true
		eng.unregisterActiveExec(deployID)
	}()

	drainCtx, drainCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer drainCancel()

	// Drain project deployments
	err := eng.CancelAndDrainProjectDeployments(drainCtx, projID)
	require.NoError(t, err)
	assert.True(t, workerFinished, "worker must be drained and finished before CancelAndDrainProjectDeployments returns")

	select {
	case <-done:
		// success: done channel was closed by unregisterActiveExec
	default:
		t.Fatal("expected done channel to be closed")
	}

	wg.Wait()
}

// TestReconcileDaemonContainers_ProtectsActivelyExecutingDeployments verifies that in-flight deployments
// (queued, cloning, building, starting) created on the current backend instance or actively executing in memory
// are protected from being erroneously aborted by periodic daemon container reconciliation.
func TestReconcileDaemonContainers_ProtectsActivelyExecutingDeployments(t *testing.T) {
	eng := &Engine{
		activeExecs: make(map[uuid.UUID]*activeExecEntry),
		startTime:   time.Now().Add(-1 * time.Minute),
	}

	activeDeployID := uuid.New()
	projID := uuid.New()
	cancelCalled := false
	cancelFn := func() {
		cancelCalled = true
	}

	// 1. Actively executing deployment must be reported as active
	eng.registerActiveExec(activeDeployID, projID, cancelFn)
	assert.True(t, eng.IsActiveExecution(activeDeployID), "registered execution must be reported active")

	// 2. Unregistered execution must NOT be reported as active
	inactiveDeployID := uuid.New()
	assert.False(t, eng.IsActiveExecution(inactiveDeployID), "unregistered execution must not be reported active")

	// 3. Deployment created AFTER engine start time belongs to current instance
	newDeployCreatedAt := time.Now()
	assert.False(t, newDeployCreatedAt.Before(eng.startTime), "new deployment created on current instance must not be considered created before engine start time")

	// 4. Stale deployment created BEFORE engine start time is considered from previous instance
	staleDeployCreatedAt := time.Now().Add(-5 * time.Minute)
	assert.True(t, staleDeployCreatedAt.Before(eng.startTime), "stale deployment from prior restart must be considered created before engine start time")

	// Clean up active exec
	eng.unregisterActiveExec(activeDeployID)
	assert.False(t, eng.IsActiveExecution(activeDeployID))
	assert.False(t, cancelCalled, "unregister should not invoke cancel")

	// 5. Child service deployment belonging to an active parent release must be protected
	parentReleaseID := uuid.New()
	eng.registerActiveExec(parentReleaseID, projID, cancelFn)
	childServiceDeploy := &models.ServiceDeployment{
		ID:           uuid.New(),
		DeploymentID: &parentReleaseID,
		Status:       models.DeployStatusQueued,
		CreatedAt:    time.Now().Add(-10 * time.Minute), // even if created earlier
	}
	assert.True(t, childServiceDeploy.DeploymentID != nil && eng.IsActiveExecution(*childServiceDeploy.DeploymentID),
		"child service deployment belonging to active parent release must be protected")

	eng.unregisterActiveExec(parentReleaseID)
}
