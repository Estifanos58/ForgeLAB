package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
	netpkg "github.com/forgelab/backend/internal/network"
	"github.com/forgelab/backend/internal/proxy"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
)

func TestEngine_RestorePreviousHealthyContainer(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully restores running previous container", func(t *testing.T) {
		var startCalls int32
		var inspectCalls int32

		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Logf("mock received: %s %s", r.Method, r.URL.Path)
			switch {
			case strings.HasSuffix(r.URL.Path, "/start"):
				atomic.AddInt32(&startCalls, 1)
				w.WriteHeader(http.StatusNoContent)
			case strings.HasSuffix(r.URL.Path, "/json"):
				atomic.AddInt32(&inspectCalls, 1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(container.InspectResponse{
					ID: "prev-container-1234567890",
					State: &container.State{
						Running:  true,
						Status:   container.StateRunning,
						ExitCode: 0,
					},
				})
			default:
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer mockServer.Close()

		cli, err := client.New(
			client.WithHost(mockServer.URL),
			client.WithHTTPClient(mockServer.Client()),
			client.WithAPIVersion("1.41"),
		)
		require.NoError(t, err)
		defer cli.Close()

		eng := NewEngine(cli, nil, nil, nil, nil, nil, nil, nil, nil, "")

		service := &models.Service{
			ID:        uuid.New(),
			ProjectID: uuid.New(),
			Name:      "web",
		}
		prevCID := "prev-container-1234567890"
		prevDeployID := uuid.New()

		var loggedMessages []string
		emitLog := func(phase, stream, message string) {
			loggedMessages = append(loggedMessages, message)
		}

		res := eng.RestorePreviousHealthyContainer(ctx, service, &prevCID, &prevDeployID, emitLog)

		assert.True(t, res.Restored, "previous container must be successfully restored")
		assert.Equal(t, int32(1), atomic.LoadInt32(&startCalls), "ContainerStart must be called once")
		assert.Equal(t, int32(1), atomic.LoadInt32(&inspectCalls), "ContainerInspect must be called once")

		// Verify safety invariant confirmation log
		var foundSafetyLog bool
		for _, msg := range loggedMessages {
			if strings.Contains(msg, "SAFETY INVARIANT ENFORCED") && strings.Contains(msg, "successfully verified running") {
				foundSafetyLog = true
				break
			}
		}
		assert.True(t, foundSafetyLog, "safety invariant log must confirm container is running")
	})

	t.Run("fails restoration when ContainerStart returns error", func(t *testing.T) {
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/start") {
				http.Error(w, `{"message":"port is already allocated"}`, http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer mockServer.Close()

		cli, err := client.New(
			client.WithHost(mockServer.URL),
			client.WithHTTPClient(mockServer.Client()),
			client.WithAPIVersion("1.41"),
		)
		require.NoError(t, err)
		defer cli.Close()

		eng := NewEngine(cli, nil, nil, nil, nil, nil, nil, nil, nil, "")

		service := &models.Service{
			ID:        uuid.New(),
			ProjectID: uuid.New(),
			Name:      "api",
		}
		prevCID := "prev-container-fail"
		prevDeployID := uuid.New()

		var loggedMessages []string
		emitLog := func(phase, stream, message string) {
			loggedMessages = append(loggedMessages, message)
		}

		res := eng.RestorePreviousHealthyContainer(ctx, service, &prevCID, &prevDeployID, emitLog)

		assert.False(t, res.Restored, "restoration must fail when start returns error")
		assert.Error(t, res.Error)

		var foundFailureLog bool
		for _, msg := range loggedMessages {
			if strings.Contains(msg, "CRITICAL RECOVERY FAILURE") {
				foundFailureLog = true
				break
			}
		}
		assert.True(t, foundFailureLog, "must log critical recovery failure")
	})

	t.Run("fails restoration when container starts but immediately exits", func(t *testing.T) {
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.Contains(r.URL.Path, "/start"):
				w.WriteHeader(http.StatusNoContent)
			case strings.Contains(r.URL.Path, "/json"):
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(container.InspectResponse{
					ID: "prev-container-crashed",
					State: &container.State{
						Running:  false,
						Status:   container.StateExited,
						ExitCode: 137,
					},
				})
			default:
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer mockServer.Close()

		cli, err := client.New(
			client.WithHost(mockServer.URL),
			client.WithHTTPClient(mockServer.Client()),
			client.WithAPIVersion("1.41"),
		)
		require.NoError(t, err)
		defer cli.Close()

		eng := NewEngine(cli, nil, nil, nil, nil, nil, nil, nil, nil, "")

		service := &models.Service{
			ID:        uuid.New(),
			ProjectID: uuid.New(),
			Name:      "db",
		}
		prevCID := "prev-container-crashed"
		prevDeployID := uuid.New()

		var loggedMessages []string
		emitLog := func(phase, stream, message string) {
			loggedMessages = append(loggedMessages, message)
		}

		res := eng.RestorePreviousHealthyContainer(ctx, service, &prevCID, &prevDeployID, emitLog)

		assert.False(t, res.Restored, "restoration must fail when container is not running after start")
		assert.Error(t, res.Error)
		assert.Contains(t, res.Error.Error(), "not running")
		assert.Contains(t, res.Error.Error(), "137")

		var foundFailureLog bool
		for _, msg := range loggedMessages {
			if strings.Contains(msg, "CRITICAL RECOVERY FAILURE") && strings.Contains(msg, "is not running") {
				foundFailureLog = true
				break
			}
		}
		assert.True(t, foundFailureLog, "must log critical recovery failure describing non-running container")
	})

	t.Run("handles nil previous container gracefully", func(t *testing.T) {
		eng := NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, nil, "")
		service := &models.Service{
			ID:        uuid.New(),
			ProjectID: uuid.New(),
			Name:      "stateless-svc",
		}

		res := eng.RestorePreviousHealthyContainer(ctx, service, nil, nil, func(p, s, m string) {})
		assert.False(t, res.Attempted)
		assert.False(t, res.Restored)
	})

	t.Run("polls starting health check until healthy and restores traffic route", func(t *testing.T) {
		var inspectCalls int32

		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/start"):
				w.WriteHeader(http.StatusNoContent)
			case strings.HasSuffix(r.URL.Path, "/json"):
				n := atomic.AddInt32(&inspectCalls, 1)
				healthStatus := container.Starting
				if n >= 3 {
					healthStatus = container.Healthy
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(container.InspectResponse{
					ID: "prev-container-health-poll",
					State: &container.State{
						Running: true,
						Status:  container.StateRunning,
						Health:  &container.Health{Status: healthStatus},
					},
				})
			default:
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer mockServer.Close()

		cli, err := client.New(
			client.WithHost(mockServer.URL),
			client.WithHTTPClient(mockServer.Client()),
			client.WithAPIVersion("1.41"),
		)
		require.NoError(t, err)
		defer cli.Close()

		router := proxy.NewTrafficRouter()
		eng := NewEngine(cli, nil, nil, nil, nil, nil, nil, nil, nil, t.TempDir())
		eng.SetTrafficRouter(router)
		defer eng.StopAllLogCollectors()

		port := 18901
		service := &models.Service{
			ID:        uuid.New(),
			ProjectID: uuid.New(),
			Name:      "api",
			Role:      models.RoleBackend,
			HostPort:  &port,
		}
		prevCID := "prev-container-health-poll"
		prevDeployID := uuid.New()

		res := eng.RestorePreviousHealthyContainer(ctx, service, &prevCID, &prevDeployID, func(p, s, m string) {})
		assert.True(t, res.Restored)
		assert.GreaterOrEqual(t, atomic.LoadInt32(&inspectCalls), int32(3))

		rt, ok := router.GetRoute(service.ID)
		require.True(t, ok, "TrafficRouter route must be restored after verified recovery")
		assert.Equal(t, prevCID, rt.ContainerID)
		assert.Equal(t, port, rt.HostPort)
	})

	t.Run("fails closed when restored container reports unhealthy", func(t *testing.T) {
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/start"):
				w.WriteHeader(http.StatusNoContent)
			case strings.HasSuffix(r.URL.Path, "/json"):
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(container.InspectResponse{
					ID: "prev-container-unhealthy",
					State: &container.State{
						Running: true,
						Status:  container.StateRunning,
						Health: &container.Health{
							Status: container.Unhealthy,
							Log: []*container.HealthcheckResult{
								{Output: "connection refused on :8080", ExitCode: 1},
							},
						},
					},
				})
			default:
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer mockServer.Close()

		cli, err := client.New(
			client.WithHost(mockServer.URL),
			client.WithHTTPClient(mockServer.Client()),
			client.WithAPIVersion("1.41"),
		)
		require.NoError(t, err)
		defer cli.Close()

		router := proxy.NewTrafficRouter()
		eng := NewEngine(cli, nil, nil, nil, nil, nil, nil, nil, nil, t.TempDir())
		eng.SetTrafficRouter(router)

		port := 18902
		service := &models.Service{
			ID:        uuid.New(),
			ProjectID: uuid.New(),
			Name:      "api",
			HostPort:  &port,
		}
		prevCID := "prev-container-unhealthy"

		var logged []string
		res := eng.RestorePreviousHealthyContainer(ctx, service, &prevCID, nil, func(p, s, m string) {
			logged = append(logged, m)
		})
		assert.False(t, res.Restored)
		require.Error(t, res.Error)
		assert.Contains(t, res.Error.Error(), "unhealthy")
		assert.Contains(t, res.Error.Error(), "connection refused")
		_, hasRoute := router.GetRoute(service.ID)
		assert.False(t, hasRoute, "route must not be restored for unhealthy container")
		assert.NotContains(t, strings.Join(logged, "\n"), "SAFETY INVARIANT ENFORCED")
	})
}

func TestToCoarseServiceStatus_RecoveryAndDegradedTransitions(t *testing.T) {
	// 1. When deployment fails and there IS a verified healthy previous container,
	// service status remains Running.
	assert.Equal(t, models.ServiceStatusRunning, toCoarseServiceStatus(models.DeployStatusFailed, true))
	assert.Equal(t, models.ServiceStatusRunning, toCoarseServiceStatus(models.DeployStatusCrashed, true))

	// 2. When deployment fails and there is NO healthy previous container (or previous container restoration failed),
	// service status MUST degrade to Failed, never falsely reporting Running!
	assert.Equal(t, models.ServiceStatusFailed, toCoarseServiceStatus(models.DeployStatusFailed, false))
	assert.Equal(t, models.ServiceStatusFailed, toCoarseServiceStatus(models.DeployStatusCrashed, false))

	// 3. Normal states
	assert.Equal(t, models.ServiceStatusRunning, toCoarseServiceStatus(models.DeployStatusRunning, false))
	assert.Equal(t, models.ServiceStatusRunning, toCoarseServiceStatus(models.DeployStatusRunning, true))
	assert.Equal(t, models.ServiceStatusStopped, toCoarseServiceStatus(models.DeployStatusStopped, false))
	assert.Equal(t, models.ServiceStatusDeploying, toCoarseServiceStatus(models.DeployStatusBuilding, false))
	assert.Equal(t, models.ServiceStatusDeploying, toCoarseServiceStatus(models.DeployStatusHealthChecking, true))
}

func TestEngine_RestorePreviousLifecycle_Regression(t *testing.T) {
	ctx := context.Background()

	t.Run("creation failure triggers previous container restoration and logs safety confirmation", func(t *testing.T) {
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/start"):
				w.WriteHeader(http.StatusNoContent)
			case strings.HasSuffix(r.URL.Path, "/json"):
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(container.InspectResponse{
					ID: "prev-postgres-1",
					State: &container.State{
						Running: true,
						Status:  container.StateRunning,
					},
				})
			default:
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer mockServer.Close()

		cli, err := client.New(
			client.WithHost(mockServer.URL),
			client.WithHTTPClient(mockServer.Client()),
			client.WithAPIVersion("1.41"),
		)
		require.NoError(t, err)
		defer cli.Close()

		eng := NewEngine(cli, nil, nil, nil, nil, nil, nil, nil, nil, "")

		service := &models.Service{
			ID:        uuid.New(),
			ProjectID: uuid.New(),
			Name:      "postgres",
		}
		prevCID := "prev-postgres-1"
		prevDeployID := uuid.New()
		stoppedPreviousForStateful := true
		hasPreviousHealthy := true

		var loggedMessages []string
		emitLog := func(phase, stream, message string) {
			loggedMessages = append(loggedMessages, message)
		}

		restorePreviousIfNeeded := func(failureReason, phase string) string {
			if stoppedPreviousForStateful && prevCID != "" {
				res := eng.RestorePreviousHealthyContainer(ctx, service, &prevCID, &prevDeployID, emitLog)
				if !res.Restored {
					hasPreviousHealthy = false
					return fmt.Sprintf("%s; RESTORATION FAILED: previous container could not be restored (%v)", failureReason, res.Error)
				}
			}
			return failureReason
		}

		creationErr := errors.New("docker: insufficient memory to create container")
		reason := fmt.Sprintf("Failed to create container for service '%s': %v", service.Name, creationErr)
		reason = restorePreviousIfNeeded(reason, models.LogPhaseStartup)

		assert.Contains(t, reason, "insufficient memory")
		assert.NotContains(t, reason, "RESTORATION FAILED")
		assert.True(t, hasPreviousHealthy, "hasPreviousHealthy must remain true after successful restoration")
		assert.Equal(t, models.ServiceStatusRunning, toCoarseServiceStatus(models.DeployStatusFailed, hasPreviousHealthy))
	})

	t.Run("startup failure with restoration failure degrades service status to failed", func(t *testing.T) {
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/start") {
				http.Error(w, `{"message":"port 5432 already allocated"}`, http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer mockServer.Close()

		cli, err := client.New(
			client.WithHost(mockServer.URL),
			client.WithHTTPClient(mockServer.Client()),
			client.WithAPIVersion("1.41"),
		)
		require.NoError(t, err)
		defer cli.Close()

		eng := NewEngine(cli, nil, nil, nil, nil, nil, nil, nil, nil, "")

		service := &models.Service{
			ID:        uuid.New(),
			ProjectID: uuid.New(),
			Name:      "postgres",
		}
		prevCID := "prev-postgres-locked"
		prevDeployID := uuid.New()
		stoppedPreviousForStateful := true
		hasPreviousHealthy := true

		var loggedMessages []string
		emitLog := func(phase, stream, message string) {
			loggedMessages = append(loggedMessages, message)
		}

		restorePreviousIfNeeded := func(failureReason, phase string) string {
			if stoppedPreviousForStateful && prevCID != "" {
				res := eng.RestorePreviousHealthyContainer(ctx, service, &prevCID, &prevDeployID, emitLog)
				if !res.Restored {
					hasPreviousHealthy = false
					return fmt.Sprintf("%s; RESTORATION FAILED: previous container could not be restored (%v)", failureReason, res.Error)
				}
			}
			return failureReason
		}

		startupErr := errors.New("container start exited immediately with code 1")
		reason := fmt.Sprintf("Failed to start container for service '%s': %v", service.Name, startupErr)
		reason = restorePreviousIfNeeded(reason, models.LogPhaseStartup)

		assert.Contains(t, reason, "RESTORATION FAILED")
		assert.False(t, hasPreviousHealthy, "hasPreviousHealthy must flip to false on restoration failure")
		// Service status MUST be Failed, never falsely reporting Running!
		assert.Equal(t, models.ServiceStatusFailed, toCoarseServiceStatus(models.DeployStatusFailed, hasPreviousHealthy))
	})

	t.Run("health check failure triggers previous container restoration", func(t *testing.T) {
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/start"):
				w.WriteHeader(http.StatusNoContent)
			case strings.HasSuffix(r.URL.Path, "/json"):
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(container.InspectResponse{
					ID: "prev-redis-healthy",
					State: &container.State{
						Running: true,
						Status:  container.StateRunning,
					},
				})
			default:
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer mockServer.Close()

		cli, err := client.New(
			client.WithHost(mockServer.URL),
			client.WithHTTPClient(mockServer.Client()),
			client.WithAPIVersion("1.41"),
		)
		require.NoError(t, err)
		defer cli.Close()

		eng := NewEngine(cli, nil, nil, nil, nil, nil, nil, nil, nil, "")

		service := &models.Service{
			ID:        uuid.New(),
			ProjectID: uuid.New(),
			Name:      "redis",
		}
		prevCID := "prev-redis-healthy"
		prevDeployID := uuid.New()
		stoppedPreviousForStateful := true
		hasPreviousHealthy := true

		var loggedMessages []string
		emitLog := func(phase, stream, message string) {
			loggedMessages = append(loggedMessages, message)
		}

		restorePreviousIfNeeded := func(failureReason, phase string) string {
			if stoppedPreviousForStateful && prevCID != "" {
				res := eng.RestorePreviousHealthyContainer(ctx, service, &prevCID, &prevDeployID, emitLog)
				if !res.Restored {
					hasPreviousHealthy = false
					return fmt.Sprintf("%s; RESTORATION FAILED: previous container could not be restored (%v)", failureReason, res.Error)
				}
			}
			return failureReason
		}

		healthCheckErr := "service 'redis' failed health check: endpoint returned HTTP 500"
		reason := restorePreviousIfNeeded(healthCheckErr, models.LogPhaseHealth)

		assert.Equal(t, healthCheckErr, reason)
		assert.True(t, hasPreviousHealthy)
		assert.Equal(t, models.ServiceStatusRunning, toCoarseServiceStatus(models.DeployStatusFailed, hasPreviousHealthy))
	})
}

func TestEngine_VerifyPreviousHealthyContainer(t *testing.T) {
	ctx := context.Background()

	t.Run("verifies live previous container and restores missing route", func(t *testing.T) {
		prevCID := "verified-prev-cid"
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(container.InspectResponse{
					ID: prevCID,
					State: &container.State{
						Running: true,
						Status:  container.StateRunning,
						Health:  &container.Health{Status: container.Healthy},
					},
				})
			case strings.HasSuffix(r.URL.Path, "/logs"):
				w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
				w.WriteHeader(http.StatusOK)
			default:
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer mockServer.Close()

		cli, err := client.New(
			client.WithHost(mockServer.URL),
			client.WithHTTPClient(mockServer.Client()),
			client.WithAPIVersion("1.41"),
		)
		require.NoError(t, err)
		defer cli.Close()

		router := proxy.NewTrafficRouter()
		eng := NewEngine(cli, nil, nil, nil, nil, nil, nil, nil, nil, t.TempDir())
		eng.SetTrafficRouter(router)
		defer eng.StopAllLogCollectors()

		port := 18903
		service := &models.Service{
			ID:        uuid.New(),
			ProjectID: uuid.New(),
			Name:      "web",
			Role:      models.RoleFrontend,
			HostPort:  &port,
		}

		res := eng.VerifyPreviousHealthyContainer(ctx, service, &prevCID, nil, nil)
		assert.True(t, res.Restored)
		rt, ok := router.GetRoute(service.ID)
		require.True(t, ok)
		assert.Equal(t, prevCID, rt.ContainerID)
		assert.Equal(t, port, rt.HostPort)
	})

	t.Run("fails closed when untouched previous container exited", func(t *testing.T) {
		prevCID := "dead-prev-cid"
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(container.InspectResponse{
				ID: prevCID,
				State: &container.State{
					Running:  false,
					Status:   container.StateExited,
					ExitCode: 137,
				},
			})
		}))
		defer mockServer.Close()

		cli, err := client.New(
			client.WithHost(mockServer.URL),
			client.WithHTTPClient(mockServer.Client()),
			client.WithAPIVersion("1.41"),
		)
		require.NoError(t, err)
		defer cli.Close()

		eng := NewEngine(cli, nil, nil, nil, nil, nil, nil, nil, nil, t.TempDir())
		service := &models.Service{
			ID:        uuid.New(),
			ProjectID: uuid.New(),
			Name:      "web",
		}

		var logged []string
		res := eng.VerifyPreviousHealthyContainer(ctx, service, &prevCID, nil, func(p, s, m string) {
			logged = append(logged, m)
		})
		assert.False(t, res.Restored)
		require.Error(t, res.Error)
		assert.Contains(t, res.Error.Error(), "not running")
		assert.NotContains(t, strings.Join(logged, "\n"), "SAFETY INVARIANT ENFORCED")
	})
}

func TestExecuteServiceDeployment_StatefulRollbackRestoresPreviousWhenFailed(t *testing.T) {
	ctx := context.Background()
	tmpRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpRoot, "Dockerfile"), []byte("FROM alpine:3.19\n"), 0644))

	ownerID := uuid.New()
	projectID := uuid.New()
	serviceID := uuid.New()
	prevSDID := uuid.New()
	rollbackSDID := uuid.New()

	prevCID := "stateful-prev-cid-01"
	newCID := "stateful-bad-rollback-02"
	prevPort := 18910

	var mu sync.Mutex
	var events []string
	var prevRunning bool = true

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(image.InspectResponse{ID: "sha256:stateful-img"})
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/tag"):
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(r.URL.Path, "/volumes/create"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"Name": "data"})
		case strings.HasSuffix(r.URL.Path, "/networks"):
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode([]network.Summary{{Network: network.Network{ID: "net-s", Name: "forgelab-net-" + projectID.String()}}})
				return
			}
			w.WriteHeader(http.StatusCreated)
		case strings.Contains(r.URL.Path, "/networks/") && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(network.Inspect{Network: network.Network{ID: "net-s", Name: "forgelab-net-" + projectID.String()}})
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(container.CreateResponse{ID: newCID})
		case strings.HasSuffix(r.URL.Path, "/start"):
			id := extractContainerID(r.URL.Path, "/start")
			mu.Lock()
			if id == prevCID {
				prevRunning = true
			}
			events = append(events, "start:"+id)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/stop"):
			id := extractContainerID(r.URL.Path, "/stop")
			mu.Lock()
			if id == prevCID {
				prevRunning = false
			}
			events = append(events, "stop:"+id)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/"):
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			id := extractContainerID(r.URL.Path, "/json")
			w.Header().Set("Content-Type", "application/json")
			if id == prevCID {
				mu.Lock()
				isRun := prevRunning
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(container.InspectResponse{
					ID: prevCID,
					State: &container.State{
						Running: isRun,
						Status:  container.StateRunning,
						Health:  &container.Health{Status: container.Healthy},
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(container.InspectResponse{
				ID: newCID,
				State: &container.State{
					Running:  false,
					Status:   container.StateExited,
					ExitCode: 1,
				},
			})
		case strings.HasSuffix(r.URL.Path, "/logs"):
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	cli, cleanup := newDirectTestDockerClient(t, handler)
	defer cleanup()

	volCfg := []models.VolumeMountConfig{{Source: "data-vol", Target: "/data", Type: models.VolumeTypeNamed, ReadOnly: false}}
	project := &models.Project{
		ID:             projectID,
		OwnerID:        ownerID,
		Name:           "stateful-rollback-app",
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: tmpRoot,
		Status:         models.ProjectStatusDeploying,
	}
	service := &models.Service{
		ID:                         serviceID,
		ProjectID:                  projectID,
		Name:                       "app",
		Role:                       models.RoleFrontend,
		Classification:             models.ClassificationApplication,
		Volumes:                    volCfg,
		Status:                     models.ServiceStatusDeploying, // As set by RollbackServiceDeployment before queuing
		ContainerID:                strPtr(prevCID),
		HostPort:                   &prevPort,
		InternalPort:               8080,
		PublicExposed:              true,
		HealthStrategy:             models.HealthStrategyDocker,
		BuildStrategy:              models.BuildStrategyAuto,
		CurrentServiceDeploymentID: &prevSDID,
	}

	digestRef := "alpine:3.19"
	sd := &models.ServiceDeployment{
		ID:             rollbackSDID,
		ServiceID:      serviceID,
		ServiceName:    "app",
		DeployNumber:   3,
		Status:         models.DeployStatusQueued,
		ExecutionMode:  models.ExecutionModeReuseImage,
		ImageDigest:    &digestRef,
		ImageTag:       &digestRef,
		Volumes:        volCfg,
		InternalPort:   8080,
		HealthStrategy: models.HealthStrategyDocker,
		BuildStrategy:  models.BuildStrategyAuto,
	}

	depSvc := services.NewDeploymentService(nil)
	depSvc.SetGetServiceDeploymentForTest(func(ctx context.Context, sdID uuid.UUID) (*models.ServiceDeployment, error) {
		cp := *sd
		return &cp, nil
	})
	depSvc.SetUpdateServiceDeploymentStatusForTest(func(ctx context.Context, sdID uuid.UUID, newStatus string, reason *string) error {
		sd.Status = newStatus
		sd.FailureReason = reason
		return nil
	})

	svcSvc := services.NewServiceService(nil)
	svcSvc.SetGetServiceForTest(func(ctx context.Context, sID uuid.UUID) (*models.Service, error) {
		cp := *service
		return &cp, nil
	})
	svcSvc.SetListServicesForTest(func(ctx context.Context, pID uuid.UUID) ([]*models.Service, error) {
		cp := *service
		return []*models.Service{&cp}, nil
	})
	svcSvc.SetUpdateServiceStatusForTest(func(ctx context.Context, sID uuid.UUID, status string, cid, tag *string, hp *int) error {
		service.Status = status
		return nil
	})

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetServiceService(svcSvc)
	projSvc.SetProjectByIDForTest(func(ctx context.Context, pID uuid.UUID) (*models.Project, error) {
		cp := *project
		return &cp, nil
	})
	projSvc.SetUpdateProjectStatusForTest(func(ctx context.Context, pID uuid.UUID, status string) error {
		project.Status = status
		return nil
	})

	router := proxy.NewTrafficRouter()
	_, _, _ = router.EnsureRoute(serviceID, projectID, "app", "127.0.0.1", prevPort, prevCID)

	pv := security.NewPathValidator([]string{tmpRoot})
	pm := netpkg.NewPortManager(18920, 18980)
	eng := NewEngine(cli, projSvc, depSvc, nil, nil, nil, pm, pv, nil, t.TempDir())
	eng.SetServiceService(svcSvc)
	eng.SetTrafficRouter(router)
	defer eng.StopAllLogCollectors()

	err := eng.ExecuteServiceDeployment(ctx, rollbackSDID)
	require.Error(t, err)
	assert.Equal(t, models.DeployStatusFailed, sd.Status)
	assert.Equal(t, models.ServiceStatusRunning, service.Status, "service must be restored to Running after previous stateful container recovery")

	mu.Lock()
	defer mu.Unlock()
	stopPrevIdx := -1
	startNewIdx := -1
	restorePrevIdx := -1
	for i, ev := range events {
		if ev == "stop:"+prevCID && stopPrevIdx == -1 {
			stopPrevIdx = i
		}
		if ev == "start:"+newCID && startNewIdx == -1 {
			startNewIdx = i
		}
		if ev == "start:"+prevCID && restorePrevIdx == -1 {
			restorePrevIdx = i
		}
	}
	assert.NotEqual(t, -1, stopPrevIdx, "previous stateful container must be stopped during queued rollback")
	assert.NotEqual(t, -1, startNewIdx)
	assert.NotEqual(t, -1, restorePrevIdx, "previous stateful container must be restarted when rollback replacement fails")
	assert.Less(t, stopPrevIdx, startNewIdx)
	assert.Less(t, startNewIdx, restorePrevIdx)

	rt, hasRoute := router.GetRoute(serviceID)
	require.True(t, hasRoute)
	assert.Equal(t, prevCID, rt.ContainerID)
	_ = time.Second
}
