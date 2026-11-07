package docker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/models"
)

// ReconcileDaemonContainers reconciles DB container state against Docker daemon state on startup:
// 1. Detects containers stopped/crashed while backend was offline and marks services/projects as crashed/stopped.
// 2. Cleans up orphaned ForgeLAB containers whose project or deployment is terminated.
// 3. Syncs active host port allocations with the in-memory port manager.
func (e *Engine) ReconcileDaemonContainers(ctx context.Context) error {
	if e.dockerClient == nil {
		e.reconcileMu.Lock()
		e.reconcileState = ReconciliationState{
			Status:        "unreachable",
			LastError:     "docker client not initialized",
			Authoritative: false,
		}
		e.reconcileMu.Unlock()
		return nil
	}

	containers, err := e.dockerClient.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		e.reconcileMu.Lock()
		e.reconcileState = ReconciliationState{
			Status:        "failed",
			LastError:     err.Error(),
			Authoritative: false,
		}
		e.reconcileMu.Unlock()
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

		if c.State == "running" || c.State == "restarting" || c.State == "created" || c.State == "paused" {
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

	now := time.Now().UTC()
	e.reconcileMu.Lock()
	e.reconcileState = ReconciliationState{
		Status:         "completed",
		LastReconciled: &now,
		Authoritative:  true,
	}
	e.reconcileMu.Unlock()

	return nil
}
