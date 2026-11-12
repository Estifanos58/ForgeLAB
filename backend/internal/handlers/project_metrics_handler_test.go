package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dockerpkg "github.com/forgelab/backend/internal/docker"
	"github.com/forgelab/backend/internal/handlers"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

func newHandlerTestDockerClient(t *testing.T, handler http.Handler) (*client.Client, func()) {
	t.Helper()
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.41")
		if strings.Contains(r.URL.Path, "/_ping") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
			return
		}
		handler.ServeHTTP(w, r)
	})
	server := httptest.NewServer(wrapped)
	cli, err := client.New(
		client.WithHost(server.URL),
		client.WithHTTPClient(server.Client()),
		client.WithAPIVersion("1.41"),
	)
	require.NoError(t, err)
	return cli, func() {
		_ = cli.Close()
		server.Close()
	}
}

func TestProjectHandler_GetRuntimeMetrics(t *testing.T) {
	ownerID := uuid.New()
	otherUserID := uuid.New()
	projectID := uuid.New()
	serviceID := uuid.New()
	cid := "cid-metrics-web"

	var statsCalls atomic.Int32
	dockerHTTP := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/json") {
			_ = json.NewEncoder(w).Encode(container.InspectResponse{
				ID: cid,
				State: &container.State{
					Running:   true,
					StartedAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
					Health:    &container.Health{Status: container.Healthy},
				},
			})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/stats") {
			statsCalls.Add(1)
			_ = json.NewEncoder(w).Encode(container.StatsResponse{
				ID:     cid,
				OSType: "linux",
				CPUStats: container.CPUStats{
					CPUUsage:    container.CPUUsage{TotalUsage: 300_000_000},
					SystemUsage: 10_000_000_000,
					OnlineCPUs:  1,
				},
				PreCPUStats: container.CPUStats{
					CPUUsage:    container.CPUUsage{TotalUsage: 100_000_000},
					SystemUsage: 0,
					OnlineCPUs:  1,
				},
				MemoryStats: container.MemoryStats{
					Usage: 64 * 1024 * 1024,
					Limit: 512 * 1024 * 1024,
				},
				Networks: map[string]container.NetworkStats{
					"eth0": {RxBytes: 4096, TxBytes: 8192},
				},
				PidsStats: container.PidsStats{Current: 10},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	cli, cleanup := newHandlerTestDockerClient(t, dockerHTTP)
	defer cleanup()

	proj := &models.Project{
		ID:      projectID,
		OwnerID: ownerID,
		Name:    "handler-metrics-app",
		Status:  models.ProjectStatusRunning,
	}
	svc := &models.Service{
		ID:          serviceID,
		ProjectID:   projectID,
		Name:        "web",
		Role:        models.RoleFrontend,
		Status:      models.ServiceStatusRunning,
		ContainerID: &cid,
	}

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListServicesForTest(func(ctx context.Context, pid uuid.UUID) ([]*models.Service, error) {
		return []*models.Service{svc}, nil
	})
	projSvc.SetServiceService(svcSvc)
	projSvc.SetProjectByIDForTest(func(ctx context.Context, pid uuid.UUID) (*models.Project, error) {
		if pid != projectID {
			return nil, services.ErrProjectNotFound
		}
		cp := *proj
		return &cp, nil
	})
	projSvc.SetProjectGetterForTest(func(ctx context.Context, pid, uid uuid.UUID) (*models.Project, error) {
		if pid != projectID {
			return nil, services.ErrProjectNotFound
		}
		if uid != ownerID {
			return nil, services.ErrProjectNotOwned
		}
		cp := *proj
		return &cp, nil
	})

	eng := dockerpkg.NewEngine(cli, projSvc, nil, nil, nil, nil, nil, nil, nil, t.TempDir())
	eng.SetServiceService(svcSvc)

	h := handlers.NewProjectHandler(projSvc, nil, eng, nil)
	r := chi.NewRouter()
	r.Get("/api/projects/{id}/metrics", h.GetRuntimeMetrics)

	t.Run("401 without user context", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/metrics", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("400 invalid project ID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/projects/not-a-uuid/metrics", nil)
		req = req.WithContext(withTestUser(req.Context(), ownerID))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("403 when project owned by another user", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/metrics", nil)
		req = req.WithContext(withTestUser(req.Context(), otherUserID))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("503 when docker engine unavailable", func(t *testing.T) {
		noEngHandler := handlers.NewProjectHandler(projSvc, nil, nil, nil)
		nr := chi.NewRouter()
		nr.Get("/api/projects/{id}/metrics", noEngHandler.GetRuntimeMetrics)
		req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/metrics", nil)
		req = req.WithContext(withTestUser(req.Context(), ownerID))
		rec := httptest.NewRecorder()
		nr.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("200 returns project runtime metrics", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/metrics", nil)
		req = req.WithContext(withTestUser(req.Context(), ownerID))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		var payload dockerpkg.ProjectRuntimeMetrics
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
		assert.Equal(t, projectID, payload.ProjectID)
		assert.True(t, payload.DockerAvailable)
		require.Len(t, payload.Services, 1)
		assert.InDelta(t, 2.0, payload.Services[0].CPUPercent, 0.01)
		assert.Equal(t, uint64(64*1024*1024), payload.Services[0].MemoryUsedBytes)
		assert.Equal(t, uint64(4096), payload.TotalNetworkRxBytes)
		assert.Equal(t, uint64(8192), payload.TotalNetworkTxBytes)
		assert.Equal(t, "healthy", payload.Services[0].HealthStatus)
		assert.False(t, payload.Cached)
		assert.Equal(t, int32(1), statsCalls.Load())
	})

	t.Run("200 serves backend cache on repeat poll and bypasses cache with refresh=true", func(t *testing.T) {
		// Repeat request within cache TTL serves cached snapshot without calling Docker /stats
		req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/metrics", nil)
		req = req.WithContext(withTestUser(req.Context(), ownerID))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		var cachedPayload dockerpkg.ProjectRuntimeMetrics
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&cachedPayload))
		assert.True(t, cachedPayload.Cached)
		assert.Equal(t, int32(1), statsCalls.Load())

		// ?refresh=true bypasses cache
		req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/metrics?refresh=true", nil)
		req = req.WithContext(withTestUser(req.Context(), ownerID))
		rec = httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		var livePayload dockerpkg.ProjectRuntimeMetrics
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&livePayload))
		assert.False(t, livePayload.Cached)
		assert.Equal(t, int32(2), statsCalls.Load())
	})
}

func TestProjectHandler_GetRuntimeMetricsHistory(t *testing.T) {
	ownerID := uuid.New()
	otherUserID := uuid.New()
	projectID := uuid.New()
	serviceID := uuid.New()
	otherServiceID := uuid.New()

	proj := &models.Project{
		ID:      projectID,
		OwnerID: ownerID,
		Name:    "hist-proj",
		Services: []*models.Service{
			{ID: serviceID, ProjectID: projectID, Name: "api"},
		},
	}

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListServicesForTest(func(ctx context.Context, pid uuid.UUID) ([]*models.Service, error) {
		return proj.Services, nil
	})
	projSvc.SetServiceService(svcSvc)
	projSvc.SetProjectGetterForTest(func(ctx context.Context, pid, uid uuid.UUID) (*models.Project, error) {
		if pid != projectID {
			return nil, services.ErrProjectNotFound
		}
		if uid != ownerID {
			return nil, services.ErrProjectNotOwned
		}
		return proj, nil
	})

	metricsSvc := services.NewMetricsService(nil, 7, 15*time.Second)
	now := time.Now().UTC()
	require.NoError(t, metricsSvc.RecordSamples(context.Background(), []models.ServiceMetricsSample{
		{
			ProjectID:       projectID,
			ServiceID:       serviceID,
			ServiceName:     "api",
			Running:         true,
			CPUPercent:      12.5,
			MemoryUsedBytes: 128 * 1024 * 1024,
			RestartCount:    1,
			SampledAt:       now.Add(-5 * time.Minute),
		},
	}))

	h := handlers.NewProjectHandler(projSvc, nil, nil, nil)
	h.SetMetricsService(metricsSvc)

	r := chi.NewRouter()
	r.Get("/api/projects/{id}/metrics/history", h.GetRuntimeMetricsHistory)

	t.Run("401 and 403 enforced", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/metrics/history", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)

		req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/metrics/history", nil)
		req = req.WithContext(withTestUser(req.Context(), otherUserID))
		rec = httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("503 when metricsService unconfigured", func(t *testing.T) {
		noHistH := handlers.NewProjectHandler(projSvc, nil, nil, nil)
		nr := chi.NewRouter()
		nr.Get("/api/projects/{id}/metrics/history", noHistH.GetRuntimeMetricsHistory)
		req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/metrics/history", nil)
		req = req.WithContext(withTestUser(req.Context(), ownerID))
		rec := httptest.NewRecorder()
		nr.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("400 validation errors on hours and foreign service_id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/metrics/history?hours=999", nil)
		req = req.WithContext(withTestUser(req.Context(), ownerID))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/metrics/history?service_id="+otherServiceID.String(), nil)
		req = req.WithContext(withTestUser(req.Context(), ownerID))
		rec = httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("200 returns chronological history and service summaries", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/metrics/history?hours=1&service_id="+serviceID.String(), nil)
		req = req.WithContext(withTestUser(req.Context(), ownerID))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		var payload models.ProjectMetricsHistoryResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
		assert.Equal(t, projectID, payload.ProjectID)
		require.Equal(t, 1, payload.TotalSamples)
		require.Len(t, payload.Summaries, 1)
		assert.InDelta(t, 12.5, payload.Summaries[0].PeakCPUPercent, 0.01)
		assert.Equal(t, 1, payload.Summaries[0].MaxRestartCount)
	})
}
