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
// kernel OOM kills, restart loops, and Docker health-check degradation, applying bounded self-healing
// and triggering automatic rollback after sustained health-check failures or crash loops.
func (e *Engine) CheckContinuousRuntimeHealth(ctx context.Context) error {
	if e.dockerClient == nil || e.serviceService == nil {
		return nil
	}

	e.runtimeHealthMu.Lock()
	defer e.runtimeHealthMu.Unlock()

	services, err := e.serviceService.ListActiveServices(ctx)
	if err != nil {
		return err
	}

	touchedProjects := make(map[uuid.UUID]bool)
	for _, svc := range services {
		if svc.ContainerID == nil || *svc.ContainerID == "" {
			continue
		}
		if e.isContainerTracked(*svc.ContainerID) || e.IsActiveExecution(svc.ID) {
			continue
		}
		if svc.CurrentServiceDeploymentID != nil && e.IsActiveExecution(*svc.CurrentServiceDeploymentID) {
			continue
		}
		if svc.Status == models.ServiceStatusDeploying ||
			svc.Status == models.DeployStatusStarting ||
			svc.Status == models.DeployStatusHealthChecking {
			continue
		}

		inspRes, err := e.dockerClient.ContainerInspect(ctx, *svc.ContainerID, client.ContainerInspectOptions{})
		if err != nil {
			if cerrdefs.IsNotFound(err) {
				if svc.Classification == models.ClassificationJob || svc.Status == models.ServiceStatusStopped {
					continue
				}
				e.StopContainerLogCollector(*svc.ContainerID)
				if svc.CurrentServiceDeploymentID != nil {
					e.StopLogCollector(*svc.CurrentServiceDeploymentID)
				}
				reason := "Runtime container terminated or disappeared from Docker daemon"
				e.markServiceCrashFailed(ctx, svc, reason, touchedProjects)
				slog.Warn("continuous health: container not found, marked service failed", "service_id", svc.ID, "container_id", *svc.ContainerID)
				e.triggerAutoRollbackIfAllowed(ctx, svc, reason, models.ServiceStatusFailed, touchedProjects)
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
			slog.Warn("continuous health: container OOM killed", "service_id", svc.ID, "container_id", *svc.ContainerID)
			e.handleContainerCrash(ctx, svc, reason, touchedProjects)
		} else if !insp.State.Running && !insp.State.Restarting {
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
				slog.Warn("continuous health: container exited with failure", "service_id", svc.ID, "exit_code", insp.State.ExitCode)
				e.handleContainerCrash(ctx, svc, reason, touchedProjects)
			} else {
				// Clean exit code 0 when service was not marked stopped: record intentional/clean stop
				if e.trafficRouter != nil {
					e.trafficRouter.RemoveRoute(svc.ID)
				}
				_ = e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusStopped, nil, nil, nil)
				slog.Info("continuous health: container exited cleanly (code 0), marked stopped", "service_id", svc.ID)
				touchedProjects[svc.ProjectID] = true
			}
		} else if insp.State.Restarting {
			e.resetHealthStrike(svc.ID)
			if svc.Status == models.ServiceStatusStopped || svc.Classification == models.ClassificationJob {
				continue
			}
			delta := e.observeRestartDelta(svc.ID, insp.RestartCount)
			eventsToRecord := 1
			if delta > eventsToRecord {
				eventsToRecord = delta
			}
			crashesInWindow, threshold, window, _, _, _ := e.recordCrashEventsAndReadPolicy(svc.ID, eventsToRecord)
			reason := fmt.Sprintf("Container is restarting continuously (observed_restarts=%d, crashes_in_window=%d/%d)", insp.RestartCount, crashesInWindow, threshold)
			if crashesInWindow >= threshold {
				e.StopContainerLogCollector(*svc.ContainerID)
				if svc.CurrentServiceDeploymentID != nil {
					e.StopLogCollector(*svc.CurrentServiceDeploymentID)
				}
				fullReason := fmt.Sprintf("Crash loop detected (%d restarts in %s): %s", crashesInWindow, window, reason)
				e.markServiceCrashFailed(ctx, svc, fullReason, touchedProjects)
				slog.Warn("continuous health: daemon restart loop escalated to crash-loop failure", "service_id", svc.ID, "crashes_in_window", crashesInWindow)
				e.triggerAutoRollbackIfAllowed(ctx, svc, fullReason, models.ServiceStatusFailed, touchedProjects)
				continue
			}
			if svc.Status != models.ServiceStatusDegraded {
				if updErr := e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusDegraded, nil, nil, nil); updErr == nil {
					touchedProjects[svc.ProjectID] = true
				}
			}
			slog.Warn("continuous health: container in restarting state, marked degraded", "service_id", svc.ID, "crashes_in_window", crashesInWindow, "threshold", threshold)
		} else if insp.State.Running {
			e.ensureRuntimeCollectorRunning(ctx, svc, *svc.ContainerID)

			if svc.Classification == models.ClassificationJob || svc.Status == models.ServiceStatusStopped {
				continue
			}

			delta := e.observeRestartDelta(svc.ID, insp.RestartCount)
			if delta > 0 {
				crashesInWindow, threshold, window, _, _, _ := e.recordCrashEventsAndReadPolicy(svc.ID, delta)
				if crashesInWindow >= threshold {
					e.StopContainerLogCollector(*svc.ContainerID)
					if svc.CurrentServiceDeploymentID != nil {
						e.StopLogCollector(*svc.CurrentServiceDeploymentID)
					}
					fullReason := fmt.Sprintf("Crash loop detected (%d restarts observed in %s)", crashesInWindow, window)
					e.markServiceCrashFailed(ctx, svc, fullReason, touchedProjects)
					slog.Warn("continuous health: restart count delta triggered crash-loop escalation", "service_id", svc.ID, "delta", delta, "crashes_in_window", crashesInWindow)
					e.triggerAutoRollbackIfAllowed(ctx, svc, fullReason, models.ServiceStatusFailed, touchedProjects)
					continue
				}
				slog.Warn("continuous health: observed background container restart", "service_id", svc.ID, "delta", delta, "restart_count", insp.RestartCount, "crashes_in_window", crashesInWindow)
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
							e.ensureRestoredServiceRoute(ctx, svc, *svc.ContainerID, svc.HostPort)
							touchedProjects[svc.ProjectID] = true
							slog.Info("continuous health: container recovered to healthy, restored running status",
								"service_id", svc.ID,
								"service_name", svc.Name,
								"container_id", *svc.ContainerID,
							)
						}
					}
				}
			} else if svc.Status == models.ServiceStatusDegraded && delta == 0 {
				if updErr := e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusRunning, nil, nil, nil); updErr == nil {
					e.ensureRestoredServiceRoute(ctx, svc, *svc.ContainerID, svc.HostPort)
					touchedProjects[svc.ProjectID] = true
					slog.Info("continuous health: container steady running, restored running status",
						"service_id", svc.ID,
						"service_name", svc.Name,
						"container_id", *svc.ContainerID,
					)
				}
			}
		}
	}

	for projID := range touchedProjects {
		_, _ = e.recalculateAndUpdateProjectStatus(ctx, projID)
		e.InvalidateProjectMetricsCache(projID)
	}

	return nil
}

func (e *Engine) recordRuntimeAudit(ctx context.Context, projectID uuid.UUID, action, resourceType, resourceID, status, message string, metadata map[string]any) {
	if e.auditService == nil {
		return
	}
	var actorID uuid.UUID
	var projectName string
	if e.projectService != nil {
		if proj, err := e.getProjectByID(ctx, projectID); err == nil && proj != nil {
			actorID = proj.OwnerID
			projectName = proj.Name
		}
	}
	if actorID == uuid.Nil {
		return
	}
	pid := projectID
	e.auditService.RecordBestEffort(ctx, services.AuditRecordInput{
		ActorID:      actorID,
		IsGuest:      models.IsGuest(actorID),
		ProjectID:    &pid,
		ProjectName:  projectName,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Status:       status,
		Message:      message,
		Metadata:     metadata,
	})
}

func (e *Engine) ensureRuntimeCollectorRunning(ctx context.Context, svc *models.Service, containerID string) {
	if svc == nil || containerID == "" {
		return
	}
	if !e.IsContainerLogCollectorActive(containerID) {
		redactor := e.buildLogRedactor(ctx, svc.ProjectID, &svc.ID, nil)
		e.StartRuntimeLogCollector(RuntimeCollectorParams{
			ContainerID:         containerID,
			ProjectID:           svc.ProjectID,
			ServiceID:           &svc.ID,
			ServiceName:         svc.Name,
			ServiceDeploymentID: svc.CurrentServiceDeploymentID,
			Redactor:            redactor,
		})
	}
}

func (e *Engine) observeRestartDelta(serviceID uuid.UUID, currentCount int) int {
	e.healthStrikeMu.Lock()
	defer e.healthStrikeMu.Unlock()
	if e.lastObservedRestartCount == nil {
		e.lastObservedRestartCount = make(map[uuid.UUID]int)
	}
	prev, seen := e.lastObservedRestartCount[serviceID]
	e.lastObservedRestartCount[serviceID] = currentCount
	if !seen {
		return 0
	}
	if currentCount > prev {
		return currentCount - prev
	}
	return 0
}

func (e *Engine) setObservedRestartCount(serviceID uuid.UUID, count int) {
	e.healthStrikeMu.Lock()
	defer e.healthStrikeMu.Unlock()
	if e.lastObservedRestartCount == nil {
		e.lastObservedRestartCount = make(map[uuid.UUID]int)
	}
	e.lastObservedRestartCount[serviceID] = count
}

func (e *Engine) recordCrashEventsAndReadPolicy(serviceID uuid.UUID, count int) (crashesInWindow, crashLoopThreshold int, crashLoopWindow time.Duration, selfHealEnabled bool, restartsAttempted, maxRestarts int) {
	if count <= 0 {
		count = 1
	}
	now := time.Now()

	e.healthStrikeMu.Lock()
	defer e.healthStrikeMu.Unlock()

	if e.crashEvents == nil {
		e.crashEvents = make(map[uuid.UUID][]time.Time)
	}
	if e.selfHealRestarts == nil {
		e.selfHealRestarts = make(map[uuid.UUID]int)
	}

	window := e.crashLoopWindow
	if window <= 0 {
		window = 10 * time.Minute
	}
	threshold := e.crashLoopThreshold
	if threshold <= 0 {
		threshold = 3
	}

	cutoff := now.Add(-window)
	existing := e.crashEvents[serviceID]
	pruned := existing[:0]
	for _, ts := range existing {
		if ts.After(cutoff) || ts.Equal(cutoff) {
			pruned = append(pruned, ts)
		}
	}
	for i := 0; i < count; i++ {
		pruned = append(pruned, now)
	}
	e.crashEvents[serviceID] = pruned

	return len(pruned), threshold, window, e.selfHealEnabled, e.selfHealRestarts[serviceID], e.selfHealMaxRestarts
}

func (e *Engine) incrementSelfHealRestart(serviceID uuid.UUID) int {
	e.healthStrikeMu.Lock()
	defer e.healthStrikeMu.Unlock()
	if e.selfHealRestarts == nil {
		e.selfHealRestarts = make(map[uuid.UUID]int)
	}
	e.selfHealRestarts[serviceID]++
	return e.selfHealRestarts[serviceID]
}

func (e *Engine) markServiceCrashFailed(ctx context.Context, svc *models.Service, reason string, touchedProjects map[uuid.UUID]bool) {
	if e.trafficRouter != nil {
		e.trafficRouter.RemoveRoute(svc.ID)
	}
	if svc.CurrentServiceDeploymentID != nil && e.deploymentService != nil {
		if err := e.deploymentService.UpdateServiceDeploymentStatus(ctx, *svc.CurrentServiceDeploymentID, models.DeployStatusCrashed, &reason); err != nil {
			_ = e.deploymentService.FailServiceDeployment(ctx, *svc.CurrentServiceDeploymentID, reason)
		}
	}
	_ = e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusFailed, nil, nil, nil)
	touchedProjects[svc.ProjectID] = true
}

func (e *Engine) handleContainerCrash(ctx context.Context, svc *models.Service, reason string, touchedProjects map[uuid.UUID]bool) {
	crashesInWindow, crashLoopThreshold, crashLoopWindow, selfHealEnabled, restartsAttempted, maxRestarts := e.recordCrashEventsAndReadPolicy(svc.ID, 1)

	if !selfHealEnabled || maxRestarts <= 0 {
		e.markServiceCrashFailed(ctx, svc, reason, touchedProjects)
		e.triggerAutoRollbackIfAllowed(ctx, svc, reason, models.ServiceStatusFailed, touchedProjects)
		return
	}

	if crashesInWindow >= crashLoopThreshold || restartsAttempted >= maxRestarts {
		fullReason := fmt.Sprintf("Crash loop detected (%d crash(es) within %s; self-heal restarts %d/%d): %s", crashesInWindow, crashLoopWindow, restartsAttempted, maxRestarts, reason)
		e.markServiceCrashFailed(ctx, svc, fullReason, touchedProjects)
		e.recordRuntimeAudit(ctx, svc.ProjectID, "service.self_heal", "service", svc.ID.String(), models.AuditStatusFailure, fullReason, map[string]any{
			"service_name":       svc.Name,
			"crashes_in_window":  crashesInWindow,
			"restarts_attempted": restartsAttempted,
			"max_restarts":       maxRestarts,
		})
		slog.Warn("continuous health: crash loop or restart budget exhausted, marking service failed",
			"service_id", svc.ID,
			"crashes_in_window", crashesInWindow,
			"restarts_attempted", restartsAttempted,
			"max_restarts", maxRestarts,
		)
		e.triggerAutoRollbackIfAllowed(ctx, svc, fullReason, models.ServiceStatusFailed, touchedProjects)
		return
	}

	attempt := e.incrementSelfHealRestart(svc.ID)
	if svc.Status != models.ServiceStatusDegraded {
		if updErr := e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusDegraded, nil, nil, nil); updErr == nil {
			touchedProjects[svc.ProjectID] = true
		}
	}
	slog.Warn("continuous health: attempting bounded self-heal container restart",
		"service_id", svc.ID,
		"service_name", svc.Name,
		"container_id", *svc.ContainerID,
		"attempt", attempt,
		"max_restarts", maxRestarts,
		"crashes_in_window", crashesInWindow,
		"reason", reason,
	)

	restartTimeout := 10
	_, restartErr := e.dockerClient.ContainerRestart(ctx, *svc.ContainerID, client.ContainerRestartOptions{Timeout: &restartTimeout})
	if restartErr == nil {
		postInspRes, postErr := e.dockerClient.ContainerInspect(ctx, *svc.ContainerID, client.ContainerInspectOptions{})
		if postErr == nil && postInspRes.Container.State != nil && postInspRes.Container.State.Running && !postInspRes.Container.State.OOMKilled {
			e.setObservedRestartCount(svc.ID, postInspRes.Container.RestartCount)
			e.ensureRuntimeCollectorRunning(ctx, svc, *svc.ContainerID)
			e.ensureRestoredServiceRoute(ctx, svc, *svc.ContainerID, svc.HostPort)

			if postInspRes.Container.State.Health != nil {
				switch postInspRes.Container.State.Health.Status {
				case container.Unhealthy:
					// Fall through to failure handling below
				case container.Starting:
					touchedProjects[svc.ProjectID] = true
					e.recordRuntimeAudit(ctx, svc.ProjectID, "service.self_heal", "service", svc.ID.String(), models.AuditStatusWarning, fmt.Sprintf("Restarted service '%s' (attempt %d/%d), awaiting health check", svc.Name, attempt, maxRestarts), map[string]any{
						"service_name": svc.Name,
						"attempt":      attempt,
						"max_restarts": maxRestarts,
						"reason":       reason,
					})
					slog.Info("continuous health: self-healed container restarted, awaiting health check completion",
						"service_id", svc.ID,
						"attempt", attempt,
					)
					return
				default:
					if updErr := e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusRunning, nil, nil, nil); updErr == nil {
						touchedProjects[svc.ProjectID] = true
					}
					e.recordRuntimeAudit(ctx, svc.ProjectID, "service.self_heal", "service", svc.ID.String(), models.AuditStatusSuccess, fmt.Sprintf("Self-healed service '%s' and verified healthy/running (attempt %d/%d)", svc.Name, attempt, maxRestarts), map[string]any{
						"service_name": svc.Name,
						"attempt":      attempt,
						"max_restarts": maxRestarts,
						"reason":       reason,
					})
					slog.Info("continuous health: self-healed service container and verified healthy/running",
						"service_id", svc.ID,
						"attempt", attempt,
					)
					return
				}
			} else {
				if updErr := e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusRunning, nil, nil, nil); updErr == nil {
					touchedProjects[svc.ProjectID] = true
				}
				e.recordRuntimeAudit(ctx, svc.ProjectID, "service.self_heal", "service", svc.ID.String(), models.AuditStatusSuccess, fmt.Sprintf("Self-healed service '%s' and verified running (attempt %d/%d)", svc.Name, attempt, maxRestarts), map[string]any{
					"service_name": svc.Name,
					"attempt":      attempt,
					"max_restarts": maxRestarts,
					"reason":       reason,
				})
				slog.Info("continuous health: self-healed service container and verified running",
					"service_id", svc.ID,
					"attempt", attempt,
				)
				return
			}
		}
	}

	slog.Warn("continuous health: self-heal restart failed recovery verification",
		"service_id", svc.ID,
		"attempt", attempt,
		"max_restarts", maxRestarts,
		"restart_error", restartErr,
	)

	if attempt >= maxRestarts {
		fullReason := fmt.Sprintf("Self-healing exhausted after %d/%d restart attempts: %s", attempt, maxRestarts, reason)
		e.markServiceCrashFailed(ctx, svc, fullReason, touchedProjects)
		e.recordRuntimeAudit(ctx, svc.ProjectID, "service.self_heal", "service", svc.ID.String(), models.AuditStatusFailure, fullReason, map[string]any{
			"service_name": svc.Name,
			"attempt":      attempt,
			"max_restarts": maxRestarts,
		})
		e.triggerAutoRollbackIfAllowed(ctx, svc, fullReason, models.ServiceStatusFailed, touchedProjects)
	}
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

	if !canAttempt {
		return
	}
	e.triggerAutoRollback(ctx, svc, fmt.Sprintf("sustained unhealthy checks (strikes=%d, output=%s)", strikes, healthOutput), models.ServiceStatusDegraded, touchedProjects)
}

func (e *Engine) triggerAutoRollbackIfAllowed(ctx context.Context, svc *models.Service, reason string, fallbackStatus string, touchedProjects map[uuid.UUID]bool) {
	e.healthStrikeMu.Lock()
	if e.lastAutoRollbackAt == nil {
		e.lastAutoRollbackAt = make(map[uuid.UUID]time.Time)
	}
	cooldown := e.autoRollbackCooldown
	if cooldown < 0 {
		cooldown = 5 * time.Minute
	}
	enabled := e.autoRollbackEnabled
	lastAttempt := e.lastAutoRollbackAt[svc.ID]
	canAttempt := enabled && (lastAttempt.IsZero() || time.Since(lastAttempt) >= cooldown)
	e.healthStrikeMu.Unlock()

	if !canAttempt {
		return
	}
	e.triggerAutoRollback(ctx, svc, reason, fallbackStatus, touchedProjects)
}

func (e *Engine) triggerAutoRollback(ctx context.Context, svc *models.Service, triggerReason string, fallbackStatus string, touchedProjects map[uuid.UUID]bool) bool {
	if e.queueEnqueuer == nil || e.deploymentService == nil || e.projectService == nil || e.serviceService == nil {
		return false
	}

	project, err := e.getProjectByID(ctx, svc.ProjectID)
	if err != nil {
		slog.Warn("auto-rollback: failed to load project for service", "service_id", svc.ID, "project_id", svc.ProjectID, "error", err)
		return false
	}
	if project.Status == models.ProjectStatusDeleting {
		return false
	}

	rollbackDeploy, err := e.deploymentService.RollbackServiceDeployment(ctx, project, svc)
	if err != nil {
		e.healthStrikeMu.Lock()
		if e.lastAutoRollbackAt == nil {
			e.lastAutoRollbackAt = make(map[uuid.UUID]time.Time)
		}
		e.lastAutoRollbackAt[svc.ID] = time.Now()
		e.healthStrikeMu.Unlock()
		if errors.Is(err, services.ErrNoDeploymentToRollback) || errors.Is(err, services.ErrActiveDeployment) {
			slog.Info("auto-rollback: skipped, no eligible prior release or deployment active (preserving current state)",
				"service_id", svc.ID,
				"trigger", triggerReason,
				"reason", err,
			)
		} else {
			slog.Warn("auto-rollback: failed to create rollback deployment",
				"service_id", svc.ID,
				"trigger", triggerReason,
				"error", err,
			)
		}
		return false
	}

	if qErr := e.queueEnqueuer.EnqueueServiceDeployment(ctx, rollbackDeploy.ID); qErr != nil {
		reason := fmt.Sprintf("auto-rollback enqueue failed (%s): %v", triggerReason, qErr)
		_ = e.deploymentService.FailServiceDeployment(ctx, rollbackDeploy.ID, reason)
		if fallbackStatus != "" {
			_ = e.serviceService.UpdateServiceStatus(ctx, svc.ID, fallbackStatus, nil, nil, nil)
		}
		slog.Error("auto-rollback: failed to enqueue rollback deployment",
			"service_id", svc.ID,
			"service_deployment_id", rollbackDeploy.ID,
			"error", qErr,
		)
		return false
	}

	e.healthStrikeMu.Lock()
	if e.healthStrikes != nil {
		e.healthStrikes[svc.ID] = 0
	}
	if e.crashEvents != nil {
		delete(e.crashEvents, svc.ID)
	}
	if e.selfHealRestarts != nil {
		delete(e.selfHealRestarts, svc.ID)
	}
	if e.lastObservedRestartCount != nil {
		delete(e.lastObservedRestartCount, svc.ID)
	}
	if e.lastAutoRollbackAt == nil {
		e.lastAutoRollbackAt = make(map[uuid.UUID]time.Time)
	}
	e.lastAutoRollbackAt[svc.ID] = time.Now()
	e.healthStrikeMu.Unlock()

	touchedProjects[svc.ProjectID] = true
	if e.auditService != nil && project.OwnerID != uuid.Nil {
		pid := project.ID
		e.auditService.RecordBestEffort(ctx, services.AuditRecordInput{
			ActorID:      project.OwnerID,
			IsGuest:      models.IsGuest(project.OwnerID),
			ProjectID:    &pid,
			ProjectName:  project.Name,
			Action:       "service.auto_rollback",
			ResourceType: "service",
			ResourceID:   svc.ID.String(),
			Status:       models.AuditStatusWarning,
			Message:      fmt.Sprintf("Automatic rollback triggered for service '%s': %s", svc.Name, triggerReason),
			Metadata: map[string]any{
				"service_name":                   svc.Name,
				"rollback_service_deployment_id": rollbackDeploy.ID.String(),
				"trigger":                        triggerReason,
			},
		})
	}
	slog.Warn("auto-rollback: triggered automatic rollback",
		"service_id", svc.ID,
		"service_name", svc.Name,
		"rollback_service_deployment_id", rollbackDeploy.ID,
		"trigger", triggerReason,
	)
	return true
}
