package docker

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
)

// 1. Docker ContainerInspect returns context.Canceled → must report cancellation, not container exit.
func TestRegression_HealthCheck_InspectContextCanceled_ReportsCancellation(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ContainerJSON{
			ContainerJSONBase: &types.ContainerJSONBase{
				State: &types.ContainerState{
					Running: true,
				},
			},
		})
	})

	cli, cleanup := newTestDockerClient(t, handler)
	defer cleanup()

	eng := &Engine{dockerClient: cli}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel context

	var errLogs []string
	var infoLogs []string

	healthy, reason := eng.verifyServiceHealth(
		ctx,
		"test-c-cancel",
		5432,
		nil,
		models.HealthStrategyDocker,
		"/",
		func(msg string) { infoLogs = append(infoLogs, msg) },
		func(msg string) { errLogs = append(errLogs, msg) },
	)

	assert.False(t, healthy, "cancelled health check must return healthy=false")
	assert.Contains(t, reason, "cancelled", "reason must report cancellation, not container crash")
	assert.NotContains(t, reason, "exited unexpectedly", "must not report container exited unexpectedly")

	foundCancelLog := false
	for _, l := range errLogs {
		if strings.Contains(strings.ToLower(l), "cancelled") {
			foundCancelLog = true
			break
		}
	}
	assert.True(t, foundCancelLog, "error log must mention cancellation")
}

// 2. Docker ContainerInspect returns a temporary API error → health checker retries.
func TestRegression_HealthCheck_InspectTemporaryError_Retries(t *testing.T) {
	var inspectCalls atomic.Int32

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			call := inspectCalls.Add(1)
			if call <= 2 {
				// Temporary Docker daemon API error
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"daemon is temporarily busy"}`))
				return
			}

			// Success on 3rd attempt
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{
						Running: true,
						Health: &types.Health{
							Status: "healthy",
						},
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	cli, cleanup := newTestDockerClient(t, handler)
	defer cleanup()

	eng := &Engine{dockerClient: cli}

	var infoLogs []string
	var errLogs []string

	healthy, reason := eng.verifyServiceHealth(
		context.Background(),
		"test-retry-inspect",
		6379,
		nil,
		models.HealthStrategyDocker,
		"/",
		func(msg string) { infoLogs = append(infoLogs, msg) },
		func(msg string) { errLogs = append(errLogs, msg) },
	)

	assert.True(t, healthy, "health check must succeed after retrying past temporary API errors")
	assert.Equal(t, "healthy", reason)
	assert.GreaterOrEqual(t, inspectCalls.Load(), int32(3), "must have retried at least 3 times")

	foundRetryLog := false
	for _, l := range infoLogs {
		if strings.Contains(l, "temporary error (will retry)") {
			foundRetryLog = true
			break
		}
	}
	assert.True(t, foundRetryLog, "info logs must mention temporary error and retry")
}

// 3. Container is actually stopped → report exit state and diagnostics.
func TestRegression_HealthCheck_ContainerActuallyStopped_ReportsDiagnostics(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/logs") {
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			// Return a sample stderr log line
			logMsg := "FATAL: could not load config file\n"
			header := []byte{2, 0, 0, 0, 0, 0, 0, byte(len(logMsg))}
			_, _ = w.Write(append(header, []byte(logMsg)...))
			return
		}

		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{
						Running:    false,
						ExitCode:   137,
						Status:     "exited",
						OOMKilled:  true,
						Error:      "out of memory kill",
						FinishedAt: "2026-10-07T08:00:00Z",
						Health: &types.Health{
							Status: "unhealthy",
							Log: []*types.HealthcheckResult{
								{Output: "health check timeout\n", ExitCode: 1},
							},
						},
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	cli, cleanup := newTestDockerClient(t, handler)
	defer cleanup()

	eng := &Engine{dockerClient: cli}

	var errLogs []string
	healthy, reason := eng.verifyServiceHealth(
		context.Background(),
		"test-exited",
		5432,
		nil,
		models.HealthStrategyDocker,
		"/",
		func(msg string) {},
		func(msg string) { errLogs = append(errLogs, msg) },
	)

	assert.False(t, healthy)
	assert.Contains(t, reason, "container exited unexpectedly")
	assert.Contains(t, reason, "exit_code=137")
	assert.Contains(t, reason, "oom_killed=true")
	assert.Contains(t, reason, "out of memory kill")
	assert.Contains(t, reason, "finished_at=2026-10-07T08:00:00Z")
	assert.Contains(t, reason, "health check timeout")
}

// 4. Docker health status starting → never promote to healthy.
func TestRegression_HealthCheck_DockerHealthStarting_NeverPromotesToHealthy(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{
						Running: true,
						Health: &types.Health{
							Status: "starting",
						},
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	cli, cleanup := newTestDockerClient(t, handler)
	defer cleanup()

	eng := &Engine{dockerClient: cli}

	// Use a short context to avoid long test wait
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	healthy, reason := eng.verifyServiceHealth(
		ctx,
		"test-starting",
		5432,
		nil,
		models.HealthStrategyDocker,
		"/",
		func(msg string) {},
		func(msg string) {},
	)

	assert.False(t, healthy, "starting state must NEVER be promoted to healthy")
	assert.False(t, reason == "healthy")
}

// 5. Docker health status unhealthy → deployment fails and preserves diagnostics.
func TestRegression_HealthCheck_DockerHealthUnhealthy_PreservesDiagnostics(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/logs") {
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			logMsg := "FATAL: role 'postgres' does not exist\n"
			header := []byte{2, 0, 0, 0, 0, 0, 0, byte(len(logMsg))}
			_, _ = w.Write(append(header, []byte(logMsg)...))
			return
		}

		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{
						Running: true,
						Health: &types.Health{
							Status: "unhealthy",
							Log: []*types.HealthcheckResult{
								{Output: "pg_isready failed: connection refused\n", ExitCode: 2},
							},
						},
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	cli, cleanup := newTestDockerClient(t, handler)
	defer cleanup()

	eng := &Engine{dockerClient: cli}

	var errLogs []string
	healthy, reason := eng.verifyServiceHealth(
		context.Background(),
		"test-unhealthy",
		5432,
		nil,
		models.HealthStrategyDocker,
		"/",
		func(msg string) {},
		func(msg string) { errLogs = append(errLogs, msg) },
	)

	assert.False(t, healthy)
	assert.Contains(t, reason, "docker health status=unhealthy")
	assert.Contains(t, reason, "pg_isready failed: connection refused")
	assert.Contains(t, reason, "FATAL: role 'postgres' does not exist")
}

// 6. Docker health status healthy → deployment succeeds.
func TestRegression_HealthCheck_DockerHealthHealthy_Succeeds(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{
						Running: true,
						Health: &types.Health{
							Status: "healthy",
						},
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	cli, cleanup := newTestDockerClient(t, handler)
	defer cleanup()

	eng := &Engine{dockerClient: cli}

	healthy, reason := eng.verifyServiceHealth(
		context.Background(),
		"test-healthy",
		6379,
		nil,
		models.HealthStrategyDocker,
		"/",
		func(msg string) {},
		func(msg string) {},
	)

	assert.True(t, healthy)
	assert.Equal(t, "healthy", reason)
}

// 7. A native Docker healthcheck cannot be bypassed by TCP/HTTP success.
func TestRegression_HealthCheck_NativeDockerHealthcheckCannotBeBypassedByTCPHTTP(t *testing.T) {
	// Spin up a live TCP/HTTP server on a localhost port that returns 200 OK
	tcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("HTTP OK"))
	}))
	defer tcpServer.Close()

	port := tcpServer.Listener.Addr().(*net.TCPAddr).Port

	// Mock Docker daemon returning "starting"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{
						Running: true,
						Health: &types.Health{
							Status: "starting",
						},
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	cli, cleanup := newTestDockerClient(t, handler)
	defer cleanup()

	eng := &Engine{dockerClient: cli}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	healthy, _ := eng.verifyServiceHealth(
		ctx,
		"test-cannot-bypass",
		port,
		&port,
		models.HealthStrategyDocker, // docker strategy
		"/",
		func(msg string) {},
		func(msg string) {},
	)

	assert.False(t, healthy, "native Docker healthcheck MUST NOT be bypassed even if TCP/HTTP on hostPort succeeds")
}

// 8. Redis/Postgres health checks respect startup timing.
func TestRegression_HealthCheck_RespectsStartupTiming(t *testing.T) {
	var attempts atomic.Int32

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			attempt := attempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if attempt < 3 {
				// Simulating startup initialization during start_period
				_ = json.NewEncoder(w).Encode(types.ContainerJSON{
					ContainerJSONBase: &types.ContainerJSONBase{
						State: &types.ContainerState{
							Running: true,
							Health: &types.Health{
								Status: "starting",
							},
						},
					},
				})
				return
			}
			// Became healthy on attempt 3
			_ = json.NewEncoder(w).Encode(types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{
						Running: true,
						Health: &types.Health{
							Status: "healthy",
						},
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	cli, cleanup := newTestDockerClient(t, handler)
	defer cleanup()

	eng := &Engine{dockerClient: cli}

	pgHC := &models.HealthCheckConfig{
		StartPeriodSeconds: 45,
		IntervalSeconds:    1,
		TimeoutSeconds:     2,
		Retries:            10,
	}

	healthy, reason := eng.verifyServiceHealth(
		context.Background(),
		"test-pg-timing",
		5432,
		nil,
		models.HealthStrategyDocker,
		"/",
		func(msg string) {},
		func(msg string) {},
		pgHC,
	)

	assert.True(t, healthy)
	assert.Equal(t, "healthy", reason)
	assert.GreaterOrEqual(t, attempts.Load(), int32(3))
}

// 9. Default nofile configuration does not force Redis down to 2048 unless explicitly configured.
func TestRegression_TrustBoundary_NofileConfiguration(t *testing.T) {
	// 9a. Default: no ulimits configured -> nil, preserving runtime/Docker defaults
	optsDefault := ContainerSecurityOptions{
		TargetCpuMillicores: 1000,
		TargetMemoryMB:      1024,
	}
	cfgDefault, err := ValidateAndBuildSecureHostConfig(optsDefault)
	require.NoError(t, err)
	assert.Nil(t, cfgDefault.Resources.Ulimits, "default hostConfig must NOT set ulimits, preserving Docker daemon defaults for Redis")

	// 9b. Configurable nofile within safe range
	optsCustom := ContainerSecurityOptions{
		TargetNofileSoft: 1024,
		TargetNofileHard: 16384,
	}
	cfgCustom, err := ValidateAndBuildSecureHostConfig(optsCustom)
	require.NoError(t, err)
	require.Len(t, cfgCustom.Resources.Ulimits, 1)
	assert.Equal(t, "nofile", cfgCustom.Resources.Ulimits[0].Name)
	assert.Equal(t, int64(1024), cfgCustom.Resources.Ulimits[0].Soft)
	assert.Equal(t, int64(16384), cfgCustom.Resources.Ulimits[0].Hard)

	// 9c. Invalid nofile: below MinNofileLimit (1024)
	optsTooLow := ContainerSecurityOptions{
		TargetNofileSoft: 512,
		TargetNofileHard: 1024,
	}
	_, err = ValidateAndBuildSecureHostConfig(optsTooLow)
	assert.ErrorIs(t, err, ErrNofileOutOfRange)

	// 9d. Invalid nofile: exceeds MaxNofileLimit (65536)
	optsTooHigh := ContainerSecurityOptions{
		TargetNofileSoft: 1024,
		TargetNofileHard: 100000,
	}
	_, err = ValidateAndBuildSecureHostConfig(optsTooHigh)
	assert.ErrorIs(t, err, ErrNofileOutOfRange)

	// 9e. Invalid nofile: soft > hard
	optsInverted := ContainerSecurityOptions{
		TargetNofileSoft: 4096,
		TargetNofileHard: 2048,
	}
	_, err = ValidateAndBuildSecureHostConfig(optsInverted)
	assert.ErrorIs(t, err, ErrNofileOutOfRange)
}

// 10. Existing security-boundary tests still pass.
func TestRegression_TrustBoundary_SecurityModelPreserved(t *testing.T) {
	opts := ContainerSecurityOptions{
		TargetCpuMillicores: 1000,
		TargetMemoryMB:      1024,
	}
	cfg, err := ValidateAndBuildSecureHostConfig(opts)
	require.NoError(t, err)

	assert.False(t, cfg.Privileged, "privileged mode MUST be disabled")
	assert.Contains(t, cfg.SecurityOpt, "no-new-privileges:true", "must have no-new-privileges:true")
	assert.Equal(t, []string{"ALL"}, []string(cfg.CapDrop), "must drop ALL capabilities")

	// Verify only minimum required safe capabilities for user privilege switching (gosu, su-exec)
	expectedSafeCaps := []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID"}
	assert.ElementsMatch(t, expectedSafeCaps, cfg.CapAdd)

	// Host network must be rejected
	_, err = ValidateAndBuildSecureHostConfig(ContainerSecurityOptions{NetworkMode: "host"})
	assert.ErrorIs(t, err, ErrHostNetworkingRejected)

	// Docker socket mount must be rejected
	_, err = ValidateAndBuildSecureHostConfig(ContainerSecurityOptions{Binds: []string{"/var/run/docker.sock:/var/run/docker.sock"}})
	assert.ErrorIs(t, err, ErrDockerSocketMountRejected)

	// Sensitive host mount must be rejected
	_, err = ValidateAndBuildSecureHostConfig(ContainerSecurityOptions{Binds: []string{"/proc:/proc"}})
	assert.ErrorIs(t, err, ErrSensitiveHostMountRejected)
}
