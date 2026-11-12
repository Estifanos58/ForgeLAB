package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

func TestGetProjectRuntimeMetrics_CollectsCPU_Memory_Network_Health(t *testing.T) {
	ctx := context.Background()
	projectID := uuid.New()
	ownerID := uuid.New()
	apiSvcID := uuid.New()
	workerSvcID := uuid.New()
	jobSvcID := uuid.New()
	missingSvcID := uuid.New()

	apiCID := "cid-api-healthy"
	workerCID := "cid-worker-unhealthy"
	missingCID := "cid-missing-gone"

	startedAt := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339Nano)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			id := extractContainerID(r.URL.Path, "/json")
			switch id {
			case apiCID:
				_ = json.NewEncoder(w).Encode(container.InspectResponse{
					ID:           apiCID,
					RestartCount: 1,
					State: &container.State{
						Running:   true,
						Status:    container.StateRunning,
						StartedAt: startedAt,
						Health: &container.Health{
							Status:        container.Healthy,
							FailingStreak: 0,
							Log: []*container.HealthcheckResult{
								{ExitCode: 0, Output: "HTTP 200 OK"},
							},
						},
					},
				})
				return
			case workerCID:
				_ = json.NewEncoder(w).Encode(container.InspectResponse{
					ID:           workerCID,
					RestartCount: 4,
					State: &container.State{
						Running:   true,
						Status:    container.StateRunning,
						StartedAt: startedAt,
						Health: &container.Health{
							Status:        container.Unhealthy,
							FailingStreak: 3,
							Log: []*container.HealthcheckResult{
								{ExitCode: 1, Output: "connection refused"},
							},
						},
					},
				})
				return
			case missingCID:
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": "No such container"})
				return
			}
		}

		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/stats") {
			id := extractContainerID(r.URL.Path, "/stats")
			switch id {
			case apiCID:
				_ = json.NewEncoder(w).Encode(container.StatsResponse{
					ID:     apiCID,
					OSType: "linux",
					Read:   time.Now().UTC(),
					CPUStats: container.CPUStats{
						CPUUsage: container.CPUUsage{
							TotalUsage: 600_000_000,
						},
						SystemUsage: 20_000_000_000,
						OnlineCPUs:  2,
					},
					PreCPUStats: container.CPUStats{
						CPUUsage: container.CPUUsage{
							TotalUsage: 100_000_000,
						},
						SystemUsage: 10_000_000_000,
						OnlineCPUs:  2,
					},
					MemoryStats: container.MemoryStats{
						Usage: 150 * 1024 * 1024,
						Limit: 1000 * 1024 * 1024,
						Stats: map[string]uint64{
							"inactive_file": 50 * 1024 * 1024,
						},
					},
					Networks: map[string]container.NetworkStats{
						"eth0": {
							RxBytes: 123456,
							TxBytes: 654321,
						},
					},
					PidsStats: container.PidsStats{Current: 24},
				})
				return
			case workerCID:
				_ = json.NewEncoder(w).Encode(container.StatsResponse{
					ID:     workerCID,
					OSType: "linux",
					Read:   time.Now().UTC(),
					CPUStats: container.CPUStats{
						CPUUsage:    container.CPUUsage{TotalUsage: 350_000_000},
						SystemUsage: 20_000_000_000,
						OnlineCPUs:  2,
					},
					PreCPUStats: container.CPUStats{
						CPUUsage:    container.CPUUsage{TotalUsage: 100_000_000},
						SystemUsage: 10_000_000_000,
						OnlineCPUs:  2,
					},
					MemoryStats: container.MemoryStats{
						Usage: 256 * 1024 * 1024,
						Limit: 512 * 1024 * 1024,
					},
					Networks: map[string]container.NetworkStats{
						"eth0": {RxBytes: 1000, TxBytes: 2000},
					},
					PidsStats: container.PidsStats{Current: 12},
				})
				return
			}
		}

		w.WriteHeader(http.StatusNotFound)
	})

	cli, cleanup := newDirectTestDockerClient(t, handler)
	defer cleanup()

	svcs := []*models.Service{
		{
			ID:             apiSvcID,
			ProjectID:      projectID,
			Name:           "api",
			Role:           models.RoleBackend,
			Classification: models.ClassificationApplication,
			Status:         models.ServiceStatusRunning,
			ContainerID:    strPtr(apiCID),
			ResourceConfig: models.ResourceConfig{CpuMillicores: 1000, MemoryMB: 1000},
		},
		{
			ID:             workerSvcID,
			ProjectID:      projectID,
			Name:           "worker",
			Role:           models.RoleWorker,
			Classification: models.ClassificationWorker,
			Status:         models.ServiceStatusDegraded,
			ContainerID:    strPtr(workerCID),
			ResourceConfig: models.ResourceConfig{CpuMillicores: 500, MemoryMB: 512},
		},
		{
			ID:             jobSvcID,
			ProjectID:      projectID,
			Name:           "migrator",
			Role:           models.RoleOther,
			Classification: models.ClassificationJob,
			Status:         models.ServiceStatusStopped,
		},
		{
			ID:             missingSvcID,
			ProjectID:      projectID,
			Name:           "ghost",
			Role:           models.RoleOther,
			Classification: models.ClassificationApplication,
			Status:         models.ServiceStatusFailed,
			ContainerID:    strPtr(missingCID),
		},
	}

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectByIDForTest(func(ctx context.Context, pID uuid.UUID) (*models.Project, error) {
		return &models.Project{
			ID:      projectID,
			OwnerID: ownerID,
			Name:    "metrics-project",
			Status:  models.ProjectStatusDegraded,
		}, nil
	})
	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListServicesForTest(func(ctx context.Context, pID uuid.UUID) ([]*models.Service, error) {
		return svcs, nil
	})
	projSvc.SetServiceService(svcSvc)

	eng := NewEngine(cli, projSvc, nil, nil, nil, nil, nil, nil, nil, t.TempDir())
	eng.SetServiceService(svcSvc)

	metrics, err := eng.GetProjectRuntimeMetrics(ctx, projectID)
	require.NoError(t, err)
	require.NotNil(t, metrics)

	assert.Equal(t, projectID, metrics.ProjectID)
	assert.Equal(t, "metrics-project", metrics.ProjectName)
	assert.True(t, metrics.DockerAvailable)
	require.Len(t, metrics.Services, 4)

	// 1. API service
	apiM := metrics.Services[0]
	assert.Equal(t, "api", apiM.ServiceName)
	assert.True(t, apiM.Running)
	assert.Equal(t, 1, apiM.RestartCount)
	assert.Equal(t, "healthy", apiM.HealthStatus)
	assert.Equal(t, "HTTP 200 OK", apiM.LastHealthOutput)
	assert.InDelta(t, 10.0, apiM.CPUPercent, 0.01) // (500M / 10B) * 2 * 100 = 10%
	assert.Equal(t, uint64(100*1024*1024), apiM.MemoryUsedBytes)
	assert.InDelta(t, 10.0, apiM.MemoryPercent, 0.01)
	assert.Equal(t, uint64(123456), apiM.NetworkRxBytes)
	assert.Equal(t, uint64(654321), apiM.NetworkTxBytes)
	assert.Equal(t, uint64(24), apiM.PidsCurrent)
	assert.GreaterOrEqual(t, apiM.UptimeSeconds, int64(100))

	// 2. Worker service (unhealthy)
	workerM := metrics.Services[1]
	assert.Equal(t, "worker", workerM.ServiceName)
	assert.True(t, workerM.Running)
	assert.Equal(t, 4, workerM.RestartCount)
	assert.Equal(t, "unhealthy", workerM.HealthStatus)
	assert.Equal(t, 3, workerM.HealthFailingStreak)
	assert.Equal(t, "connection refused", workerM.LastHealthOutput)
	assert.InDelta(t, 5.0, workerM.CPUPercent, 0.01) // (250M / 10B) * 2 * 100 = 5%

	// 3. Job service without container
	jobM := metrics.Services[2]
	assert.Equal(t, "migrator", jobM.ServiceName)
	assert.False(t, jobM.Running)
	assert.Equal(t, "none", jobM.HealthStatus)
	assert.Empty(t, jobM.MetricError)

	// 4. Missing container yields explicit per-service error without breaking response
	missingM := metrics.Services[3]
	assert.Equal(t, "ghost", missingM.ServiceName)
	assert.False(t, missingM.Running)
	assert.Contains(t, missingM.MetricError, "not found")

	// Aggregates
	assert.InDelta(t, 15.0, metrics.TotalCPUPercent, 0.01)
	assert.Equal(t, uint64(100*1024*1024+256*1024*1024), metrics.TotalMemoryUsedBytes)
	assert.Equal(t, uint64(123456+1000), metrics.TotalNetworkRxBytes)
	assert.Equal(t, uint64(654321+2000), metrics.TotalNetworkTxBytes)
}

func TestGetProjectRuntimeMetrics_NilDockerClient_GracefulDegradation(t *testing.T) {
	ctx := context.Background()
	projectID := uuid.New()
	svcID := uuid.New()
	cid := "some-container-id"

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectByIDForTest(func(ctx context.Context, pID uuid.UUID) (*models.Project, error) {
		return &models.Project{
			ID:     projectID,
			Name:   "no-docker",
			Status: models.ProjectStatusStopped,
		}, nil
	})
	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListServicesForTest(func(ctx context.Context, pID uuid.UUID) ([]*models.Service, error) {
		return []*models.Service{
			{ID: svcID, ProjectID: projectID, Name: "web", ContainerID: &cid, Status: models.ServiceStatusRunning},
		}, nil
	})
	projSvc.SetServiceService(svcSvc)

	eng := NewEngine(nil, projSvc, nil, nil, nil, nil, nil, nil, nil, t.TempDir())
	eng.SetServiceService(svcSvc)

	metrics, err := eng.GetProjectRuntimeMetrics(ctx, projectID)
	require.NoError(t, err)
	assert.False(t, metrics.DockerAvailable)
	require.Len(t, metrics.Services, 1)
	assert.Contains(t, metrics.Services[0].MetricError, "docker client unavailable")
}

func TestGetProjectRuntimeMetrics_ServiceQueryError_Propagates(t *testing.T) {
	ctx := context.Background()
	projectID := uuid.New()

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectByIDForTest(func(ctx context.Context, pID uuid.UUID) (*models.Project, error) {
		return &models.Project{ID: projectID, Name: "err-proj"}, nil
	})
	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListServicesForTest(func(ctx context.Context, pID uuid.UUID) ([]*models.Service, error) {
		return nil, errors.New("db connection reset")
	})

	eng := NewEngine(nil, projSvc, nil, nil, nil, nil, nil, nil, nil, t.TempDir())
	eng.SetServiceService(svcSvc)

	_, err := eng.GetProjectRuntimeMetrics(ctx, projectID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db connection reset")
}

func TestGetProjectRuntimeMetrics_CacheAndSingleflight(t *testing.T) {
	ctx := context.Background()
	projectID := uuid.New()
	svcID := uuid.New()
	cid := "cid-cache-sf"

	var statsCalls atomic.Int32
	var slowStats atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			_ = json.NewEncoder(w).Encode(container.InspectResponse{
				ID:           cid,
				RestartCount: 2,
				State: &container.State{
					Running:   true,
					Status:    container.StateRunning,
					StartedAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
					Health:    &container.Health{Status: container.Healthy},
				},
			})
			return
		}
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/stats") {
			if slowStats.Load() {
				time.Sleep(40 * time.Millisecond)
			}
			statsCalls.Add(1)
			_ = json.NewEncoder(w).Encode(container.StatsResponse{
				ID:     cid,
				OSType: "linux",
				CPUStats: container.CPUStats{
					CPUUsage:    container.CPUUsage{TotalUsage: 400_000_000},
					SystemUsage: 10_000_000_000,
					OnlineCPUs:  1,
				},
				PreCPUStats: container.CPUStats{
					CPUUsage:    container.CPUUsage{TotalUsage: 100_000_000},
					SystemUsage: 0,
					OnlineCPUs:  1,
				},
				MemoryStats: container.MemoryStats{Usage: 128 * 1024 * 1024, Limit: 512 * 1024 * 1024},
				Networks:    map[string]container.NetworkStats{"eth0": {RxBytes: 512, TxBytes: 1024}},
				PidsStats:   container.PidsStats{Current: 7},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	cli, cleanup := newDirectTestDockerClient(t, handler)
	defer cleanup()

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectByIDForTest(func(ctx context.Context, pID uuid.UUID) (*models.Project, error) {
		return &models.Project{ID: projectID, Name: "cache-proj", Status: models.ProjectStatusRunning}, nil
	})
	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListServicesForTest(func(ctx context.Context, pID uuid.UUID) ([]*models.Service, error) {
		return []*models.Service{{ID: svcID, ProjectID: projectID, Name: "api", ContainerID: &cid, Status: models.ServiceStatusRunning}}, nil
	})
	projSvc.SetServiceService(svcSvc)

	eng := NewEngine(cli, projSvc, nil, nil, nil, nil, nil, nil, nil, t.TempDir())
	eng.SetServiceService(svcSvc)
	eng.SetMetricsCacheTTL(30 * time.Second)

	// 1. Cold fetch hits Docker /stats once
	m1, err := eng.GetProjectRuntimeMetrics(ctx, projectID)
	require.NoError(t, err)
	assert.False(t, m1.Cached)
	assert.Equal(t, int32(1), statsCalls.Load())

	// 2. Subsequent poll within TTL hits backend cache without querying Docker
	m2, err := eng.GetProjectRuntimeMetrics(ctx, projectID)
	require.NoError(t, err)
	assert.True(t, m2.Cached)
	assert.Equal(t, int32(1), statsCalls.Load())

	// 3. Forced refresh bypasses cache
	m3, err := eng.GetProjectRuntimeMetricsWithOptions(ctx, projectID, true)
	require.NoError(t, err)
	assert.False(t, m3.Cached)
	assert.Equal(t, int32(2), statsCalls.Load())

	// 4. Invalidate cache + concurrent calls coalesce via singleflight
	eng.InvalidateProjectMetricsCache(projectID)
	slowStats.Store(true)
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snap, callErr := eng.GetProjectRuntimeMetricsWithOptions(ctx, projectID, true)
			if callErr != nil {
				errs <- callErr
				return
			}
			if snap == nil || len(snap.Services) != 1 {
				errs <- errors.New("unexpected empty snapshot")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for callErr := range errs {
		require.NoError(t, callErr)
	}
	assert.Equal(t, int32(3), statsCalls.Load(), "concurrent callers must coalesce via singleflight")
}

func TestSampleActiveProjectsMetrics_PersistsAndUpdatesCache(t *testing.T) {
	ctx := context.Background()
	projectID := uuid.New()
	svcID := uuid.New()
	cid := "cid-sampled"

	var statsCalls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			_ = json.NewEncoder(w).Encode(container.InspectResponse{
				ID:           cid,
				RestartCount: 3,
				State: &container.State{
					Running: true,
					Health:  &container.Health{Status: container.Healthy},
				},
			})
			return
		}
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/stats") {
			statsCalls.Add(1)
			_ = json.NewEncoder(w).Encode(container.StatsResponse{
				ID:     cid,
				OSType: "linux",
				CPUStats: container.CPUStats{
					CPUUsage:    container.CPUUsage{TotalUsage: 500_000_000},
					SystemUsage: 10_000_000_000,
					OnlineCPUs:  1,
				},
				PreCPUStats: container.CPUStats{
					CPUUsage:    container.CPUUsage{TotalUsage: 100_000_000},
					SystemUsage: 0,
					OnlineCPUs:  1,
				},
				MemoryStats: container.MemoryStats{Usage: 200 * 1024 * 1024, Limit: 1000 * 1024 * 1024},
				Networks:    map[string]container.NetworkStats{"eth0": {RxBytes: 2048, TxBytes: 4096}},
				PidsStats:   container.PidsStats{Current: 9},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	cli, cleanup := newDirectTestDockerClient(t, handler)
	defer cleanup()

	svcModel := &models.Service{
		ID:             svcID,
		ProjectID:      projectID,
		Name:           "worker",
		ContainerID:    &cid,
		Status:         models.ServiceStatusRunning,
		ResourceConfig: models.ResourceConfig{MemoryMB: 1000},
	}
	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectByIDForTest(func(ctx context.Context, pID uuid.UUID) (*models.Project, error) {
		return &models.Project{ID: projectID, Name: "sampled-proj", Status: models.ProjectStatusRunning}, nil
	})
	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListActiveServicesForTest(func(ctx context.Context) ([]*models.Service, error) {
		return []*models.Service{svcModel}, nil
	})
	svcSvc.SetListServicesForTest(func(ctx context.Context, pID uuid.UUID) ([]*models.Service, error) {
		return []*models.Service{svcModel}, nil
	})
	projSvc.SetServiceService(svcSvc)

	metricsSvc := services.NewMetricsService(nil, 7, 15*time.Second)
	eng := NewEngine(cli, projSvc, nil, nil, nil, nil, nil, nil, nil, t.TempDir())
	eng.SetServiceService(svcSvc)
	eng.SetMetricsService(metricsSvc)
	eng.SetMetricsCacheTTL(30 * time.Second)

	require.NoError(t, eng.SampleActiveProjectsMetrics(ctx))
	assert.Equal(t, int32(1), statsCalls.Load())

	// Subsequent request serves directly from cache primed by sampler
	cached, err := eng.GetProjectRuntimeMetrics(ctx, projectID)
	require.NoError(t, err)
	assert.True(t, cached.Cached)
	assert.Equal(t, int32(1), statsCalls.Load())

	// Historical sample was persisted cleanly
	hist, err := metricsSvc.QueryProjectHistory(ctx, projectID, services.MetricsHistoryQueryOptions{
		From: time.Now().UTC().Add(-time.Hour),
		To:   time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, 1, hist.TotalSamples)
	require.Len(t, hist.Summaries, 1)
	assert.Equal(t, "worker", hist.Summaries[0].ServiceName)
	assert.InDelta(t, 4.0, hist.Summaries[0].PeakCPUPercent, 0.01)
	assert.Equal(t, 3, hist.Summaries[0].MaxRestartCount)
	assert.Equal(t, uint64(2048), hist.Samples[0].NetworkRxBytes)
}
