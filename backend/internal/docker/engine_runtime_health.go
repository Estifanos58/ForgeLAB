package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

func (e *Engine) verifyDockerDaemon(ctx context.Context) error {
	if e.dockerClient == nil {
		return nil
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ping, err := e.dockerClient.Ping(pingCtx, client.PingOptions{})
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

		inspRes, err := e.dockerClient.ContainerInspect(ctx, *svc.ContainerID, client.ContainerInspectOptions{})
		if err != nil {
			if cerrdefs.IsNotFound(err) {
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
		insp := inspRes.Container

		if insp.State == nil {
			continue
		}

		if insp.State.OOMKilled {
			e.resetHealthStrike(svc.ID)
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
			e.resetHealthStrike(svc.ID)
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

			if svc.Classification == models.ClassificationJob || svc.Status == models.ServiceStatusStopped || svc.Status == models.ServiceStatusDeploying {
				continue
			}

			if insp.State.Health != nil {
				switch insp.State.Health.Status {
				case container.Unhealthy:
					lastOutput := extractLastHealthOutput(insp.State.Health)
					if svc.Status != models.ServiceStatusDegraded {
						if updErr := e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusDegraded, nil, nil, nil); updErr != nil {
							slog.Warn("continuous health: failed to mark unhealthy service degraded", "service_id", svc.ID, "error", updErr)
						} else {
							touchedProjects[svc.ProjectID] = true
							slog.Warn("continuous health: container unhealthy, marked service degraded",
								"service_id", svc.ID,
								"service_name", svc.Name,
								"container_id", *svc.ContainerID,
								"health_output", lastOutput,
							)
						}
					}
					e.handleUnhealthyStrike(ctx, svc, lastOutput, touchedProjects)
				case container.Healthy:
					e.resetHealthStrike(svc.ID)
					if svc.Status == models.ServiceStatusDegraded {
						if updErr := e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusRunning, nil, nil, nil); updErr != nil {
							slog.Warn("continuous health: failed to restore healthy service status", "service_id", svc.ID, "error", updErr)
						} else {
							touchedProjects[svc.ProjectID] = true
							slog.Info("continuous health: container recovered to healthy, restored running status",
								"service_id", svc.ID,
								"service_name", svc.Name,
								"container_id", *svc.ContainerID,
							)
						}
					}
				}
			}
		}
	}

	for projID := range touchedProjects {
		_, _ = e.recalculateAndUpdateProjectStatus(ctx, projID)
	}

	return nil
}

func extractLastHealthOutput(h *container.Health) string {
	if h == nil || len(h.Log) == 0 {
		return ""
	}
	for i := len(h.Log) - 1; i >= 0; i-- {
		if out := strings.TrimSpace(h.Log[i].Output); out != "" {
			return out
		}
	}
	return ""
}

func (e *Engine) handleUnhealthyStrike(ctx context.Context, svc *models.Service, healthOutput string, touchedProjects map[uuid.UUID]bool) {
	e.healthStrikeMu.Lock()
	if e.healthStrikes == nil {
		e.healthStrikes = make(map[uuid.UUID]int)
	}
	if e.lastAutoRollbackAt == nil {
		e.lastAutoRollbackAt = make(map[uuid.UUID]time.Time)
	}
	e.healthStrikes[svc.ID]++
	strikes := e.healthStrikes[svc.ID]
	threshold := e.autoRollbackThreshold
	if threshold <= 0 {
		threshold = 3
	}
	cooldown := e.autoRollbackCooldown
	if cooldown < 0 {
		cooldown = 5 * time.Minute
	}
	enabled := e.autoRollbackEnabled
	lastAttempt := e.lastAutoRollbackAt[svc.ID]
	canAttempt := enabled && strikes >= threshold && (lastAttempt.IsZero() || time.Since(lastAttempt) >= cooldown)
	e.healthStrikeMu.Unlock()

	if !canAttempt || e.queueEnqueuer == nil || e.deploymentService == nil || e.projectService == nil {
		return
	}

	project, err := e.getProjectByID(ctx, svc.ProjectID)
	if err != nil {
		slog.Warn("auto-rollback: failed to load project for unhealthy service", "service_id", svc.ID, "project_id", svc.ProjectID, "error", err)
		return
	}
	if project.Status == models.ProjectStatusDeleting {
		return
	}

	rollbackDeploy, err := e.deploymentService.RollbackServiceDeployment(ctx, project, svc)
	if err != nil {
		e.healthStrikeMu.Lock()
		e.lastAutoRollbackAt[svc.ID] = time.Now()
		e.healthStrikeMu.Unlock()
		if errors.Is(err, services.ErrNoDeploymentToRollback) || errors.Is(err, services.ErrActiveDeployment) {
			slog.Info("auto-rollback: skipped, no eligible prior release or deployment active (preserving current degraded release)",
				"service_id", svc.ID,
				"strikes", strikes,
				"reason", err,
			)
		} else {
			slog.Warn("auto-rollback: failed to create rollback deployment",
				"service_id", svc.ID,
				"strikes", strikes,
				"error", err,
			)
		}
		return
	}

	if qErr := e.queueEnqueuer.EnqueueServiceDeployment(ctx, rollbackDeploy.ID); qErr != nil {
		reason := fmt.Sprintf("auto-rollback enqueue failed after sustained unhealthy checks: %v", qErr)
		_ = e.deploymentService.FailServiceDeployment(ctx, rollbackDeploy.ID, reason)
		slog.Error("auto-rollback: failed to enqueue rollback deployment",
			"service_id", svc.ID,
			"service_deployment_id", rollbackDeploy.ID,
			"error", qErr,
		)
		return
	}

	e.healthStrikeMu.Lock()
	e.healthStrikes[svc.ID] = 0
	e.lastAutoRollbackAt[svc.ID] = time.Now()
	e.healthStrikeMu.Unlock()

	touchedProjects[svc.ProjectID] = true
	slog.Warn("auto-rollback: triggered automatic rollback after sustained unhealthy checks",
		"service_id", svc.ID,
		"service_name", svc.Name,
		"strikes", strikes,
		"rollback_service_deployment_id", rollbackDeploy.ID,
		"health_output", healthOutput,
	)
}
