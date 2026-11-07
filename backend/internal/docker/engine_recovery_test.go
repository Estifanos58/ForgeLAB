package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
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
				_ = json.NewEncoder(w).Encode(types.ContainerJSON{
					ContainerJSONBase: &types.ContainerJSONBase{
						ID: "prev-container-1234567890",
						State: &types.ContainerState{
							Running:  true,
							Status:   "running",
							ExitCode: 0,
						},
					},
				})
			default:
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer mockServer.Close()

		cli, err := client.NewClientWithOpts(
			client.WithHost(mockServer.URL),
			client.WithHTTPClient(mockServer.Client()),
			client.WithVersion("1.41"),
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

		cli, err := client.NewClientWithOpts(
			client.WithHost(mockServer.URL),
			client.WithHTTPClient(mockServer.Client()),
			client.WithVersion("1.41"),
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
				_ = json.NewEncoder(w).Encode(types.ContainerJSON{
					ContainerJSONBase: &types.ContainerJSONBase{
						ID: "prev-container-crashed",
						State: &types.ContainerState{
							Running:  false,
							Status:   "exited",
							ExitCode: 137,
						},
					},
				})
			default:
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer mockServer.Close()

		cli, err := client.NewClientWithOpts(
			client.WithHost(mockServer.URL),
			client.WithHTTPClient(mockServer.Client()),
			client.WithVersion("1.41"),
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
				_ = json.NewEncoder(w).Encode(types.ContainerJSON{
					ContainerJSONBase: &types.ContainerJSONBase{
						ID: "prev-postgres-1",
						State: &types.ContainerState{
							Running: true,
							Status:  "running",
						},
					},
				})
			default:
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer mockServer.Close()

		cli, err := client.NewClientWithOpts(
			client.WithHost(mockServer.URL),
			client.WithHTTPClient(mockServer.Client()),
			client.WithVersion("1.41"),
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

		cli, err := client.NewClientWithOpts(
			client.WithHost(mockServer.URL),
			client.WithHTTPClient(mockServer.Client()),
			client.WithVersion("1.41"),
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
				_ = json.NewEncoder(w).Encode(types.ContainerJSON{
					ContainerJSONBase: &types.ContainerJSONBase{
						ID: "prev-redis-healthy",
						State: &types.ContainerState{
							Running: true,
							Status:  "running",
						},
					},
				})
			default:
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer mockServer.Close()

		cli, err := client.NewClientWithOpts(
			client.WithHost(mockServer.URL),
			client.WithHTTPClient(mockServer.Client()),
			client.WithVersion("1.41"),
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
