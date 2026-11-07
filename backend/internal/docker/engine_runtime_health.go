package docker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/docker/docker/client"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/models"
)

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
