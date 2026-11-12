package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/proxy"
	"github.com/forgelab/backend/internal/services"
)

type mockRollbackEnqueuer struct {
	mu       sync.Mutex
	enqueued []uuid.UUID
	err      error
}

func (m *mockRollbackEnqueuer) EnqueueServiceDeployment(ctx context.Context, serviceDeploymentID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.enqueued = append(m.enqueued, serviceDeploymentID)
	return nil
}

func (m *mockRollbackEnqueuer) EnqueuedIDs() []uuid.UUID {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]uuid.UUID, len(m.enqueued))
	copy(out, m.enqueued)
	return out
}

func TestAppControl_MultiServiceRestartStartStop_HandlesJobs(t *testing.T) {
	ctx := context.Background()
	ownerID := uuid.New()
	projectID := uuid.New()
	deployID := uuid.New()

	feSvcID := uuid.New()
	apiSvcID := uuid.New()
	dbSvcID := uuid.New()
	jobSvcID := uuid.New()

	var mu sync.Mutex
	restarted := map[string]int{}
	started := map[string]int{}
	stopped := map[string]int{}
	failRestartFor := ""

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/restart"):
			id := extractContainerID(r.URL.Path, "/restart")
			if failRestartFor != "" && id == failRestartFor {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"restart failed"}`))
				return
			}
			mu.Lock()
			restarted[id]++
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/start"):
			id := extractContainerID(r.URL.Path, "/start")
			mu.Lock()
			started[id]++
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/stop"):
			id := extractContainerID(r.URL.Path, "/stop")
			mu.Lock()
			stopped[id]++
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/logs"):
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	cli, cleanup := newTestDockerClient(t, handler)
	defer cleanup()

	svcStore := []*models.Service{
		{
			ID:             feSvcID,
			ProjectID:      projectID,
			Name:           "frontend",
			Classification: models.ClassificationApplication,
			Status:         models.ServiceStatusDegraded,
			ContainerID:    strPtr("c-fe"),
		},
		{
			ID:             apiSvcID,
			ProjectID:      projectID,
			Name:           "api",
			Classification: models.ClassificationApplication,
			Status:         models.ServiceStatusStopped,
			ContainerID:    strPtr("c-api"),
		},
		{
			ID:             dbSvcID,
			ProjectID:      projectID,
			Name:           "postgres",
			Classification: models.ClassificationInfrastructure,
			Status:         models.ServiceStatusRunning,
			ContainerID:    strPtr("c-db"),
		},
		{
			ID:             jobSvcID,
			ProjectID:      projectID,
			Name:           "migrate",
			Classification: models.ClassificationJob,
			Status:         models.ServiceStatusStopped,
			ContainerID:    strPtr("c-job"),
		},
	}

	project := &models.Project{
		ID:                  projectID,
		OwnerID:             ownerID,
		Name:                "multi-svc",
		Status:              models.ProjectStatusDegraded,
		CurrentDeploymentID: &deployID,
	}
	deployment := &models.Deployment{
		ID:        deployID,
		ProjectID: projectID,
		Status:    models.DeployStatusRunning,
		// Legacy release deployment has no top-level ContainerID in multi-service mode
		ContainerID: nil,
	}

	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListServicesForTest(func(ctx context.Context, pID uuid.UUID) ([]*models.Service, error) {
		mu.Lock()
		defer mu.Unlock()
		out := make([]*models.Service, 0, len(svcStore))
		for _, s := range svcStore {
			if s.ProjectID == pID {
				cp := *s
				out = append(out, &cp)
			}
		}
		return out, nil
	})
	svcSvc.SetUpdateServiceStatusForTest(func(ctx context.Context, serviceID uuid.UUID, status string, containerID, imageTag *string, hostPort *int) error {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range svcStore {
			if s.ID == serviceID {
				s.Status = status
			}
		}
		return nil
	})

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetServiceService(svcSvc)
	projSvc.SetProjectGetterForTest(func(ctx context.Context, pID, oID uuid.UUID) (*models.Project, error) {
		if pID != projectID || oID != ownerID {
			return nil, services.ErrProjectNotFound
		}
		cp := *project
		return &cp, nil
	})
	projSvc.SetUpdateProjectStatusForTest(func(ctx context.Context, pID uuid.UUID, status string) error {
		mu.Lock()
		defer mu.Unlock()
		project.Status = status
		return nil
	})

	depSvc := services.NewDeploymentService(nil)
	depSvc.SetGetDeploymentForTest(func(ctx context.Context, dID uuid.UUID) (*models.Deployment, error) {
		cp := *deployment
		return &cp, nil
	})
	depSvc.SetUpdateDeploymentStatusForTest(func(ctx context.Context, dID uuid.UUID, newStatus string, reason *string) error {
		deployment.Status = newStatus
		return nil
	})
	depSvc.SetCurrentDeploymentForTest(func(ctx context.Context, pID, dID uuid.UUID, projStatus string) error {
		project.Status = projStatus
		return nil
	})

	eng := NewEngine(cli, projSvc, depSvc, nil, nil, nil, nil, nil, nil, t.TempDir())
	eng.SetServiceService(svcSvc)
	defer eng.StopAllLogCollectors()

	// 1. Project-wide RestartApp must restart all non-job containers and skip job workloads.
	require.NoError(t, eng.RestartApp(ctx, projectID, ownerID))

	mu.Lock()
	assert.Equal(t, 1, restarted["c-fe"])
	assert.Equal(t, 1, restarted["c-api"])
	assert.Equal(t, 1, restarted["c-db"])
	assert.Equal(t, 0, restarted["c-job"], "job container must NOT be restarted by RestartApp")
	mu.Unlock()
	assert.Equal(t, models.ProjectStatusRunning, project.Status)
	assert.Equal(t, models.ServiceStatusRunning, svcStore[0].Status)
	assert.Equal(t, models.ServiceStatusRunning, svcStore[1].Status)
	assert.Equal(t, models.ServiceStatusRunning, svcStore[2].Status)
	assert.Equal(t, models.ServiceStatusStopped, svcStore[3].Status, "job status must remain stopped")

	// 2. StopApp must stop running non-job services and skip already-stopped job container.
	require.NoError(t, eng.StopApp(ctx, projectID, ownerID))
	mu.Lock()
	assert.Equal(t, 1, stopped["c-fe"])
	assert.Equal(t, 1, stopped["c-api"])
	assert.Equal(t, 1, stopped["c-db"])
	assert.Equal(t, 0, stopped["c-job"], "already-stopped job container must be skipped during StopApp")
	mu.Unlock()
	assert.Equal(t, models.ProjectStatusStopped, project.Status)

	// 3. StartApp must start non-job containers only and skip job workload.
	require.NoError(t, eng.StartApp(ctx, projectID, ownerID))
	mu.Lock()
	assert.Equal(t, 1, started["c-fe"])
	assert.Equal(t, 1, started["c-api"])
	assert.Equal(t, 1, started["c-db"])
	assert.Equal(t, 0, started["c-job"], "job container must NOT be started by StartApp")
	mu.Unlock()
	assert.Equal(t, models.ProjectStatusRunning, project.Status)

	// 4. RestartApp must propagate Docker restart failures cleanly.
	failRestartFor = "c-api"
	err := eng.RestartApp(ctx, projectID, ownerID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api")
	assert.Contains(t, err.Error(), "restart failed")
}

func TestRuntimeHealth_UnhealthyDegradesRecoversAndAutoRollbacks(t *testing.T) {
	ctx := context.Background()
	ownerID := uuid.New()
	projectID := uuid.New()
	apiSvcID := uuid.New()
	currentSDID := uuid.New()
	rollbackSDID := uuid.New()

	var mu sync.Mutex
	healthState := container.Unhealthy
	healthLogOutput := "HTTP GET /healthz returned 503 Service Unavailable"

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			mu.Lock()
			st := healthState
			out := healthLogOutput
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(container.InspectResponse{
				ID: "c-health-1",
				State: &container.State{
					Running: true,
					Health: &container.Health{
						Status: st,
						Log: []*container.HealthcheckResult{
							{Output: out, ExitCode: 1},
						},
					},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/logs"):
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	cli, cleanup := newTestDockerClient(t, handler)
	defer cleanup()

	svcRecord := &models.Service{
		ID:                         apiSvcID,
		ProjectID:                  projectID,
		Name:                       "api",
		Classification:             models.ClassificationApplication,
		Status:                     models.ServiceStatusRunning,
		ContainerID:                strPtr("c-health-1"),
		CurrentServiceDeploymentID: &currentSDID,
	}
	project := &models.Project{
		ID:      projectID,
		OwnerID: ownerID,
		Name:    "health-app",
		Status:  models.ProjectStatusRunning,
	}

	var rollbackErr error
	var failedRollbackIDs []uuid.UUID

	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListActiveServicesForTest(func(ctx context.Context) ([]*models.Service, error) {
		mu.Lock()
		defer mu.Unlock()
		cp := *svcRecord
		return []*models.Service{&cp}, nil
	})
	svcSvc.SetListServicesForTest(func(ctx context.Context, pID uuid.UUID) ([]*models.Service, error) {
		mu.Lock()
		defer mu.Unlock()
		cp := *svcRecord
		return []*models.Service{&cp}, nil
	})
	svcSvc.SetUpdateServiceStatusForTest(func(ctx context.Context, serviceID uuid.UUID, status string, containerID, imageTag *string, hostPort *int) error {
		mu.Lock()
		defer mu.Unlock()
		svcRecord.Status = status
		return nil
	})

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetServiceService(svcSvc)
	projSvc.SetProjectByIDForTest(func(ctx context.Context, pID uuid.UUID) (*models.Project, error) {
		mu.Lock()
		defer mu.Unlock()
		cp := *project
		return &cp, nil
	})
	projSvc.SetUpdateProjectStatusForTest(func(ctx context.Context, pID uuid.UUID, status string) error {
		mu.Lock()
		defer mu.Unlock()
		project.Status = status
		return nil
	})

	depSvc := services.NewDeploymentService(nil)
	depSvc.SetRollbackServiceDeploymentForTest(func(ctx context.Context, p *models.Project, s *models.Service) (*models.ServiceDeployment, error) {
		mu.Lock()
		defer mu.Unlock()
		if rollbackErr != nil {
			return nil, rollbackErr
		}
		svcRecord.Status = models.ServiceStatusDeploying
		return &models.ServiceDeployment{
			ID:            rollbackSDID,
			ServiceID:     s.ID,
			Status:        models.DeployStatusQueued,
			ExecutionMode: models.ExecutionModeReuseImage,
		}, nil
	})
	depSvc.SetFailServiceDeploymentForTest(func(ctx context.Context, sdID uuid.UUID, reason string) error {
		mu.Lock()
		defer mu.Unlock()
		failedRollbackIDs = append(failedRollbackIDs, sdID)
		svcRecord.Status = models.ServiceStatusDegraded
		return nil
	})

	enqueuer := &mockRollbackEnqueuer{}
	eng := NewEngine(cli, projSvc, depSvc, nil, nil, nil, nil, nil, nil, t.TempDir())
	eng.SetServiceService(svcSvc)
	eng.SetQueueEnqueuer(enqueuer)
	eng.SetAutoRollbackConfig(true, 3, time.Hour)
	defer eng.StopAllLogCollectors()

	// Strike 1: unhealthy marks service + project as degraded, does not rollback yet.
	require.NoError(t, eng.CheckContinuousRuntimeHealth(ctx))
	assert.Equal(t, models.ServiceStatusDegraded, svcRecord.Status)
	assert.Equal(t, models.ProjectStatusDegraded, project.Status)
	assert.Equal(t, 1, eng.GetHealthStrikeCount(apiSvcID))
	assert.Empty(t, enqueuer.EnqueuedIDs())

	// Container recovers to healthy -> service + project recover to running and strikes reset.
	mu.Lock()
	healthState = container.Healthy
	mu.Unlock()
	require.NoError(t, eng.CheckContinuousRuntimeHealth(ctx))
	assert.Equal(t, models.ServiceStatusRunning, svcRecord.Status)
	assert.Equal(t, models.ProjectStatusRunning, project.Status)
	assert.Equal(t, 0, eng.GetHealthStrikeCount(apiSvcID))

	// Container becomes starting -> preserves running status without accumulating strikes.
	mu.Lock()
	healthState = container.Starting
	mu.Unlock()
	require.NoError(t, eng.CheckContinuousRuntimeHealth(ctx))
	assert.Equal(t, models.ServiceStatusRunning, svcRecord.Status)
	assert.Equal(t, 0, eng.GetHealthStrikeCount(apiSvcID))

	// Sustained unhealthy checks (strikes 1, 2, 3) -> strike 3 triggers automatic rollback + enqueue.
	mu.Lock()
	healthState = container.Unhealthy
	mu.Unlock()

	require.NoError(t, eng.CheckContinuousRuntimeHealth(ctx))
	assert.Equal(t, 1, eng.GetHealthStrikeCount(apiSvcID))
	assert.Empty(t, enqueuer.EnqueuedIDs())

	require.NoError(t, eng.CheckContinuousRuntimeHealth(ctx))
	assert.Equal(t, 2, eng.GetHealthStrikeCount(apiSvcID))
	assert.Empty(t, enqueuer.EnqueuedIDs())

	require.NoError(t, eng.CheckContinuousRuntimeHealth(ctx))
	assert.Equal(t, 0, eng.GetHealthStrikeCount(apiSvcID), "strikes reset once rollback is queued")
	assert.Equal(t, []uuid.UUID{rollbackSDID}, enqueuer.EnqueuedIDs())
	assert.Equal(t, models.ProjectStatusDeploying, project.Status)

	// Cooldown suppression: even if status returns to degraded/running with cooldown active,
	// hitting threshold again before cooldown expires does NOT enqueue another rollback.
	svcRecord.Status = models.ServiceStatusDegraded
	eng.SetAutoRollbackConfig(true, 1, time.Hour)
	require.NoError(t, eng.CheckContinuousRuntimeHealth(ctx))
	assert.Len(t, enqueuer.EnqueuedIDs(), 1, "cooldown must prevent rollback loop")

	// When enqueue fails, rollback deployment is marked failed and prior degraded service is preserved.
	eng.SetAutoRollbackConfig(true, 1, 0)
	enqueuer.mu.Lock()
	enqueuer.err = errors.New("redis unavailable")
	enqueuer.mu.Unlock()
	require.NoError(t, eng.CheckContinuousRuntimeHealth(ctx))
	assert.Equal(t, []uuid.UUID{rollbackSDID}, failedRollbackIDs)
	assert.Equal(t, models.ServiceStatusDegraded, svcRecord.Status)

	// When no prior healthy deployment exists, degraded service stays intact (Deployment Safety Invariant).
	enqueuer.mu.Lock()
	enqueuer.err = nil
	enqueuer.mu.Unlock()
	rollbackErr = services.ErrNoDeploymentToRollback
	require.NoError(t, eng.CheckContinuousRuntimeHealth(ctx))
	assert.Equal(t, models.ServiceStatusDegraded, svcRecord.Status)
	assert.Len(t, enqueuer.EnqueuedIDs(), 1)
}

func extractContainerID(path, suffix string) string {
	trimmed := strings.TrimSuffix(path, suffix)
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func TestRuntimeHealth_SelfHealAndCrashLoopEscalation(t *testing.T) {
	ctx := context.Background()
	ownerID := uuid.New()
	projectID := uuid.New()
	apiSvcID := uuid.New()
	currentSDID := uuid.New()
	rollbackSDID := uuid.New()
	hostPort := 18940

	var mu sync.Mutex
	inspectState := container.State{
		Running:  false,
		Status:   container.StateExited,
		ExitCode: 1,
	}
	restartCountMetric := 0
	restartCalls := 0
	var postRestartState container.State

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/restart"):
			mu.Lock()
			restartCalls++
			inspectState = postRestartState
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/start"):
			mu.Lock()
			inspectState = container.State{
				Running: true,
				Status:  container.StateRunning,
				Health:  &container.Health{Status: container.Healthy},
			}
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			mu.Lock()
			st := inspectState
			rc := restartCountMetric
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(container.InspectResponse{
				ID:           "c-selfheal-1",
				RestartCount: rc,
				State:        &st,
			})
		case strings.HasSuffix(r.URL.Path, "/logs"):
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	cli, cleanup := newTestDockerClient(t, handler)
	defer cleanup()

	svcRecord := &models.Service{
		ID:                         apiSvcID,
		ProjectID:                  projectID,
		Name:                       "api",
		Role:                       models.RoleBackend,
		Classification:             models.ClassificationApplication,
		Status:                     models.ServiceStatusRunning,
		ContainerID:                strPtr("c-selfheal-1"),
		HostPort:                   &hostPort,
		PublicExposed:              true,
		CurrentServiceDeploymentID: &currentSDID,
	}
	project := &models.Project{
		ID:      projectID,
		OwnerID: ownerID,
		Name:    "selfheal-app",
		Status:  models.ProjectStatusRunning,
	}

	var sdStatus string = models.DeployStatusRunning
	svcSvc := services.NewServiceService(nil)
	svcSvc.SetGetServiceForTest(func(ctx context.Context, sID uuid.UUID) (*models.Service, error) {
		mu.Lock()
		defer mu.Unlock()
		cp := *svcRecord
		return &cp, nil
	})
	svcSvc.SetListActiveServicesForTest(func(ctx context.Context) ([]*models.Service, error) {
		mu.Lock()
		defer mu.Unlock()
		cp := *svcRecord
		return []*models.Service{&cp}, nil
	})
	svcSvc.SetListServicesForTest(func(ctx context.Context, pID uuid.UUID) ([]*models.Service, error) {
		mu.Lock()
		defer mu.Unlock()
		cp := *svcRecord
		return []*models.Service{&cp}, nil
	})
	svcSvc.SetUpdateServiceStatusForTest(func(ctx context.Context, serviceID uuid.UUID, status string, containerID, imageTag *string, hp *int) error {
		mu.Lock()
		defer mu.Unlock()
		svcRecord.Status = status
		return nil
	})

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetServiceService(svcSvc)
	projSvc.SetProjectGetterForTest(func(ctx context.Context, pID, oID uuid.UUID) (*models.Project, error) {
		mu.Lock()
		defer mu.Unlock()
		cp := *project
		return &cp, nil
	})
	projSvc.SetProjectByIDForTest(func(ctx context.Context, pID uuid.UUID) (*models.Project, error) {
		mu.Lock()
		defer mu.Unlock()
		cp := *project
		return &cp, nil
	})
	projSvc.SetUpdateProjectStatusForTest(func(ctx context.Context, pID uuid.UUID, status string) error {
		mu.Lock()
		defer mu.Unlock()
		project.Status = status
		return nil
	})

	rollbackCalls := 0
	depSvc := services.NewDeploymentService(nil)
	depSvc.SetUpdateServiceDeploymentStatusForTest(func(ctx context.Context, sdID uuid.UUID, newStatus string, reason *string) error {
		mu.Lock()
		sdStatus = newStatus
		mu.Unlock()
		return nil
	})
	depSvc.SetRollbackServiceDeploymentForTest(func(ctx context.Context, p *models.Project, s *models.Service) (*models.ServiceDeployment, error) {
		mu.Lock()
		rollbackCalls++
		svcRecord.Status = models.ServiceStatusDeploying
		mu.Unlock()
		return &models.ServiceDeployment{
			ID:            rollbackSDID,
			ServiceID:     s.ID,
			Status:        models.DeployStatusQueued,
			ExecutionMode: models.ExecutionModeReuseImage,
		}, nil
	})

	router := proxy.NewTrafficRouter()
	enqueuer := &mockRollbackEnqueuer{}
	eng := NewEngine(cli, projSvc, depSvc, nil, nil, nil, nil, nil, nil, t.TempDir())
	eng.SetServiceService(svcSvc)
	eng.SetTrafficRouter(router)
	eng.SetQueueEnqueuer(enqueuer)
	eng.SetSelfHealConfig(true, 3)
	eng.SetCrashLoopConfig(3, 10*time.Minute)
	eng.SetAutoRollbackConfig(true, 3, 0)
	defer eng.StopAllLogCollectors()

	// 1. Transient crash (exit 1) -> self-heal restarts and verifies healthy -> Running & route restored
	mu.Lock()
	inspectState = container.State{Running: false, Status: container.StateExited, ExitCode: 1}
	postRestartState = container.State{Running: true, Status: container.StateRunning, Health: &container.Health{Status: container.Healthy}}
	mu.Unlock()

	require.NoError(t, eng.CheckContinuousRuntimeHealth(ctx))
	assert.Equal(t, 1, restartCalls)
	assert.Equal(t, 1, eng.GetSelfHealRestartCount(apiSvcID))
	assert.Equal(t, 1, eng.GetCrashLoopCount(apiSvcID))
	assert.Equal(t, models.ServiceStatusRunning, svcRecord.Status)
	assert.Empty(t, enqueuer.EnqueuedIDs())
	rt, hasRoute := router.GetRoute(apiSvcID)
	require.True(t, hasRoute)
	assert.Equal(t, "c-selfheal-1", rt.ContainerID)

	// 2. Repeated crashes inside CrashLoopWindow -> tick 2 restarts, tick 3 hits CrashLoopThreshold=3 -> escalates to Failed + AutoRollback
	mu.Lock()
	inspectState = container.State{OOMKilled: true, Running: false, Status: container.StateExited, ExitCode: 137}
	postRestartState = container.State{Running: true, Status: container.StateRunning}
	mu.Unlock()
	require.NoError(t, eng.CheckContinuousRuntimeHealth(ctx))
	assert.Equal(t, 2, restartCalls)
	assert.Equal(t, 2, eng.GetCrashLoopCount(apiSvcID))
	assert.Equal(t, models.ServiceStatusRunning, svcRecord.Status)

	mu.Lock()
	inspectState = container.State{OOMKilled: true, Running: false, Status: container.StateExited, ExitCode: 137}
	mu.Unlock()
	require.NoError(t, eng.CheckContinuousRuntimeHealth(ctx))
	assert.Equal(t, 2, restartCalls, "must NOT restart once crash-loop threshold is reached")
	assert.Equal(t, models.DeployStatusCrashed, sdStatus)
	assert.Equal(t, models.ServiceStatusDeploying, svcRecord.Status)
	assert.Equal(t, 1, rollbackCalls)
	assert.Len(t, enqueuer.EnqueuedIDs(), 1)
	_, hasRoute = router.GetRoute(apiSvcID)
	assert.False(t, hasRoute, "route must be removed on crash-loop failure")

	// 3. Manual StartService resets crash-loop & self-heal budgets
	require.NoError(t, eng.StartService(ctx, projectID, apiSvcID, ownerID))
	assert.Equal(t, 0, eng.GetSelfHealRestartCount(apiSvcID))
	assert.Equal(t, 0, eng.GetCrashLoopCount(apiSvcID))
	assert.Equal(t, models.ServiceStatusRunning, svcRecord.Status)

	// 4. Daemon Restarting loop -> degraded before threshold, escalates + rollbacks on threshold
	mu.Lock()
	restartCountMetric = 1
	inspectState = container.State{Running: false, Restarting: true, Status: container.StateRestarting}
	mu.Unlock()
	require.NoError(t, eng.CheckContinuousRuntimeHealth(ctx))
	assert.Equal(t, models.ServiceStatusDegraded, svcRecord.Status)
	assert.Equal(t, 1, eng.GetCrashLoopCount(apiSvcID))

	mu.Lock()
	restartCountMetric = 3
	inspectState = container.State{Running: false, Restarting: true, Status: container.StateRestarting}
	mu.Unlock()
	require.NoError(t, eng.CheckContinuousRuntimeHealth(ctx))
	assert.Equal(t, 2, rollbackCalls)
	assert.Len(t, enqueuer.EnqueuedIDs(), 2)

	// 5. SelfHealEnabled=false disables restart and immediately fails + auto-rollbacks (or stays Failed if no prior deploy)
	require.NoError(t, eng.StartService(ctx, projectID, apiSvcID, ownerID))
	eng.SetSelfHealConfig(false, 3)
	eng.SetAutoRollbackConfig(false, 3, 0)
	prevRestarts := restartCalls
	mu.Lock()
	inspectState = container.State{Running: false, Status: container.StateExited, ExitCode: 1}
	mu.Unlock()
	require.NoError(t, eng.CheckContinuousRuntimeHealth(ctx))
	assert.Equal(t, prevRestarts, restartCalls)
	assert.Equal(t, models.ServiceStatusFailed, svcRecord.Status)
	assert.Len(t, enqueuer.EnqueuedIDs(), 2)
}
