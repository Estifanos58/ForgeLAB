package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/forgelab/backend/internal/models"
)

// RestorePreviousContainerResult contains detailed diagnostics about the recovery attempt.
type RestorePreviousContainerResult struct {
	Attempted bool
	Restored  bool
	Error     error
	Message   string
}

func formatHealthDetail(healthOutput string) string {
	trimmed := strings.TrimSpace(healthOutput)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) > 200 {
		trimmed = trimmed[:200] + "..."
	}
	return fmt.Sprintf(": %s", trimmed)
}

func (e *Engine) ensureRestoredServiceRoute(ctx context.Context, service *models.Service, containerID string, hostPort *int) {
	if e == nil || e.trafficRouter == nil || service == nil || containerID == "" || hostPort == nil || *hostPort <= 0 {
		return
	}
	if service.Classification == models.ClassificationJob ||
		service.Classification == models.ClassificationInfrastructure ||
		service.Classification == models.ClassificationWorker {
		return
	}

	targetHost := strings.TrimSpace(e.GetAppHostIP())
	if targetHost == "" || targetHost == "0.0.0.0" || targetHost == "::" {
		targetHost = "127.0.0.1"
	}
	expectedUpstreamURL := fmt.Sprintf("http://%s:%d", targetHost, *hostPort)

	if existing, ok := e.trafficRouter.GetRoute(service.ID); ok {
		if existing.ContainerID == containerID && existing.HostPort == *hostPort && existing.UpstreamURL == expectedUpstreamURL {
			return
		}
		restored := existing
		restored.ProjectID = service.ProjectID
		restored.ServiceName = service.Name
		restored.ContainerID = containerID
		restored.HostPort = *hostPort
		restored.UpstreamURL = expectedUpstreamURL
		if err := e.trafficRouter.RestoreRoute(restored); err != nil {
			slog.Warn("failed to restore upstream route for recovered service", "service_id", service.ID, "error", err)
			return
		}
		if e.onRouteChange != nil {
			e.onRouteChange(ctx)
		}
		return
	}

	if _, created, err := e.trafficRouter.EnsureRoute(service.ID, service.ProjectID, service.Name, targetHost, *hostPort, containerID); err != nil {
		slog.Warn("failed to ensure upstream route for recovered service", "service_id", service.ID, "error", err)
	} else if created && e.onRouteChange != nil {
		e.onRouteChange(ctx)
	}
}

// VerifyPreviousHealthyContainer confirms an untouched previous container is still running
// (and healthy when a Docker healthcheck is configured) and that its proxy route and runtime log
// collector are active before reporting previous-release preservation as successful.
func (e *Engine) VerifyPreviousHealthyContainer(
	ctx context.Context,
	service *models.Service,
	previousContainerID *string,
	previousDeploymentID *uuid.UUID,
	emitLog func(phase, stream, message string),
) RestorePreviousContainerResult {
	if previousContainerID == nil || *previousContainerID == "" || service == nil {
		return RestorePreviousContainerResult{
			Attempted: false,
			Restored:  false,
			Message:   "no previous healthy container to verify",
		}
	}
	if e == nil || e.dockerClient == nil {
		err := errors.New("docker client is not configured")
		return RestorePreviousContainerResult{
			Attempted: true,
			Restored:  false,
			Error:     err,
			Message:   err.Error(),
		}
	}
	if emitLog == nil {
		emitLog = func(phase, stream, message string) {}
	}

	cid := *previousContainerID
	shortID := cid
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}

	verifyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	inspectRes, err := e.dockerClient.ContainerInspect(verifyCtx, cid, client.ContainerInspectOptions{})
	if err != nil {
		errDetail := fmt.Sprintf("CRITICAL RECOVERY FAILURE: Previous container %s for service '%s' could not be verified: %v", shortID, service.Name, err)
		emitLog(models.LogPhaseHealth, models.LogStreamStderr, errDetail)
		return RestorePreviousContainerResult{
			Attempted: true,
			Restored:  false,
			Error:     err,
			Message:   errDetail,
		}
	}
	inspect := inspectRes.Container
	if inspect.State == nil || !inspect.State.Running {
		statusStr := "unknown"
		exitCode := -1
		if inspect.State != nil {
			statusStr = string(inspect.State.Status)
			exitCode = inspect.State.ExitCode
		}
		errDetail := fmt.Sprintf("CRITICAL RECOVERY FAILURE: Previous container %s for service '%s' is no longer running (status=%s, exit_code=%d)", shortID, service.Name, statusStr, exitCode)
		emitLog(models.LogPhaseHealth, models.LogStreamStderr, errDetail)
		return RestorePreviousContainerResult{
			Attempted: true,
			Restored:  false,
			Error:     fmt.Errorf("previous container is not running (status=%s, exit_code=%d)", statusStr, exitCode),
			Message:   errDetail,
		}
	}
	if inspect.State.OOMKilled {
		errDetail := fmt.Sprintf("CRITICAL RECOVERY FAILURE: Previous container %s for service '%s' was terminated by kernel OOM killer", shortID, service.Name)
		emitLog(models.LogPhaseHealth, models.LogStreamStderr, errDetail)
		return RestorePreviousContainerResult{
			Attempted: true,
			Restored:  false,
			Error:     fmt.Errorf("previous container %s was OOM killed", shortID),
			Message:   errDetail,
		}
	}
	if inspect.State.Health != nil && inspect.State.Health.Status == container.Unhealthy {
		healthOut := extractLastHealthOutput(inspect.State.Health)
		errDetail := fmt.Sprintf("CRITICAL RECOVERY FAILURE: Previous container %s for service '%s' is unhealthy (status=unhealthy%s)", shortID, service.Name, formatHealthDetail(healthOut))
		emitLog(models.LogPhaseHealth, models.LogStreamStderr, errDetail)
		return RestorePreviousContainerResult{
			Attempted: true,
			Restored:  false,
			Error:     fmt.Errorf("previous container health status is unhealthy%s", formatHealthDetail(healthOut)),
			Message:   errDetail,
		}
	}

	e.ensureRestoredServiceRoute(verifyCtx, service, cid, service.HostPort)
	e.ensureRuntimeCollectorRunning(verifyCtx, service, cid)

	successMsg := fmt.Sprintf("SAFETY INVARIANT ENFORCED: New deployment for '%s' failed, and previous healthy container %s was verified active.", service.Name, shortID)
	emitLog(models.LogPhaseHealth, models.LogStreamSystem, successMsg)
	return RestorePreviousContainerResult{
		Attempted: true,
		Restored:  true,
		Message:   successMsg,
	}
}

// RestorePreviousHealthyContainer attempts to restart and verify the previous healthy container.
// It confirms that the container is actually running (and healthy when a Docker healthcheck is
// configured) before claiming recovery or restoring upstream routing.
// If the restart fails or the container fails verification, it returns Restored=false with detailed diagnostics.
func (e *Engine) RestorePreviousHealthyContainer(
	ctx context.Context,
	service *models.Service,
	previousContainerID *string,
	previousDeploymentID *uuid.UUID,
	emitLog func(phase, stream, message string),
) RestorePreviousContainerResult {
	if previousContainerID == nil || *previousContainerID == "" {
		return RestorePreviousContainerResult{
			Attempted: false,
			Restored:  false,
			Message:   "no previous healthy container to restore",
		}
	}
	if e == nil || e.dockerClient == nil {
		err := errors.New("docker client is not configured")
		return RestorePreviousContainerResult{
			Attempted: true,
			Restored:  false,
			Error:     err,
			Message:   err.Error(),
		}
	}
	if emitLog == nil {
		emitLog = func(phase, stream, message string) {}
	}

	cid := *previousContainerID
	shortID := cid
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}

	emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Attempting to restore previous container %s for service '%s'...", shortID, service.Name))

	restartCtx, restartCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer restartCancel()

	if _, err := e.dockerClient.ContainerStart(restartCtx, cid, client.ContainerStartOptions{}); err != nil {
		errDetail := fmt.Sprintf("CRITICAL RECOVERY FAILURE: Failed to restart previous container %s for service '%s': %v", shortID, service.Name, err)
		emitLog(models.LogPhaseHealth, models.LogStreamStderr, errDetail)
		slog.Error("previous container restoration failed", "service", service.Name, "container_id", cid, "error", err)
		return RestorePreviousContainerResult{
			Attempted: true,
			Restored:  false,
			Error:     err,
			Message:   errDetail,
		}
	}

	// Confirm that previous container is actually running and healthy (poll if health check is starting)
	for {
		inspectRes, err := e.dockerClient.ContainerInspect(restartCtx, cid, client.ContainerInspectOptions{})
		if err != nil {
			errDetail := fmt.Sprintf("CRITICAL RECOVERY FAILURE: Failed to inspect restored container %s for service '%s': %v", shortID, service.Name, err)
			emitLog(models.LogPhaseHealth, models.LogStreamStderr, errDetail)
			return RestorePreviousContainerResult{
				Attempted: true,
				Restored:  false,
				Error:     err,
				Message:   errDetail,
			}
		}
		inspect := inspectRes.Container

		if inspect.State == nil || !inspect.State.Running {
			statusStr := "unknown"
			exitCode := -1
			if inspect.State != nil {
				statusStr = string(inspect.State.Status)
				exitCode = inspect.State.ExitCode
			}
			errDetail := fmt.Sprintf("CRITICAL RECOVERY FAILURE: Restored container %s for service '%s' is not running (status=%s, exit_code=%d)", shortID, service.Name, statusStr, exitCode)
			emitLog(models.LogPhaseHealth, models.LogStreamStderr, errDetail)
			return RestorePreviousContainerResult{
				Attempted: true,
				Restored:  false,
				Error:     fmt.Errorf("restored container is not running (status=%s, exit_code=%d)", statusStr, exitCode),
				Message:   errDetail,
			}
		}

		if inspect.State.OOMKilled {
			errDetail := fmt.Sprintf("CRITICAL RECOVERY FAILURE: Restored container %s for service '%s' was terminated by kernel OOM killer", shortID, service.Name)
			emitLog(models.LogPhaseHealth, models.LogStreamStderr, errDetail)
			return RestorePreviousContainerResult{
				Attempted: true,
				Restored:  false,
				Error:     fmt.Errorf("restored container %s was OOM killed", shortID),
				Message:   errDetail,
			}
		}

		if inspect.State.Health == nil {
			break
		}

		healthStatus := inspect.State.Health.Status
		if healthStatus == container.Unhealthy {
			healthOut := extractLastHealthOutput(inspect.State.Health)
			errDetail := fmt.Sprintf("CRITICAL RECOVERY FAILURE: Restored container %s for service '%s' failed health check (status=unhealthy%s)", shortID, service.Name, formatHealthDetail(healthOut))
			emitLog(models.LogPhaseHealth, models.LogStreamStderr, errDetail)
			return RestorePreviousContainerResult{
				Attempted: true,
				Restored:  false,
				Error:     fmt.Errorf("restored container health status is unhealthy%s", formatHealthDetail(healthOut)),
				Message:   errDetail,
			}
		}

		if healthStatus != container.Starting {
			// Healthy or healthcheck disabled ("none" / "")
			break
		}

		select {
		case <-restartCtx.Done():
			errDetail := fmt.Sprintf("CRITICAL RECOVERY FAILURE: Restored container %s for service '%s' did not become healthy before verification timeout (health_status=%s)", shortID, service.Name, healthStatus)
			emitLog(models.LogPhaseHealth, models.LogStreamStderr, errDetail)
			return RestorePreviousContainerResult{
				Attempted: true,
				Restored:  false,
				Error:     fmt.Errorf("restored container did not become healthy before timeout (health_status=%s): %w", healthStatus, restartCtx.Err()),
				Message:   errDetail,
			}
		case <-time.After(150 * time.Millisecond):
		}
	}

	// Ensure upstream traffic route and runtime log collector are restored only after verification succeeds
	e.ensureRestoredServiceRoute(restartCtx, service, cid, service.HostPort)

	redactor := e.buildLogRedactor(restartCtx, service.ProjectID, &service.ID, nil)
	e.StartRuntimeLogCollector(RuntimeCollectorParams{
		ContainerID:         cid,
		ProjectID:           service.ProjectID,
		ServiceDeploymentID: previousDeploymentID,
		ServiceID:           &service.ID,
		ServiceName:         service.Name,
		Redactor:            redactor,
	})

	successMsg := fmt.Sprintf("SAFETY INVARIANT ENFORCED: New deployment for '%s' failed, but previous healthy container %s was successfully verified running and active.", service.Name, shortID)
	emitLog(models.LogPhaseHealth, models.LogStreamSystem, successMsg)

	return RestorePreviousContainerResult{
		Attempted: true,
		Restored:  true,
		Message:   successMsg,
	}
}
