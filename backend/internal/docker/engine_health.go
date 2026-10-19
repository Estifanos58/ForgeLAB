package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"github.com/forgelab/backend/internal/agent"
	"github.com/forgelab/backend/internal/models"
)

func (e *Engine) fetchRecentContainerLogs(ctx context.Context, containerID string, tailLines int) string {
	if e.dockerClient == nil {
		return ""
	}
	logCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	reader, err := e.dockerClient.ContainerLogs(logCtx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       fmt.Sprintf("%d", tailLines),
	})
	if err != nil {
		return ""
	}
	defer reader.Close()

	var lines []string
	_ = DemuxDockerStream(logCtx, reader, func(stream string, line string) {
		clean := strings.TrimSpace(line)
		if clean != "" {
			lines = append(lines, fmt.Sprintf("[%s] %s", stream, clean))
		}
	})
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}
	return strings.Join(lines, "\n")
}

// buildContainerExitDiagnostics collects rich diagnostic state from an exited container.
func (e *Engine) buildContainerExitDiagnostics(ctx context.Context, containerID string, state *types.ContainerState) string {
	if state == nil {
		return "state=unknown"
	}
	var parts []string
	parts = append(parts, fmt.Sprintf("exit_code=%d", state.ExitCode))
	parts = append(parts, fmt.Sprintf("status=%s", state.Status))
	parts = append(parts, fmt.Sprintf("oom_killed=%t", state.OOMKilled))
	if state.Error != "" {
		parts = append(parts, fmt.Sprintf("error=%q", state.Error))
	}
	if state.FinishedAt != "" {
		parts = append(parts, fmt.Sprintf("finished_at=%s", state.FinishedAt))
	}
	if state.Health != nil {
		parts = append(parts, fmt.Sprintf("health_status=%s", state.Health.Status))
		if len(state.Health.Log) > 0 {
			var logSnippets []string
			startIdx := 0
			if len(state.Health.Log) > 3 {
				startIdx = len(state.Health.Log) - 3
			}
			for _, entry := range state.Health.Log[startIdx:] {
				cleanOut := strings.TrimSpace(entry.Output)
				if len(cleanOut) > 120 {
					cleanOut = cleanOut[:120] + "..."
				}
				logSnippets = append(logSnippets, fmt.Sprintf("[exit=%d] %s", entry.ExitCode, cleanOut))
			}
			parts = append(parts, fmt.Sprintf("recent_health_logs=[%s]", strings.Join(logSnippets, "; ")))
		}
	}
	recentLogs := e.fetchRecentContainerLogs(ctx, containerID, 15)
	if recentLogs != "" {
		parts = append(parts, fmt.Sprintf("recent_logs=\n%s", recentLogs))
	}
	return strings.Join(parts, ", ")
}

func isContainerNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if client.IsErrNotFound(err) {
		return true
	}
	type notFound interface {
		NotFound() bool
	}
	if nfe, ok := err.(notFound); ok && nfe.NotFound() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such container") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "code 404") ||
		strings.Contains(msg, "status 404")
}

// isInternalPortListening checks whether a specific internal TCP port is listening inside a container
// by querying kernel socket tables (/proc/net/tcp and /proc/net/tcp6).
// Returns (listening, supported):
// - listening: true if the port is in TCP_LISTEN state (0A)
// - supported: true if container exec and kernel table query succeeded (false if distroless/scratch container)
func (e *Engine) isInternalPortListening(ctx context.Context, containerID string, internalPort int) (bool, bool) {
	if internalPort <= 0 || e.dockerClient == nil {
		return false, false
	}
	hexPort := fmt.Sprintf("%04X", internalPort)
	execCfg := container.ExecOptions{
		Cmd:          []string{"sh", "-c", fmt.Sprintf("grep -qiE ':[0-9A-Fa-f]*%s .* 0A' /proc/net/tcp /proc/net/tcp6 2>/dev/null", hexPort)},
		AttachStdout: false,
		AttachStderr: false,
	}
	execID, err := e.dockerClient.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return false, false
	}
	startCfg := container.ExecStartOptions{}
	err = e.dockerClient.ContainerExecStart(ctx, execID.ID, startCfg)
	if err != nil {
		return false, false
	}
	inspect, err := e.dockerClient.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return false, false
	}
	if inspect.ExitCode == 0 {
		return true, true
	}
	if inspect.ExitCode == 1 {
		return false, true
	}
	return false, false
}

// verifyServiceHealth performs health verification through the correct container/network path
// working seamlessly whether ForgeLAB runs directly on the host or inside Docker Compose.
func (e *Engine) verifyServiceHealth(
	ctx context.Context,
	containerID string,
	internalPort int,
	hostPort *int,
	healthStrategy string,
	healthPath string,
	logFn func(msg string),
	errLogFn func(msg string),
	hcConfigs ...*models.HealthCheckConfig,
) (bool, string) {
	if healthStrategy == models.HealthStrategyNone {
		cJSON, err := e.dockerClient.ContainerInspect(ctx, containerID)
		if err == nil && cJSON.State != nil && cJSON.State.Running {
			return true, "container verified running"
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return false, "health check cancelled"
		}
		return false, "container is not running"
	}

	if healthPath == "" {
		healthPath = "/"
	}

	var hcConfig *models.HealthCheckConfig
	if len(hcConfigs) > 0 && hcConfigs[0] != nil {
		hcConfig = hcConfigs[0]
	}

	// Calculate overall health check budget respecting Compose timing
	expectedBudget := 60 * time.Second
	pollInterval := 1500 * time.Millisecond

	if hcConfig != nil {
		startPeriod := time.Duration(hcConfig.StartPeriodSeconds) * time.Second
		interval := time.Duration(hcConfig.IntervalSeconds) * time.Second
		if interval <= 0 {
			interval = 5 * time.Second
		}
		timeout := time.Duration(hcConfig.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		retries := hcConfig.Retries
		if retries <= 0 {
			retries = 3
		}
		calcBudget := startPeriod + time.Duration(retries)*(interval+timeout) + 15*time.Second
		if calcBudget > expectedBudget {
			expectedBudget = calcBudget
		}
		if interval < pollInterval {
			pollInterval = interval
		}
	}

	httpClient := &http.Client{Timeout: 2 * time.Second}

	startTime := time.Now()

	for attempt := 1; ; attempt++ {
		// 1. Check context cancellation / timeout
		if errors.Is(ctx.Err(), context.Canceled) {
			errLogFn("Health check cancelled: deployment context cancelled")
			return false, "health check cancelled"
		}
		if time.Since(startTime) >= expectedBudget {
			errLogFn(fmt.Sprintf("Health check timed out after %v", expectedBudget))
			return false, fmt.Sprintf("health check timed out after %v", expectedBudget)
		}

		// 2. Inspect container with bounded timeout
		inspectCtx, inspectCancel := context.WithTimeout(ctx, 5*time.Second)
		cJSON, err := e.dockerClient.ContainerInspect(inspectCtx, containerID)
		inspectCancel()
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				errLogFn("Health check cancelled: deployment context cancelled")
				return false, "health check cancelled"
			}
			if isContainerNotFoundError(err) {
				errLogFn(fmt.Sprintf("Container %s definitively not found during health check: %v", containerID, err))
				return false, fmt.Sprintf("container %s definitively not found: %v", containerID, err)
			}
			if time.Since(startTime) >= expectedBudget {
				errLogFn(fmt.Sprintf("Health check timed out after %v", expectedBudget))
				return false, fmt.Sprintf("health check timed out after %v", expectedBudget)
			}
			// Temporary Docker API / inspect error: do NOT report as container exit!
			logFn(fmt.Sprintf("Docker inspect attempt %d returned temporary error (will retry): %v", attempt, err))
			select {
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.Canceled) {
					errLogFn("Health check cancelled: deployment context cancelled")
					return false, "health check cancelled"
				}
				return false, "health check cancelled"
			case <-time.After(pollInterval):
				continue
			}
		}

		// 3. Check if container actually exited or entered a restart/crash loop
		if cJSON.State == nil || !cJSON.State.Running || cJSON.State.Restarting {
			if cJSON.State != nil && cJSON.State.Restarting {
				diag := e.buildContainerExitDiagnostics(ctx, containerID, cJSON.State)
				errLogFn(fmt.Sprintf("Container is in crash loop (state=restarting, restart_count=%d): %s", cJSON.RestartCount, diag))
				return false, fmt.Sprintf("container in crash loop (restart_count=%d): %s", cJSON.RestartCount, diag)
			}
			diag := e.buildContainerExitDiagnostics(ctx, containerID, cJSON.State)
			errLogFn(fmt.Sprintf("Container exited unexpectedly during health check: %s", diag))
			return false, fmt.Sprintf("container exited unexpectedly: %s", diag)
		}

		// Dynamically discover Docker native HealthConfig from cJSON if hcConfig was not supplied
		if hcConfig == nil && cJSON.Config != nil && cJSON.Config.Healthcheck != nil {
			ch := cJSON.Config.Healthcheck
			retries := ch.Retries
			if retries <= 0 {
				retries = 3
			}
			interval := ch.Interval
			if interval <= 0 {
				interval = 5 * time.Second
			}
			timeout := ch.Timeout
			if timeout <= 0 {
				timeout = 5 * time.Second
			}
			calcBudget := ch.StartPeriod + time.Duration(retries)*(interval+timeout) + 15*time.Second
			if calcBudget > expectedBudget {
				expectedBudget = calcBudget
			}
		}

		// 4. Check Docker-native health check
		hasNativeHealth := (cJSON.State.Health != nil) || (healthStrategy == models.HealthStrategyDocker)
		if hasNativeHealth {
			if cJSON.State.Health != nil {
				status := cJSON.State.Health.Status
				if status == "healthy" {
					logFn(fmt.Sprintf("Docker native health check reported healthy on attempt %d.", attempt))
					return true, "healthy"
				}
				if status == "unhealthy" {
					diag := ""
					if len(cJSON.State.Health.Log) > 0 {
						lastEntry := cJSON.State.Health.Log[len(cJSON.State.Health.Log)-1]
						diag = strings.TrimSpace(lastEntry.Output)
					}
					recentLogs := e.fetchRecentContainerLogs(ctx, containerID, 10)
					msg := fmt.Sprintf("Health verification failed: docker health status=unhealthy (attempt %d)", attempt)
					if diag != "" {
						msg = fmt.Sprintf("%s: %s", msg, diag)
					}
					if recentLogs != "" {
						msg = fmt.Sprintf("%s\nRecent container logs:\n%s", msg, recentLogs)
					}
					errLogFn(msg)
					return false, msg
				}
				// status == "starting" (or initializing): KEEP WAITING
				if attempt == 1 || attempt%5 == 0 {
					logFn(fmt.Sprintf("Docker native health check status is '%s' for %s (attempt %d)... waiting", status, containerID[:12], attempt))
				}
			} else {
				if attempt == 1 || attempt%5 == 0 {
					logFn(fmt.Sprintf("Waiting for Docker native health check monitor to initialize for %s (attempt %d)...", containerID[:12], attempt))
				}
			}

			// For Docker-native health check services, DO NOT fall through to TCP/HTTP!
			select {
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.Canceled) {
					errLogFn("Health check cancelled: deployment context cancelled")
					return false, "health check cancelled"
				}
				return false, "health check cancelled"
			case <-time.After(pollInterval):
				if time.Since(startTime) >= expectedBudget {
					msg := fmt.Sprintf("Health verification failed: docker health check timed out after %v while waiting to become healthy", expectedBudget)
					errLogFn(msg)
					return false, msg
				}
				continue
			}
		}

		// 5. Fallback TCP / HTTP verification for services WITHOUT a Docker-native health check
		var targets []string
		if cJSON.NetworkSettings != nil {
			for _, netSettings := range cJSON.NetworkSettings.Networks {
				if netSettings != nil && netSettings.IPAddress != "" && internalPort > 0 {
					targets = append(targets, fmt.Sprintf("%s:%d", netSettings.IPAddress, internalPort))
				}
			}
			if cJSON.NetworkSettings.IPAddress != "" && internalPort > 0 {
				targets = append(targets, fmt.Sprintf("%s:%d", cJSON.NetworkSettings.IPAddress, internalPort))
			}
		}

		isContainer := false
		if _, err := os.Stat("/.dockerenv"); err == nil {
			isContainer = true
		}
		if os.Getenv("FORGELAB_IN_DOCKER") != "" || os.Getenv("DOCKER_CONTAINER") != "" {
			isContainer = true
		}

		if hostPort != nil && *hostPort > 0 {
			targets = append(targets, fmt.Sprintf("127.0.0.1:%d", *hostPort))
			targets = append(targets, fmt.Sprintf("localhost:%d", *hostPort))
			if isContainer {
				targets = append(targets, fmt.Sprintf("host.docker.internal:%d", *hostPort))
				if cJSON.NetworkSettings != nil && cJSON.NetworkSettings.Gateway != "" {
					targets = append(targets, fmt.Sprintf("%s:%d", cJSON.NetworkSettings.Gateway, *hostPort))
				}
				if gw := agent.GetDefaultGateway(); gw != "" {
					targets = append(targets, fmt.Sprintf("%s:%d", gw, *hostPort))
				}
			}
		}

		var lastHTTPStatus int
		var lastProbeErr string

		if healthStrategy == models.HealthStrategyHTTP {
			// Explicit HTTP health check: MUST probe HTTP, MUST require 2xx or 3xx status, and MUST NOT fall back to TCP or socket inspection.
			for _, target := range targets {
				reqURL := fmt.Sprintf("http://%s%s", target, healthPath)
				reqCtx, reqCancel := context.WithTimeout(ctx, 2*time.Second)
				req, reqErr := http.NewRequestWithContext(reqCtx, http.MethodGet, reqURL, nil)
				if reqErr == nil {
					res, doErr := httpClient.Do(req)
					if doErr == nil {
						res.Body.Close()
						reqCancel()
						lastHTTPStatus = res.StatusCode
						if res.StatusCode >= 200 && res.StatusCode < 400 {
							logFn(fmt.Sprintf("Application readiness verified: HTTP %s returned %d on attempt %d.", target, res.StatusCode, attempt))
							return true, "healthy"
						}
						logFn(fmt.Sprintf("Application readiness check failed on %s: HTTP %d (expected 2xx/3xx status, attempt %d)... waiting", target, res.StatusCode, attempt))
					} else {
						reqCancel()
						lastProbeErr = doErr.Error()
					}
				} else {
					reqCancel()
					lastProbeErr = reqErr.Error()
				}
			}
		} else if healthStrategy == models.HealthStrategyTCP {
			// Explicit TCP health check: MUST verify TCP connection, MUST NOT probe HTTP endpoints.
			for _, target := range targets {
				conn, tcpErr := net.DialTimeout("tcp", target, 1*time.Second)
				if tcpErr == nil {
					conn.Close()
					logFn(fmt.Sprintf("Listening port verified: TCP connection established to %s (attempt %d).", target, attempt))
					return true, "healthy"
				}
				lastProbeErr = tcpErr.Error()
			}
		} else {
			// Auto / heuristic health check: try HTTP first, then TCP, then socket-level inspection.
			for _, target := range targets {
				reqURL := fmt.Sprintf("http://%s%s", target, healthPath)
				reqCtx, reqCancel := context.WithTimeout(ctx, 2*time.Second)
				req, reqErr := http.NewRequestWithContext(reqCtx, http.MethodGet, reqURL, nil)
				if reqErr == nil {
					res, doErr := httpClient.Do(req)
					if doErr == nil {
						res.Body.Close()
						reqCancel()
						if res.StatusCode >= 200 && res.StatusCode < 400 {
							logFn(fmt.Sprintf("Application readiness verified: HTTP %s returned %d on attempt %d.", target, res.StatusCode, attempt))
							return true, "healthy"
						}
					} else {
						reqCancel()
					}
				} else {
					reqCancel()
				}

				conn, tcpErr := net.DialTimeout("tcp", target, 1*time.Second)
				if tcpErr == nil {
					conn.Close()
					logFn(fmt.Sprintf("TCP health check passed via %s on attempt %d.", target, attempt))
					return true, "healthy"
				}
			}

			// Socket-level verification fallback: if external HTTP/TCP connection attempts failed (e.g. due to Docker network namespace isolation between backend container and target container), check whether the internal port is listening inside the container via docker exec
			if cJSON.State.Running && !cJSON.State.Restarting && cJSON.State.Health == nil {
				if internalPort > 0 {
					listening, supported := e.isInternalPortListening(ctx, containerID, internalPort)
					if listening {
						logFn(fmt.Sprintf("Listening port %d verified inside container (attempt %d).", internalPort, attempt))
						return true, "healthy"
					}
					if supported {
						if attempt == 1 || attempt%5 == 0 {
							logFn(fmt.Sprintf("Service port %d not yet listening inside container (attempt %d)... waiting", internalPort, attempt))
						}
					} else if attempt >= 8 && cJSON.RestartCount == 0 {
						logFn(fmt.Sprintf("Process startup verified: service container running stably without exits (attempt %d).", attempt))
						return true, "healthy"
					}
				} else if attempt >= 6 && cJSON.RestartCount == 0 {
					logFn(fmt.Sprintf("Process startup verified: service container running stably (attempt %d).", attempt))
					return true, "healthy"
				}
			}
		}

		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				errLogFn("Health check cancelled: deployment context cancelled")
				return false, "health check cancelled"
			}
			var msg string
			if healthStrategy == models.HealthStrategyHTTP && lastHTTPStatus > 0 {
				msg = fmt.Sprintf("HTTP health verification failed: received HTTP %d on endpoint '%s' (expected 2xx/3xx status code)", lastHTTPStatus, healthPath)
			} else if healthStrategy == models.HealthStrategyHTTP && lastProbeErr != "" {
				msg = fmt.Sprintf("HTTP health verification failed: %s", lastProbeErr)
			} else if healthStrategy == models.HealthStrategyTCP && lastProbeErr != "" {
				msg = fmt.Sprintf("TCP health verification failed on port %d: %s", internalPort, lastProbeErr)
			} else {
				msg = fmt.Sprintf("Health verification timed out after %v", expectedBudget)
			}
			errLogFn(msg)
			return false, msg
		case <-time.After(pollInterval):
			if time.Since(startTime) >= expectedBudget {
				var msg string
				if healthStrategy == models.HealthStrategyHTTP && lastHTTPStatus > 0 {
					msg = fmt.Sprintf("HTTP health verification failed: received HTTP %d on endpoint '%s' (expected 2xx/3xx status code)", lastHTTPStatus, healthPath)
				} else if healthStrategy == models.HealthStrategyHTTP && lastProbeErr != "" {
					msg = fmt.Sprintf("HTTP health verification failed: %s", lastProbeErr)
				} else if healthStrategy == models.HealthStrategyTCP && lastProbeErr != "" {
					msg = fmt.Sprintf("TCP health verification failed on port %d: %s", internalPort, lastProbeErr)
				} else {
					msg = fmt.Sprintf("Health verification timed out after %v", expectedBudget)
				}
				errLogFn(msg)
				return false, msg
			}
			continue
		}
	}
}

// buildServiceDeploymentTiers partitions service deployments into dependency execution tiers (DAG)
