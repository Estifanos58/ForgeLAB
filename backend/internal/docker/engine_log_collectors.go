package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/logging"
	"github.com/forgelab/backend/internal/models"
	ws "github.com/forgelab/backend/internal/websocket"
)

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
	if len(containerID) > 12 && reg.IsActive("container:"+containerID[:12]) {
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
