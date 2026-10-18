package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/logging"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
	ws "github.com/forgelab/backend/internal/websocket"
)

// RuntimeCollectorParams holds parameters for attaching a continuous runtime log collector.
type RuntimeCollectorParams struct {
	ContainerID         string
	ProjectID           uuid.UUID
	DeploymentID        *uuid.UUID // release deployment ID or legacy deployment ID
	ServiceDeploymentID *uuid.UUID // nil for legacy deployment
	ServiceID           *uuid.UUID // nil for legacy deployment
	ServiceName         string     // for service badge
	Redactor            *logging.LogRedactor
	Since               time.Time
}

// buildLogRedactor constructs a LogRedactor loaded with decrypted secrets for the project/service.
func (e *Engine) buildLogRedactor(ctx context.Context, projectID uuid.UUID, serviceID *uuid.UUID, envSnapshot json.RawMessage) *logging.LogRedactor {
	redactor := logging.NewLogRedactor()
	if e.secretService == nil {
		return redactor
	}
	_, _, allSecrets, err := e.secretService.GetBuildVariables(ctx, projectID, serviceID, envSnapshot)
	if err == nil {
		redactor.SetSecrets(allSecrets)
	}
	return redactor
}

// emitRuntimeLog redacts, persists to PostgreSQL with phase=runtime, and publishes to all appropriate WebSocket channels.
func (e *Engine) emitRuntimeLog(p RuntimeCollectorParams, instanceID, stream, message string) {
	if e.deploymentService == nil {
		return
	}
	const maxLogMsgBytes = 64 * 1024
	if len(message) > maxLogMsgBytes {
		message = message[:maxLogMsgBytes] + " ... [truncated: exceeded 64KB log size limit]"
	}
	redactedMsg := message
	if p.Redactor != nil {
		redactedMsg = p.Redactor.Redact(message)
	}

	persistCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	persistedLog, err := e.deploymentService.AddDeploymentServiceLog(
		persistCtx,
		p.DeploymentID,
		p.ServiceDeploymentID,
		p.ServiceID,
		models.LogPhaseRuntime,
		stream,
		redactedMsg,
	)
	if err != nil {
		if instanceID != "" {
			e.getCollectorRegistry().RecordPersistenceError(instanceID, err)
		}
		cid := p.ContainerID
		if len(cid) > 12 {
			cid = cid[:12]
		}
		slog.Error("failed to persist runtime log", "container", cid, "error", err)
		return
	}

	if e.wsHub != nil {
		data := map[string]interface{}{
			"id":        persistedLog.ID,
			"sequence":  persistedLog.ID,
			"timestamp": persistedLog.Timestamp.Format(time.RFC3339Nano),
			"phase":     persistedLog.Phase,
			"stream":    persistedLog.Stream,
			"message":   persistedLog.Message,
		}
		if p.ServiceDeploymentID != nil {
			data["service_deployment_id"] = p.ServiceDeploymentID.String()
		}
		if p.ServiceID != nil {
			data["service_id"] = p.ServiceID.String()
		}
		if p.ServiceName != "" {
			data["service_name"] = p.ServiceName
		}
		if p.DeploymentID != nil {
			data["deployment_id"] = p.DeploymentID.String()
		}

		if p.ServiceDeploymentID != nil {
			sdChannel := fmt.Sprintf("service-deployment:%s", p.ServiceDeploymentID.String())
			_ = e.wsHub.PublishEvent(sdChannel, &ws.EventMessage{
				Type:     "log",
				Channel:  sdChannel,
				Sequence: persistedLog.ID,
				Data:     data,
			})
			if p.DeploymentID != nil && p.ServiceID != nil {
				serviceChannel := fmt.Sprintf("deployment:%s:service:%s", p.DeploymentID.String(), p.ServiceID.String())
				_ = e.wsHub.PublishEvent(serviceChannel, &ws.EventMessage{
					Type:     "log",
					Channel:  serviceChannel,
					Sequence: persistedLog.ID,
					Data:     data,
				})
			}
			if p.DeploymentID != nil {
				channel := fmt.Sprintf("deployment:%s", p.DeploymentID.String())
				_ = e.wsHub.PublishEvent(channel, &ws.EventMessage{
					Type:     "log",
					Channel:  channel,
					Sequence: persistedLog.ID,
					Data:     data,
				})
			}
		} else if p.DeploymentID != nil {
			channel := fmt.Sprintf("deployment:%s", p.DeploymentID.String())
			_ = e.wsHub.PublishEvent(channel, &ws.EventMessage{
				Type:     "log",
				Channel:  channel,
				Sequence: persistedLog.ID,
				Data:     data,
			})
		}

		// Always publish runtime logs to project channel
		projectChannel := fmt.Sprintf("project:%s", p.ProjectID.String())
		_ = e.wsHub.PublishEvent(projectChannel, &ws.EventMessage{
			Type:     "log",
			Channel:  projectChannel,
			Sequence: persistedLog.ID,
			Data:     data,
		})
	}
}

// StartRuntimeLogCollector starts a persistent background runtime log collector for a container.
// The collector lifecycle is independent of deployment execution (does not exit when deployment completes).
// It stops explicitly when the container is stopped, replaced, or removed, or when ForgeLAB shuts down.
// It includes bounded retries with exponential backoff if the Docker log stream drops while the container is running.
func (e *Engine) StartRuntimeLogCollector(p RuntimeCollectorParams) {
	if e.dockerClient == nil || p.ContainerID == "" {
		return
	}

	var collectorKey string
	if p.ServiceDeploymentID != nil {
		collectorKey = p.ServiceDeploymentID.String()
	} else if p.DeploymentID != nil {
		collectorKey = p.DeploymentID.String()
	} else {
		collectorKey = p.ContainerID
	}

	keys := []string{collectorKey, "container:" + p.ContainerID}
	if len(p.ContainerID) > 12 {
		keys = append(keys, "container:"+p.ContainerID[:12])
	}

	logCtx, logCancel := context.WithCancel(context.Background())
	instanceID := e.getCollectorRegistry().Register(p, logCancel, keys...)

	for _, k := range keys {
		e.activeLogCollectors.Store(k, logCancel)
	}

	go func() {
		defer func() {
			e.getCollectorRegistry().Unregister(instanceID, keys...)
			for _, k := range keys {
				if !e.getCollectorRegistry().IsActive(k) {
					e.activeLogCollectors.Delete(k)
				}
			}
			logCancel()
		}()

		const maxRetries = 5
		retryCount := 0
		lastLogTime := p.Since
		if lastLogTime.IsZero() {
			lastLogTime = time.Now().Add(-2 * time.Second)
		}

		for {
			if logCtx.Err() != nil {
				return
			}

			reader, err := e.dockerClient.ContainerLogs(logCtx, p.ContainerID, container.LogsOptions{
				ShowStdout: true,
				ShowStderr: true,
				Follow:     true,
				Timestamps: true,
				Since:      lastLogTime.Format(time.RFC3339Nano),
			})
			if err != nil {
				if logCtx.Err() != nil {
					return
				}
				e.getCollectorRegistry().RecordCollectionError(instanceID, err)
				insp, inspErr := e.dockerClient.ContainerInspect(logCtx, p.ContainerID)
				if inspErr != nil || !insp.State.Running {
					cid := p.ContainerID
					if len(cid) > 12 {
						cid = cid[:12]
					}
					slog.Info("container no longer running, ending log collector", "container", cid)
					return
				}

				retryCount++
				if retryCount > maxRetries {
					cid := p.ContainerID
					if len(cid) > 12 {
						cid = cid[:12]
					}
					slog.Warn("runtime log collector retries exhausted", "container", cid, "error", err)
					return
				}

				backoff := time.Duration(retryCount*500) * time.Millisecond
				select {
				case <-time.After(backoff):
					continue
				case <-logCtx.Done():
					return
				}
			}

			// Connection established, reset retry counter
			retryCount = 0

			demuxErr := DemuxDockerStream(logCtx, reader, func(stream string, line string) {
				lastLogTime = time.Now()
				e.getCollectorRegistry().RecordActivity(instanceID, 1)
				e.emitRuntimeLog(p, instanceID, stream, line)
			})
			_ = reader.Close()

			if logCtx.Err() != nil {
				return
			}
			if demuxErr != nil && !errors.Is(demuxErr, io.EOF) {
				e.getCollectorRegistry().RecordCollectionError(instanceID, demuxErr)
			}

			// Stream closed; check if container is still running
			insp, inspErr := e.dockerClient.ContainerInspect(logCtx, p.ContainerID)
			if inspErr != nil || !insp.State.Running {
				cid := p.ContainerID
				if len(cid) > 12 {
					cid = cid[:12]
				}
				slog.Info("container stopped or exited, runtime collector ending", "container", cid)
				return
			}

			// Container is still running; retry attaching stream with backoff
			retryCount++
			if retryCount > maxRetries {
				cid := p.ContainerID
				if len(cid) > 12 {
					cid = cid[:12]
				}
				slog.Warn("runtime log collector retries exhausted after disconnect", "container", cid, "last_err", demuxErr)
				return
			}

			backoff := time.Duration(retryCount*500) * time.Millisecond
			select {
			case <-time.After(backoff):
			case <-logCtx.Done():
				return
			}
		}
	}()
}

// startRuntimeLogCollector provides a backward-compatible wrapper for StartRuntimeLogCollector.
func (e *Engine) startRuntimeLogCollector(parentCtx context.Context, containerID string, serviceDeployID, serviceID, projectID uuid.UUID, redactor *logging.LogRedactor, emitLog func(phase, stream, message string)) {
	e.StartRuntimeLogCollector(RuntimeCollectorParams{
		ContainerID:         containerID,
		ProjectID:           projectID,
		ServiceDeploymentID: &serviceDeployID,
		ServiceID:           &serviceID,
		Redactor:            redactor,
	})
}

// StopLogCollector stops the runtime log collector for a specific deployment or service deployment.
func (e *Engine) StopLogCollector(id uuid.UUID) {
	e.getCollectorRegistry().Stop(id.String())
	if cancelFn, loaded := e.activeLogCollectors.LoadAndDelete(id.String()); loaded {
		if fn, ok := cancelFn.(context.CancelFunc); ok {
			fn()
		}
	}
}

// StopContainerLogCollector stops the runtime log collector for a specific container ID.
func (e *Engine) StopContainerLogCollector(containerID string) {
	if containerID == "" {
		return
	}
	reg := e.getCollectorRegistry()
	reg.Stop("container:" + containerID)
	if cancelFn, loaded := e.activeLogCollectors.LoadAndDelete("container:" + containerID); loaded {
		if fn, ok := cancelFn.(context.CancelFunc); ok {
			fn()
		}
	}
	if len(containerID) > 12 {
		reg.Stop("container:" + containerID[:12])
		if cancelFn, loaded := e.activeLogCollectors.LoadAndDelete("container:" + containerID[:12]); loaded {
			if fn, ok := cancelFn.(context.CancelFunc); ok {
				fn()
			}
		}
	}
}

// IsLogCollectorActive checks if a log collector is active for the given ID.
func (e *Engine) IsLogCollectorActive(id uuid.UUID) bool {
	if e.getCollectorRegistry().IsActive(id.String()) {
		return true
	}
	_, ok := e.activeLogCollectors.Load(id.String())
	return ok
}

// IsContainerLogCollectorActive checks if a log collector is active for the given container ID.
func (e *Engine) IsContainerLogCollectorActive(containerID string) bool {
	reg := e.getCollectorRegistry()
	if reg.IsActive("container:" + containerID) {
		return true
	}
	if len(containerID) > 12 && reg.IsActive("container:" + containerID[:12]) {
		return true
	}
	if _, ok := e.activeLogCollectors.Load("container:" + containerID); ok {
		return true
	}
	if len(containerID) > 12 {
		if _, ok := e.activeLogCollectors.Load("container:" + containerID[:12]); ok {
			return true
		}
	}
	return false
}

// StopAllLogCollectors stops all active runtime log collector goroutines (used during shutdown).
func (e *Engine) StopAllLogCollectors() {
	e.getCollectorRegistry().StopAll()
	e.activeLogCollectors.Range(func(key, value interface{}) bool {
		if cancelFn, ok := value.(context.CancelFunc); ok {
			cancelFn()
		}
		e.activeLogCollectors.Delete(key)
		return true
	})
	slog.Info("all runtime log collectors stopped")
}

// Helper methods for Docker Engine
func (e *Engine) getProjectByID(ctx context.Context, projectID uuid.UUID) (*models.Project, error) {
	return e.projectService.GetProjectByIDWithoutOwnership(ctx, projectID)
}

// StopApp stops the currently running containers for a project.
func (e *Engine) StopApp(ctx context.Context, projectID, ownerID uuid.UUID) error {
	project, err := e.projectService.GetProject(ctx, projectID, ownerID)
	if err != nil {
		return err
	}

	if project.CurrentDeploymentID == nil {
		return errors.New("project has no active deployment to stop")
	}

	// 1. Stop legacy deployment container if present
	deployment, err := e.deploymentService.GetDeployment(ctx, *project.CurrentDeploymentID)
	if err == nil && deployment.ContainerID != nil && *deployment.ContainerID != "" {
		e.StopLogCollector(deployment.ID)
		e.StopContainerLogCollector(*deployment.ContainerID)
		if err := e.dockerClient.ContainerStop(ctx, *deployment.ContainerID, container.StopOptions{}); err != nil && !client.IsErrNotFound(err) {
			return fmt.Errorf("failed to stop container: %w", err)
		}
		if err := e.deploymentService.UpdateDeploymentStatus(ctx, deployment.ID, models.DeployStatusStopped, nil); err != nil {
			return fmt.Errorf("failed to update deployment status: %w", err)
		}
	}

	// 2. Stop all service containers for multi-service configurations
	if e.serviceService != nil {
		svcs, err := e.serviceService.ListServices(ctx, projectID)
		if err != nil {
			return fmt.Errorf("failed to list services: %w", err)
		}
		for _, svc := range svcs {
			if svc.CurrentServiceDeploymentID != nil {
				e.StopLogCollector(*svc.CurrentServiceDeploymentID)
			}
			if svc.ContainerID != nil && *svc.ContainerID != "" {
				e.StopContainerLogCollector(*svc.ContainerID)
				if err := e.dockerClient.ContainerStop(ctx, *svc.ContainerID, container.StopOptions{}); err != nil && !client.IsErrNotFound(err) {
					return fmt.Errorf("failed to stop service container: %w", err)
				}
			}
			if err := e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusStopped, nil, nil, nil); err != nil {
				return fmt.Errorf("failed to update service status: %w", err)
			}
		}
	}

	if _, err := e.recalculateAndUpdateProjectStatus(ctx, projectID); err != nil {
		return fmt.Errorf("failed to update project status: %w", err)
	}
	if err := e.deploymentService.SetCurrentDeployment(ctx, projectID, *project.CurrentDeploymentID, models.ProjectStatusStopped); err != nil {
		return fmt.Errorf("failed to set current deployment: %w", err)
	}

	slog.Info("project app stopped", "project_id", projectID)
	return nil
}

// StartApp starts the stopped containers for a project.
func (e *Engine) StartApp(ctx context.Context, projectID, ownerID uuid.UUID) error {
	project, err := e.projectService.GetProject(ctx, projectID, ownerID)
	if err != nil {
		return err
	}

	if project.CurrentDeploymentID == nil {
		return errors.New("project has no active deployment to start")
	}

	// 1. Start legacy deployment container if present
	deployment, err := e.deploymentService.GetDeployment(ctx, *project.CurrentDeploymentID)
	if err == nil && deployment.ContainerID != nil && *deployment.ContainerID != "" {
		if err := e.dockerClient.ContainerStart(ctx, *deployment.ContainerID, container.StartOptions{}); err != nil {
			return fmt.Errorf("failed to start container: %w", err)
		}
		redactor := e.buildLogRedactor(ctx, project.ID, nil, deployment.EnvSnapshot)
		e.StartRuntimeLogCollector(RuntimeCollectorParams{
			ContainerID:  *deployment.ContainerID,
			ProjectID:    project.ID,
			DeploymentID: &deployment.ID,
			Redactor:     redactor,
		})
		if err := e.deploymentService.UpdateDeploymentStatus(ctx, deployment.ID, models.DeployStatusRunning, nil); err != nil {
			return fmt.Errorf("failed to update deployment status: %w", err)
		}
	}

	// 2. Start all service containers for multi-service configurations
	if e.serviceService != nil {
		svcs, err := e.serviceService.ListServices(ctx, projectID)
		if err != nil {
			return fmt.Errorf("failed to list services: %w", err)
		}
		for _, svc := range svcs {
			if svc.ContainerID != nil && *svc.ContainerID != "" {
				if err := e.dockerClient.ContainerStart(ctx, *svc.ContainerID, container.StartOptions{}); err != nil {
					return fmt.Errorf("failed to start service container: %w", err)
				}
				redactor := e.buildLogRedactor(ctx, project.ID, &svc.ID, nil)
				e.StartRuntimeLogCollector(RuntimeCollectorParams{
					ContainerID:         *svc.ContainerID,
					ProjectID:           project.ID,
					DeploymentID:        project.CurrentDeploymentID,
					ServiceID:           &svc.ID,
					ServiceName:         svc.Name,
					ServiceDeploymentID: svc.CurrentServiceDeploymentID,
					Redactor:            redactor,
				})
			}
			if err := e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusRunning, nil, nil, nil); err != nil {
				return fmt.Errorf("failed to update service status: %w", err)
			}
		}
	}

	if _, err := e.recalculateAndUpdateProjectStatus(ctx, projectID); err != nil {
		return fmt.Errorf("failed to update project status: %w", err)
	}
	if err := e.deploymentService.SetCurrentDeployment(ctx, projectID, *project.CurrentDeploymentID, models.ProjectStatusRunning); err != nil {
		return fmt.Errorf("failed to set current deployment: %w", err)
	}

	slog.Info("project app started", "project_id", projectID)
	return nil
}

// RestartApp restarts the container for a project.
func (e *Engine) RestartApp(ctx context.Context, projectID, ownerID uuid.UUID) error {
	project, err := e.projectService.GetProject(ctx, projectID, ownerID)
	if err != nil {
		return err
	}

	if project.CurrentDeploymentID == nil {
		return errors.New("project has no active deployment to restart")
	}

	deployment, err := e.deploymentService.GetDeployment(ctx, *project.CurrentDeploymentID)
	if err != nil {
		return err
	}

	if deployment.ContainerID != nil && *deployment.ContainerID != "" {
		e.StopLogCollector(deployment.ID)
		e.StopContainerLogCollector(*deployment.ContainerID)
		timeout := 10
		if err := e.dockerClient.ContainerRestart(ctx, *deployment.ContainerID, container.StopOptions{Timeout: &timeout}); err != nil {
			return fmt.Errorf("failed to restart container: %w", err)
		}
		redactor := e.buildLogRedactor(ctx, project.ID, nil, deployment.EnvSnapshot)
		e.StartRuntimeLogCollector(RuntimeCollectorParams{
			ContainerID:  *deployment.ContainerID,
			ProjectID:    project.ID,
			DeploymentID: &deployment.ID,
			Redactor:     redactor,
		})
	}

	if err := e.deploymentService.UpdateDeploymentStatus(ctx, deployment.ID, models.DeployStatusRunning, nil); err != nil {
		return fmt.Errorf("failed to update deployment status: %w", err)
	}
	if err := e.deploymentService.SetCurrentDeployment(ctx, projectID, deployment.ID, models.ProjectStatusRunning); err != nil {
		return fmt.Errorf("failed to set current deployment: %w", err)
	}

	slog.Info("project app restarted", "project_id", projectID)
	return nil
}

// StopService stops the container of an individual service and recalculates project status.
func (e *Engine) StopService(ctx context.Context, projectID, serviceID, ownerID uuid.UUID) error {
	project, err := e.projectService.GetProject(ctx, projectID, ownerID)
	if err != nil {
		return err
	}
	if e.serviceService == nil {
		return errors.New("service management unavailable")
	}
	svc, err := e.serviceService.GetService(ctx, serviceID)
	if err != nil {
		return err
	}
	if svc.ProjectID != project.ID {
		return fmt.Errorf("service %s does not belong to project %s", serviceID, project.ID)
	}
	if svc.CurrentServiceDeploymentID != nil {
		e.StopLogCollector(*svc.CurrentServiceDeploymentID)
	}
	if svc.ContainerID != nil && *svc.ContainerID != "" {
		e.StopContainerLogCollector(*svc.ContainerID)
		if err := e.dockerClient.ContainerStop(ctx, *svc.ContainerID, container.StopOptions{}); err != nil && !client.IsErrNotFound(err) {
			return fmt.Errorf("failed to stop service container: %w", err)
		}
	}
	if err := e.serviceService.UpdateServiceStatus(ctx, serviceID, models.ServiceStatusStopped, nil, nil, nil); err != nil {
		return fmt.Errorf("failed to update service status: %w", err)
	}
	svcs, err := e.serviceService.ListServices(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("failed to list services: %w", err)
	}
	newStatus := services.CalculateProjectStatus(svcs)
	if err := e.projectService.UpdateProjectStatus(ctx, project.ID, newStatus); err != nil {
		return fmt.Errorf("failed to update project status: %w", err)
	}
	return nil
}

// StartService starts the container of an individual service and recalculates project status.
func (e *Engine) StartService(ctx context.Context, projectID, serviceID, ownerID uuid.UUID) error {
	project, err := e.projectService.GetProject(ctx, projectID, ownerID)
	if err != nil {
		return err
	}
	if e.serviceService == nil {
		return errors.New("service management unavailable")
	}
	svc, err := e.serviceService.GetService(ctx, serviceID)
	if err != nil {
		return err
	}
	if svc.ProjectID != project.ID {
		return fmt.Errorf("service %s does not belong to project %s", serviceID, project.ID)
	}
	if svc.ContainerID != nil && *svc.ContainerID != "" {
		if err := e.dockerClient.ContainerStart(ctx, *svc.ContainerID, container.StartOptions{}); err != nil {
			return fmt.Errorf("failed to start service container: %w", err)
		}
		redactor := e.buildLogRedactor(ctx, project.ID, &svc.ID, nil)
		e.StartRuntimeLogCollector(RuntimeCollectorParams{
			ContainerID:         *svc.ContainerID,
			ProjectID:           project.ID,
			DeploymentID:        project.CurrentDeploymentID,
			ServiceID:           &svc.ID,
			ServiceName:         svc.Name,
			ServiceDeploymentID: svc.CurrentServiceDeploymentID,
			Redactor:            redactor,
		})
	}
	if err := e.serviceService.UpdateServiceStatus(ctx, serviceID, models.ServiceStatusRunning, nil, nil, nil); err != nil {
		return fmt.Errorf("failed to update service status: %w", err)
	}
	svcs, err := e.serviceService.ListServices(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("failed to list services: %w", err)
	}
	newStatus := services.CalculateProjectStatus(svcs)
	if err := e.projectService.UpdateProjectStatus(ctx, project.ID, newStatus); err != nil {
		return fmt.Errorf("failed to update project status: %w", err)
	}
	return nil
}

// RestartService restarts an individual service.
func (e *Engine) RestartService(ctx context.Context, projectID, serviceID, ownerID uuid.UUID) error {
	if err := e.StopService(ctx, projectID, serviceID, ownerID); err != nil {
		return err
	}
	return e.StartService(ctx, projectID, serviceID, ownerID)
}

// CleanUpProjectContainers is a backward-compatible wrapper calling CleanUpProjectResources.
func (e *Engine) CleanUpProjectContainers(ctx context.Context, projectID uuid.UUID) {
	_ = e.CleanUpProjectResources(ctx, projectID)
}

// CleanUpProjectResources stops and removes all containers, images, and networks owned by a project.
// It is idempotent, error-aware, and safely ignores already-removed resources.
// It never touches Compose infrastructure containers, persistent volumes, or unrelated images.
func (e *Engine) CleanUpProjectResources(ctx context.Context, projectID uuid.UUID) error {
	if e.dockerClient == nil {
		return nil
	}

	// 0. Cancel and drain all active in-flight deployment workers for this project
	if err := e.CancelAndDrainProjectDeployments(ctx, projectID); err != nil {
		slog.Warn("timeout or error waiting to drain project deployments during cleanup", "project_id", projectID, "error", err)
	}

	var cleanupErrs []error
	projIDStr := projectID.String()

	// 1. Gather all containers owned by this project (including orphaned containers)
	knownContainerIDs := make(map[string]bool)
	if e.deploymentService != nil {
		if deployments, err := e.deploymentService.ListDeployments(ctx, projectID); err == nil {
			for _, d := range deployments {
				if d.ContainerID != nil && *d.ContainerID != "" {
					knownContainerIDs[*d.ContainerID] = true
				}
			}
		}
	}
	if e.serviceService != nil {
		if svcs, err := e.serviceService.ListServices(ctx, projectID); err == nil {
			for _, svc := range svcs {
				if svc.CurrentServiceDeploymentID != nil {
					e.StopLogCollector(*svc.CurrentServiceDeploymentID)
				}
				if svc.ContainerID != nil && *svc.ContainerID != "" {
					knownContainerIDs[*svc.ContainerID] = true
				}
			}
		}
	}

	containers, err := e.dockerClient.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("failed to list containers for project cleanup: %w", err)
	}

	stopTimeout := 5
	for _, c := range containers {
		// NEVER touch Docker Compose infrastructure containers
		if _, isCompose := c.Labels["com.docker.compose.project"]; isCompose {
			continue
		}
		if _, isComposeService := c.Labels["com.docker.compose.service"]; isComposeService {
			continue
		}

		isOwned := false
		if c.Labels != nil && c.Labels["forgelab.project_id"] == projIDStr {
			isOwned = true
		} else if knownContainerIDs[c.ID] || (len(c.ID) > 12 && knownContainerIDs[c.ID[:12]]) {
			isOwned = true
		}

		if isOwned {
			e.StopContainerLogCollector(c.ID)
			// Stop container (ignore not found or already stopped)
			_ = e.dockerClient.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &stopTimeout})

			// Remove container with RemoveVolumes: false (Never remove persistent volumes!)
			remErr := e.dockerClient.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true, RemoveVolumes: false})
			if remErr != nil && !client.IsErrNotFound(remErr) {
				slog.Error("failed to remove project container", "container_id", c.ID, "project_id", projIDStr, "error", remErr)
				cleanupErrs = append(cleanupErrs, fmt.Errorf("failed to remove container %s: %w", c.ID, remErr))
			} else {
				slog.Info("removed project container", "container_id", c.ID, "project_id", projIDStr)
			}
		}
	}

	// 2. Remove project networks
	nets, netListErr := e.dockerClient.NetworkList(ctx, dockernetwork.ListOptions{})
	if netListErr != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("failed to list networks: %w", netListErr))
	} else {
		projPrefix := fmt.Sprintf("forgelab-net-%s", projIDStr)
		projShortPrefix := fmt.Sprintf("forgelab-net-%s", projIDStr[:8])
		for _, n := range nets {
			isProjectNet := (n.Labels != nil && n.Labels["forgelab.project_id"] == projIDStr) ||
				strings.HasPrefix(n.Name, projPrefix) ||
				strings.HasPrefix(n.Name, projShortPrefix)

			if isProjectNet {
				remErr := e.dockerClient.NetworkRemove(ctx, n.ID)
				if remErr != nil && !client.IsErrNotFound(remErr) {
					slog.Error("failed to remove project network", "network_id", n.ID, "name", n.Name, "error", remErr)
					cleanupErrs = append(cleanupErrs, fmt.Errorf("failed to remove network %s: %w", n.Name, remErr))
				} else {
					slog.Info("removed project network", "network_id", n.ID, "name", n.Name)
				}
			}
		}
	}

	// 3. Remove project-owned images (including retained rollback images)
	// Never use global image prune! Remove only images owned by this project.
	images, imgListErr := e.dockerClient.ImageList(ctx, dockerimage.ListOptions{All: true})
	if imgListErr != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("failed to list images: %w", imgListErr))
	} else {
		projTagPrefix := fmt.Sprintf("forgelab/%s", projIDStr)
		for _, img := range images {
			isProjectImage := false
			if img.Labels != nil && img.Labels["forgelab.project_id"] == projIDStr {
				isProjectImage = true
			} else {
				for _, tag := range img.RepoTags {
					if strings.HasPrefix(tag, projTagPrefix) {
						isProjectImage = true
						break
					}
				}
			}

			if isProjectImage {
				_, remErr := e.dockerClient.ImageRemove(ctx, img.ID, dockerimage.RemoveOptions{Force: true, PruneChildren: true})
				if remErr != nil && !client.IsErrNotFound(remErr) {
					slog.Warn("failed to remove project image", "image_id", img.ID, "error", remErr)
					cleanupErrs = append(cleanupErrs, fmt.Errorf("failed to remove image %s: %w", img.ID, remErr))
				} else {
					slog.Info("removed project image", "image_id", img.ID, "project_id", projIDStr)
				}
			}
		}
	}

	// 4. Release in-memory port allocations
	if e.portManager != nil {
		if activeContainers, err := e.dockerClient.ContainerList(ctx, container.ListOptions{}); err == nil {
			var runningPorts []int
			for _, ac := range activeContainers {
				if ac.State == "running" {
					for _, p := range ac.Ports {
						if p.PublicPort > 0 {
							runningPorts = append(runningPorts, int(p.PublicPort))
						}
					}
				}
			}
			e.portManager.ReconcileUsedPorts(runningPorts)
		}
	}

	if len(cleanupErrs) > 0 {
		return errors.Join(cleanupErrs...)
	}
	return nil
}

// ReconcileDaemonContainers reconciles DB container state against Docker daemon state on startup:
// 1. Detects containers stopped/crashed while backend was offline and marks services/projects as crashed/stopped.
// 2. Cleans up orphaned ForgeLAB containers whose project or deployment is terminated.
// 3. Syncs active host port allocations with the in-memory port manager.
func (e *Engine) ReconcileDaemonContainers(ctx context.Context) error {
	if e.dockerClient == nil {
		return nil
	}

	containers, err := e.dockerClient.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("failed to list docker containers for reconciliation: %w", err)
	}

	containerMap := make(map[string]types.Container, len(containers))
	var runningPorts []int

	for _, c := range containers {
		containerMap[c.ID] = c
		// Match short ID prefix as well
		if len(c.ID) > 12 {
			containerMap[c.ID[:12]] = c
		}

		if c.State == "running" {
			for _, p := range c.Ports {
				if p.PublicPort > 0 {
					runningPorts = append(runningPorts, int(p.PublicPort))
				}
			}
		}
	}

	// 1. Sync port allocations with active running containers
	if e.portManager != nil {
		e.portManager.ReconcileUsedPorts(runningPorts)
		slog.Info("reconciled host port manager with running containers", "active_ports_count", len(runningPorts))
	}

	// Track IDs of containers that are legitimately active in the database
	legitimateContainers := make(map[string]bool)

	// 2. Reconcile active multi-service configurations
	if e.serviceService != nil {
		services, err := e.serviceService.ListActiveServices(ctx)
		if err != nil {
			slog.Warn("reconciliation: failed to list active services", "error", err)
		} else {
			touchedProjects := make(map[uuid.UUID]bool)
			for _, svc := range services {
				touchedProjects[svc.ProjectID] = true
				if svc.ContainerID == nil || *svc.ContainerID == "" {
					if svc.Status == models.ServiceStatusRunning {
						_ = e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusStopped, nil, nil, nil)
						slog.Info("reconciliation: service has no container, marked stopped", "service_id", svc.ID)
					}
					continue
				}

				cID := *svc.ContainerID
				dockCont, exists := containerMap[cID]
				if !exists {
					if svc.Classification == models.ClassificationJob || svc.Status == models.ServiceStatusStopped {
						slog.Info("reconciliation: stopped/job service container missing (cleaned up), keeping status", "service_id", svc.ID)
						continue
					}
					// Container disappeared while offline
					e.StopContainerLogCollector(cID)
					if svc.CurrentServiceDeploymentID != nil {
						e.StopLogCollector(*svc.CurrentServiceDeploymentID)
					}
					_ = e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusFailed, nil, nil, nil)
					if svc.CurrentServiceDeploymentID != nil {
						_ = e.deploymentService.FailServiceDeployment(ctx, *svc.CurrentServiceDeploymentID, "Container disappeared from Docker daemon while backend was offline")
					}
					slog.Warn("reconciliation: service container missing, marked failed", "service_id", svc.ID, "container_id", cID)
				} else if dockCont.State != "running" {
					if svc.Status == models.ServiceStatusStopped || svc.Classification == models.ClassificationJob {
						// Service was intentionally stopped or is a completed job; preserve its stopped container
						legitimateContainers[dockCont.ID] = true
						if len(dockCont.ID) > 12 {
							legitimateContainers[dockCont.ID[:12]] = true
						}
						slog.Info("reconciliation: service container stopped as expected", "service_id", svc.ID, "container_id", cID)
					} else {
						// Container is dead or exited while service was expected to be running
						e.StopContainerLogCollector(cID)
						if svc.CurrentServiceDeploymentID != nil {
							e.StopLogCollector(*svc.CurrentServiceDeploymentID)
						}
						_ = e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusFailed, nil, nil, nil)
						if svc.CurrentServiceDeploymentID != nil {
							_ = e.deploymentService.FailServiceDeployment(ctx, *svc.CurrentServiceDeploymentID, fmt.Sprintf("Container stopped with state '%s' while backend was offline", dockCont.State))
						}
						slog.Warn("reconciliation: service container exited, marked failed", "service_id", svc.ID, "state", dockCont.State)
					}
				} else {
					// Legitimately running
					legitimateContainers[dockCont.ID] = true
					if len(dockCont.ID) > 12 {
						legitimateContainers[dockCont.ID[:12]] = true
					}
					// Restore runtime log collector if not active (e.g. after backend restart)
					if !e.IsContainerLogCollectorActive(dockCont.ID) {
						redactor := e.buildLogRedactor(ctx, svc.ProjectID, &svc.ID, nil)
						e.StartRuntimeLogCollector(RuntimeCollectorParams{
							ContainerID:         dockCont.ID,
							ProjectID:           svc.ProjectID,
							ServiceID:           &svc.ID,
							ServiceName:         svc.Name,
							ServiceDeploymentID: svc.CurrentServiceDeploymentID,
							Redactor:            redactor,
						})
						slog.Info("reconciliation: restored runtime log collector for running service", "service_id", svc.ID, "container_id", dockCont.ID[:12])
					}
				}
			}

			// Recalculate parent project status for all projects with inspected services
			for projID := range touchedProjects {
				_, _ = e.recalculateAndUpdateProjectStatus(ctx, projID)
			}

			// Also reconcile any project currently marked active in DB whose services may have ceased
			if e.projectService != nil {
				if activeProjects, err := e.projectService.ListActiveProjects(ctx); err == nil {
					for _, p := range activeProjects {
						if !touchedProjects[p.ID] {
							_, _ = e.recalculateAndUpdateProjectStatus(ctx, p.ID)
						}
					}
				}
			}
		}
	}

	// 2b. Reconcile active service deployments (in-flight or running)
	if e.deploymentService != nil {
		activeSDs, err := e.deploymentService.ListActiveServiceDeployments(ctx)
		if err != nil {
			slog.Warn("reconciliation: failed to list active service deployments", "error", err)
		} else {
			for _, sd := range activeSDs {
				// Reconcile interrupted in-flight deployments whose workers terminated when backend restarted.
				// A deployment can only be interrupted by a backend restart if:
				// 1. It was created BEFORE this backend instance started up, AND
				// 2. Neither it NOR its parent release deployment is currently actively executing in memory.
				if sd.Status == models.DeployStatusQueued ||
					sd.Status == models.DeployStatusCloning ||
					sd.Status == models.DeployStatusBuilding ||
					sd.Status == models.DeployStatusStarting ||
					sd.Status == models.DeployStatusHealthChecking {
					if e.IsActiveExecution(sd.ID) ||
						(sd.DeploymentID != nil && e.IsActiveExecution(*sd.DeploymentID)) ||
						(!sd.CreatedAt.IsZero() && !sd.CreatedAt.Before(e.startTime)) {
						continue
					}
					reason := "Deployment interrupted: backend restarted while service deployment was in progress"
					_ = e.deploymentService.FailServiceDeployment(ctx, sd.ID, reason)
					slog.Info("reconciliation: interrupted in-flight service deployment marked failed", "service_deployment_id", sd.ID, "status", sd.Status)
					continue
				}

				if sd.ContainerID == nil || *sd.ContainerID == "" {
					continue
				}
				cID := *sd.ContainerID
				legitimateContainers[cID] = true
				if len(cID) > 12 {
					legitimateContainers[cID[:12]] = true
				}
				if dockCont, exists := containerMap[cID]; exists {
					legitimateContainers[dockCont.ID] = true
					if len(dockCont.ID) > 12 {
						legitimateContainers[dockCont.ID[:12]] = true
					}
				}
			}
		}
	}

	// 2c. Protect in-memory active containers currently deploying
	e.activeContainers.Range(func(key, value any) bool {
		if cID, ok := key.(string); ok && cID != "" {
			legitimateContainers[cID] = true
			if len(cID) > 12 {
				legitimateContainers[cID[:12]] = true
			}
		}
		return true
	})

	// 3. Reconcile active legacy single-container deployments
	if e.deploymentService != nil {
		deployments, err := e.deploymentService.ListActiveDeployments(ctx)
		if err != nil {
			slog.Warn("reconciliation: failed to list active deployments", "error", err)
		} else {
			for _, d := range deployments {
				// Reconcile interrupted in-flight deployments whose workers terminated when backend restarted.
				// A deployment can only be interrupted by a backend restart if:
				// 1. It was created BEFORE this backend instance started up, AND
				// 2. It is not currently actively executing in memory.
				if d.Status == models.DeployStatusQueued ||
					d.Status == models.DeployStatusCloning ||
					d.Status == models.DeployStatusBuilding ||
					d.Status == models.DeployStatusStarting ||
					d.Status == models.DeployStatusHealthChecking {
					if e.IsActiveExecution(d.ID) || (!d.CreatedAt.IsZero() && !d.CreatedAt.Before(e.startTime)) {
						continue
					}
					reason := "Deployment interrupted: backend restarted while deployment was in progress"
					_ = e.deploymentService.UpdateDeploymentStatus(ctx, d.ID, models.DeployStatusFailed, &reason)
					if e.projectService != nil {
						_ = e.projectService.UpdateProjectStatus(ctx, d.ProjectID, models.ProjectStatusStopped)
					}
					slog.Info("reconciliation: interrupted in-flight deployment marked failed", "deployment_id", d.ID, "status", d.Status)
					continue
				}

				if d.ContainerID == nil || *d.ContainerID == "" {
					continue
				}
				cID := *d.ContainerID
				dockCont, exists := containerMap[cID]
				if !exists || dockCont.State != "running" {
					e.StopLogCollector(d.ID)
					e.StopContainerLogCollector(cID)
					stateMsg := "missing"
					if exists {
						stateMsg = dockCont.State
					}
					reason := fmt.Sprintf("Container %s while backend was offline", stateMsg)
					_ = e.deploymentService.UpdateDeploymentStatus(ctx, d.ID, models.DeployStatusFailed, &reason)
					if e.projectService != nil {
						_ = e.projectService.UpdateProjectStatus(ctx, d.ProjectID, models.ProjectStatusStopped)
					}
					slog.Warn("reconciliation: legacy deployment container dead/missing", "deployment_id", d.ID, "container_id", cID)
				} else {
					legitimateContainers[dockCont.ID] = true
					if len(dockCont.ID) > 12 {
						legitimateContainers[dockCont.ID[:12]] = true
					}
					// Restore runtime log collector if not active (e.g. after backend restart)
					if !e.IsContainerLogCollectorActive(dockCont.ID) {
						redactor := e.buildLogRedactor(ctx, d.ProjectID, nil, d.EnvSnapshot)
						e.StartRuntimeLogCollector(RuntimeCollectorParams{
							ContainerID:  dockCont.ID,
							ProjectID:    d.ProjectID,
							DeploymentID: &d.ID,
							Redactor:     redactor,
						})
						slog.Info("reconciliation: restored runtime log collector for running legacy deployment", "deployment_id", d.ID, "container_id", dockCont.ID[:12])
					}
				}
			}
		}
	}

	// 4. Clean up orphaned ForgeLAB application containers
	orphansCleaned := 0
	stopTimeout := 5
	for _, c := range containers {
		// NEVER touch Docker Compose infrastructure containers (backend, frontend, postgres, redis, migrate)!
		if _, isCompose := c.Labels["com.docker.compose.project"]; isCompose {
			continue
		}
		if _, isComposeService := c.Labels["com.docker.compose.service"]; isComposeService {
			continue
		}

		// Only consider application containers provisioned by ForgeLAB engine
		_, hasProjectLabel := c.Labels["forgelab.project_id"]
		_, hasDeploymentLabel := c.Labels["forgelab.deployment_id"]
		_, hasServiceDeploymentLabel := c.Labels["forgelab.service_deployment_id"]

		if !hasProjectLabel && !hasDeploymentLabel && !hasServiceDeploymentLabel {
			continue
		}

		// Check if actively tracked in memory
		if e.isContainerTracked(c.ID) || (len(c.ID) > 12 && e.isContainerTracked(c.ID[:12])) {
			continue
		}

		// Grace period: NEVER remove containers created within the last 15 minutes!
		// In-flight builds, database initializations (e.g. postgres initdb), and multi-tier DAGs
		// must never be killed by the periodic background reconciler.
		if c.Created > 0 {
			createdTime := time.Unix(c.Created, 0)
			if time.Since(createdTime) < 15*time.Minute {
				continue
			}
		}

		if !legitimateContainers[c.ID] {
			cShort := c.ID
			if len(cShort) > 12 {
				cShort = cShort[:12]
			}
			slog.Info("reconciliation: removing orphaned container", "container_id", cShort, "labels", c.Labels)
			e.StopContainerLogCollector(c.ID)
			_ = e.dockerClient.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &stopTimeout})
			_ = e.dockerClient.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true, RemoveVolumes: false})
			orphansCleaned++
		}
	}

	if orphansCleaned > 0 {
		slog.Info("reconciliation completed: cleaned up orphaned containers", "count", orphansCleaned)
	}

	// 5. Prune orphaned ForgeLAB networks with zero connected containers
	_ = e.PruneDanglingNetworks(ctx)

	return nil
}

// PruneDanglingResources removes expired build workspaces and dangling Docker images to bound storage growth.
func (e *Engine) PruneDanglingResources(ctx context.Context, maxBuildAge time.Duration) error {
	if maxBuildAge <= 0 {
		maxBuildAge = 2 * time.Hour
	}

	var pruneErrs []error

	// 1. Prune old build directories in workDir
	if e.workDir != "" {
		entries, err := os.ReadDir(e.workDir)
		if err == nil {
			now := time.Now()
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				info, err := entry.Info()
				if err != nil {
					continue
				}
				if now.Sub(info.ModTime()) > maxBuildAge {
					dirPath := filepath.Join(e.workDir, entry.Name())
					if err := os.RemoveAll(dirPath); err != nil {
						slog.Warn("failed to remove expired build directory", "path", dirPath, "error", err)
						pruneErrs = append(pruneErrs, fmt.Errorf("failed to remove build dir %s: %w", dirPath, err))
					} else {
						slog.Info("pruned expired build directory", "path", dirPath, "age", now.Sub(info.ModTime()).String())
					}
				}
			}
		} else if !os.IsNotExist(err) {
			slog.Warn("failed to read build workDir for pruning", "path", e.workDir, "error", err)
			pruneErrs = append(pruneErrs, fmt.Errorf("failed to read workDir %s: %w", e.workDir, err))
		}
	}

	// 2. Prune dangling Docker images with exclusive maintenance synchronization
	if e.dockerClient != nil {
		if err := e.pruneDanglingImages(ctx); err != nil {
			pruneErrs = append(pruneErrs, err)
		}
	}

	// 3. Prune dangling/orphaned ForgeLAB networks with zero active containers
	if e.dockerClient != nil {
		if err := e.PruneDanglingNetworks(ctx); err != nil {
			pruneErrs = append(pruneErrs, err)
		}
	}

	if len(pruneErrs) > 0 {
		return errors.Join(pruneErrs...)
	}
	return nil
}

// pruneDanglingImages acquires the exclusive buildMaintenanceMu lock to ensure no Docker image builds
// are active or can start while dangling Docker images are being pruned.
func (e *Engine) pruneDanglingImages(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if !e.buildMaintenanceMu.TryLock() {
		slog.Info("docker image prune waiting for active builds")
		e.buildMaintenanceMu.Lock()
	}
	defer e.buildMaintenanceMu.Unlock()

	slog.Info("docker image prune started")
	pruneFilters := filters.NewArgs()
	pruneFilters.Add("dangling", "true")
	if e.pruneFilterLabel != "" {
		pruneFilters.Add("label", e.pruneFilterLabel)
	}

	report, err := e.dockerClient.ImagesPrune(ctx, pruneFilters)
	if err != nil {
		slog.Warn("failed to prune dangling docker images", "error", err)
		return fmt.Errorf("docker image prune failed: %w", err)
	}

	slog.Info("docker image prune completed", "deleted_count", len(report.ImagesDeleted), "space_reclaimed_bytes", report.SpaceReclaimed)
	return nil
}

// EnsureNetwork guarantees a Docker network exists with the given name and labels.
// It handles race conditions (already exists / conflict) and performs automatic self-healing
// pruning if the Docker daemon's IPAM default address pool is exhausted.
func (e *Engine) EnsureNetwork(ctx context.Context, networkName string, labels map[string]string) error {
	if e.dockerClient == nil {
		return nil
	}

	// 1. Fast check: already exists?
	_, inspectErr := e.dockerClient.NetworkInspect(ctx, networkName, dockernetwork.InspectOptions{})
	if inspectErr == nil {
		return nil
	}

	// 2. Attempt creation
	createOpts := dockernetwork.CreateOptions{
		Driver: "bridge",
		Labels: labels,
	}

	_, err := e.dockerClient.NetworkCreate(ctx, networkName, createOpts)
	if err == nil {
		return nil
	}

	// 3. Handle conflict / already exists (concurrent worker created it)
	errStr := strings.ToLower(err.Error())
	if strings.Contains(errStr, "already exists") || strings.Contains(errStr, "conflict") {
		if _, inspectErr := e.dockerClient.NetworkInspect(ctx, networkName, dockernetwork.InspectOptions{}); inspectErr == nil {
			return nil
		}
	}

	// 4. Handle pool exhaustion / overlap: self-heal by pruning orphaned unattached forgelab networks
	if strings.Contains(errStr, "pool") || strings.Contains(errStr, "address") || strings.Contains(errStr, "overlap") {
		slog.Warn("docker network pool exhaustion detected; pruning orphaned networks", "network", networkName, "error", err)
		_ = e.PruneDanglingNetworks(ctx)
		// Retry creation once after pruning
		_, retryErr := e.dockerClient.NetworkCreate(ctx, networkName, createOpts)
		if retryErr == nil {
			slog.Info("successfully created network after pruning orphaned networks", "network", networkName)
			return nil
		}
		if strings.Contains(strings.ToLower(retryErr.Error()), "already exists") || strings.Contains(strings.ToLower(retryErr.Error()), "conflict") {
			if _, inspectErr := e.dockerClient.NetworkInspect(ctx, networkName, dockernetwork.InspectOptions{}); inspectErr == nil {
				return nil
			}
		}
		err = retryErr
	}

	return fmt.Errorf("failed to create network '%s': %w", networkName, err)
}

// PruneDanglingNetworks cleans up orphaned ForgeLAB bridge networks that have zero connected containers.
func (e *Engine) PruneDanglingNetworks(ctx context.Context) error {
	if e.dockerClient == nil {
		return nil
	}

	nets, err := e.dockerClient.NetworkList(ctx, dockernetwork.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list networks for pruning: %w", err)
	}

	for _, n := range nets {
		// Only touch networks created by ForgeLAB
		isForgeLabNet := strings.HasPrefix(n.Name, "forgelab-net-") || (n.Labels != nil && n.Labels["forgelab.project_id"] != "")
		if !isForgeLabNet {
			continue
		}

		// Inspect network to verify no active containers are attached
		insp, inspectErr := e.dockerClient.NetworkInspect(ctx, n.ID, dockernetwork.InspectOptions{})
		if inspectErr != nil {
			continue
		}
		if len(insp.Containers) == 0 {
			if removeErr := e.dockerClient.NetworkRemove(ctx, n.ID); removeErr == nil {
				shortID := n.ID
				if len(shortID) > 12 {
					shortID = shortID[:12]
				}
				slog.Info("pruned orphaned forgelab network", "network_id", shortID, "network_name", n.Name)
			}
		}
	}
	return nil
}

func (e *Engine) verifyDockerDaemon(ctx context.Context) error {
	if e.dockerClient == nil {
		return nil
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ping, err := e.dockerClient.Ping(pingCtx)
	if err != nil {
		return err
	}
	e.setCachedBuilderVersion(ping.BuilderVersion)
	return nil
}

// PruneOldProjectImages prunes older Docker images created for a specific project,
// preserving the latest keepRecent images to maintain fast rollbacks while freeing disk space.
func (e *Engine) PruneOldProjectImages(ctx context.Context, projectID string, keepRecent int) (int, error) {
	if e.dockerClient == nil || projectID == "" {
		return 0, nil
	}
	if keepRecent <= 0 {
		keepRecent = 3
	}

	opts := dockerimage.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("label", fmt.Sprintf("forgelab.project_id=%s", projectID)),
		),
	}

	images, err := e.dockerClient.ImageList(ctx, opts)
	if err != nil {
		return 0, fmt.Errorf("failed to list project images for pruning: %w", err)
	}

	if len(images) <= keepRecent {
		return 0, nil
	}

	// Sort images by Created timestamp descending (newest first)
	sort.Slice(images, func(i, j int) bool {
		return images[i].Created > images[j].Created
	})

	prunedCount := 0
	for _, img := range images[keepRecent:] {
		_, pruneErr := e.dockerClient.ImageRemove(ctx, img.ID, dockerimage.RemoveOptions{
			PruneChildren: true,
		})
		if pruneErr == nil {
			prunedCount++
			shortID := img.ID
			if len(shortID) > 12 {
				shortID = shortID[:12]
			}
			slog.Info("pruned old project image", "project_id", projectID, "image_id", shortID)
		}
	}

	return prunedCount, nil
}

// CheckContinuousRuntimeHealth inspects all active service containers to detect unexpected exits,
// kernel OOM kills, and Docker health-check degradation.
func (e *Engine) CheckContinuousRuntimeHealth(ctx context.Context) error {
	if e.dockerClient == nil || e.serviceService == nil {
		return nil
	}

	services, err := e.serviceService.ListActiveServices(ctx)
	if err != nil {
		return err
	}

	touchedProjects := make(map[uuid.UUID]bool)
	for _, svc := range services {
		if svc.ContainerID == nil || *svc.ContainerID == "" {
			continue
		}

		insp, err := e.dockerClient.ContainerInspect(ctx, *svc.ContainerID)
		if err != nil {
			if client.IsErrNotFound(err) {
				if svc.Classification == models.ClassificationJob || svc.Status == models.ServiceStatusStopped {
					continue
				}
				_ = e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusFailed, nil, nil, nil)
				if svc.CurrentServiceDeploymentID != nil && e.deploymentService != nil {
					_ = e.deploymentService.FailServiceDeployment(ctx, *svc.CurrentServiceDeploymentID, "Runtime container terminated or disappeared from Docker daemon")
				}
				slog.Warn("continuous health: container not found, marked service failed", "service_id", svc.ID, "container_id", *svc.ContainerID)
				touchedProjects[svc.ProjectID] = true
			}
			continue
		}

		if insp.State == nil {
			continue
		}

		if insp.State.OOMKilled {
			if svc.Status == models.ServiceStatusStopped {
				continue
			}
			e.StopContainerLogCollector(*svc.ContainerID)
			if svc.CurrentServiceDeploymentID != nil {
				e.StopLogCollector(*svc.CurrentServiceDeploymentID)
			}
			reason := "Container was terminated by kernel OOM (Out Of Memory) killer. Review memory quota or memory limits."
			_ = e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusFailed, nil, nil, nil)
			if svc.CurrentServiceDeploymentID != nil && e.deploymentService != nil {
				_ = e.deploymentService.FailServiceDeployment(ctx, *svc.CurrentServiceDeploymentID, reason)
			}
			slog.Warn("continuous health: container OOM killed", "service_id", svc.ID, "container_id", *svc.ContainerID)
			touchedProjects[svc.ProjectID] = true
		} else if !insp.State.Running {
			if svc.Status == models.ServiceStatusStopped || svc.Classification == models.ClassificationJob {
				// Intentionally stopped service or completed one-shot job: do NOT mark as crashed
				continue
			}
			e.StopContainerLogCollector(*svc.ContainerID)
			if svc.CurrentServiceDeploymentID != nil {
				e.StopLogCollector(*svc.CurrentServiceDeploymentID)
			}
			if insp.State.ExitCode != 0 {
				reason := fmt.Sprintf("Container crashed with non-zero exit code (%d)", insp.State.ExitCode)
				_ = e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusFailed, nil, nil, nil)
				if svc.CurrentServiceDeploymentID != nil && e.deploymentService != nil {
					_ = e.deploymentService.FailServiceDeployment(ctx, *svc.CurrentServiceDeploymentID, reason)
				}
				slog.Warn("continuous health: container exited with failure", "service_id", svc.ID, "exit_code", insp.State.ExitCode)
				touchedProjects[svc.ProjectID] = true
			} else {
				// Clean exit code 0 when service was not marked stopped: record intentional/clean stop
				_ = e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusStopped, nil, nil, nil)
				slog.Info("continuous health: container exited cleanly (code 0), marked stopped", "service_id", svc.ID)
				touchedProjects[svc.ProjectID] = true
			}
		} else if insp.State.Running {
			if !e.IsContainerLogCollectorActive(*svc.ContainerID) {
				redactor := e.buildLogRedactor(ctx, svc.ProjectID, &svc.ID, nil)
				e.StartRuntimeLogCollector(RuntimeCollectorParams{
					ContainerID:         *svc.ContainerID,
					ProjectID:           svc.ProjectID,
					ServiceID:           &svc.ID,
					ServiceName:         svc.Name,
					ServiceDeploymentID: svc.CurrentServiceDeploymentID,
					Redactor:            redactor,
				})
			}
		}
	}

	for projID := range touchedProjects {
		_, _ = e.recalculateAndUpdateProjectStatus(ctx, projID)
	}

	return nil
}
