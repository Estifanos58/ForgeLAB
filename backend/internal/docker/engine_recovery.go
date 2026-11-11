package docker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
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

// RestorePreviousHealthyContainer attempts to restart and verify the previous healthy container.
// It confirms that the container is actually running before claiming recovery.
// If the restart fails or the container is not confirmed running, it returns Restored=false with detailed diagnostics.
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

	// Confirm that previous container is actually running
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

	// Resume runtime log collection for the restored container
	e.StartRuntimeLogCollector(RuntimeCollectorParams{
		ContainerID:         cid,
		ProjectID:           service.ProjectID,
		ServiceDeploymentID: previousDeploymentID,
		ServiceID:           &service.ID,
		ServiceName:         service.Name,
	})

	successMsg := fmt.Sprintf("SAFETY INVARIANT ENFORCED: New deployment for '%s' failed, but previous healthy container %s was successfully verified running and active.", service.Name, shortID)
	emitLog(models.LogPhaseHealth, models.LogStreamSystem, successMsg)

	return RestorePreviousContainerResult{
		Attempted: true,
		Restored:  true,
		Message:   successMsg,
	}
}
