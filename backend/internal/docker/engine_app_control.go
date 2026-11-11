package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/client"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

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

	var svcs []*models.Service
	if e.serviceService != nil {
		svcs, err = e.serviceService.ListServices(ctx, projectID)
		if err != nil {
			return fmt.Errorf("failed to list services: %w", err)
		}
	}

	hasServiceContainers := false
	for _, svc := range svcs {
		if svc.ContainerID != nil && *svc.ContainerID != "" {
			hasServiceContainers = true
			break
		}
	}

	if project.CurrentDeploymentID == nil && !hasServiceContainers {
		return errors.New("project has no active deployment to stop")
	}

	// 1. Stop legacy deployment container if present
	if project.CurrentDeploymentID != nil && e.deploymentService != nil {
		deployment, err := e.deploymentService.GetDeployment(ctx, *project.CurrentDeploymentID)
		if err == nil {
			if deployment.ContainerID != nil && *deployment.ContainerID != "" {
				e.StopLogCollector(deployment.ID)
				e.StopContainerLogCollector(*deployment.ContainerID)
				if _, err := e.dockerClient.ContainerStop(ctx, *deployment.ContainerID, client.ContainerStopOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
					return fmt.Errorf("failed to stop container: %w", err)
				}
			}
			if (deployment.ContainerID != nil && *deployment.ContainerID != "") || hasServiceContainers {
				if err := e.deploymentService.UpdateDeploymentStatus(ctx, deployment.ID, models.DeployStatusStopped, nil); err != nil {
					return fmt.Errorf("failed to update deployment status: %w", err)
				}
			}
		}
	}

	// 2. Stop all service containers for multi-service configurations
	if e.serviceService != nil {
		for _, svc := range svcs {
			e.resetHealthStrike(svc.ID)
			if svc.Classification == models.ClassificationJob &&
				svc.Status != models.ServiceStatusRunning &&
				svc.Status != models.ServiceStatusDeploying &&
				svc.Status != models.ServiceStatusDegraded {
				// Completed/failed/inactive one-shot jobs do not need stopping
				continue
			}
			if svc.CurrentServiceDeploymentID != nil {
				e.StopLogCollector(*svc.CurrentServiceDeploymentID)
			}
			if svc.ContainerID != nil && *svc.ContainerID != "" {
				e.StopContainerLogCollector(*svc.ContainerID)
				if _, err := e.dockerClient.ContainerStop(ctx, *svc.ContainerID, client.ContainerStopOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
					return fmt.Errorf("failed to stop service container: %w", err)
				}
			}
			if err := e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusStopped, nil, nil, nil); err != nil {
				return fmt.Errorf("failed to update service status: %w", err)
			}
			if e.trafficRouter != nil {
				e.trafficRouter.RemoveRoute(svc.ID)
			}
		}
	}

	newStatus, err := e.recalculateAndUpdateProjectStatus(ctx, projectID)
	if err != nil {
		return fmt.Errorf("failed to update project status: %w", err)
	}
	if newStatus == "" || len(svcs) == 0 {
		newStatus = models.ProjectStatusStopped
		if e.projectService != nil {
			_ = e.projectService.UpdateProjectStatus(ctx, projectID, newStatus)
		}
	}
	if project.CurrentDeploymentID != nil && e.deploymentService != nil {
		if err := e.deploymentService.SetCurrentDeployment(ctx, projectID, *project.CurrentDeploymentID, newStatus); err != nil {
			return fmt.Errorf("failed to set current deployment: %w", err)
		}
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

	var svcs []*models.Service
	if e.serviceService != nil {
		svcs, err = e.serviceService.ListServices(ctx, projectID)
		if err != nil {
			return fmt.Errorf("failed to list services: %w", err)
		}
	}

	hasStartableService := false
	for _, svc := range svcs {
		if svc.Classification != models.ClassificationJob && svc.ContainerID != nil && *svc.ContainerID != "" {
			hasStartableService = true
			break
		}
	}

	if project.CurrentDeploymentID == nil && !hasStartableService {
		return errors.New("project has no active deployment to start")
	}

	// 1. Start legacy deployment container if present
	startedLegacy := false
	if project.CurrentDeploymentID != nil && e.deploymentService != nil {
		deployment, err := e.deploymentService.GetDeployment(ctx, *project.CurrentDeploymentID)
		if err == nil && deployment.ContainerID != nil && *deployment.ContainerID != "" {
			if _, err := e.dockerClient.ContainerStart(ctx, *deployment.ContainerID, client.ContainerStartOptions{}); err != nil {
				return fmt.Errorf("failed to start container: %w", err)
			}
			startedLegacy = true
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
	}

	// 2. Start all non-job service containers for multi-service configurations
	startedServices := 0
	if e.serviceService != nil {
		for _, svc := range svcs {
			e.resetHealthStrike(svc.ID)
			if svc.Classification == models.ClassificationJob {
				// Never re-run one-shot job containers during StartApp
				continue
			}
			if svc.ContainerID == nil || *svc.ContainerID == "" {
				continue
			}
			if _, err := e.dockerClient.ContainerStart(ctx, *svc.ContainerID, client.ContainerStartOptions{}); err != nil {
				return fmt.Errorf("failed to start service container: %w", err)
			}
			startedServices++
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
			if err := e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusRunning, nil, nil, nil); err != nil {
				return fmt.Errorf("failed to update service status: %w", err)
			}
			if e.trafficRouter != nil && svc.HostPort != nil && *svc.HostPort > 0 && (svc.PublicExposed || svc.Role == models.RoleFrontend) {
				_, _ = e.trafficRouter.SwitchTraffic(svc.ID, project.ID, svc.Name, e.GetAppHostIP(), *svc.HostPort, *svc.ContainerID)
			}
		}
	}

	if project.CurrentDeploymentID != nil && !startedLegacy && startedServices > 0 && e.deploymentService != nil {
		if err := e.deploymentService.UpdateDeploymentStatus(ctx, *project.CurrentDeploymentID, models.DeployStatusRunning, nil); err != nil {
			return fmt.Errorf("failed to update deployment status: %w", err)
		}
	}

	newStatus, err := e.recalculateAndUpdateProjectStatus(ctx, projectID)
	if err != nil {
		return fmt.Errorf("failed to update project status: %w", err)
	}
	if newStatus == "" || len(svcs) == 0 {
		newStatus = models.ProjectStatusRunning
		if e.projectService != nil {
			_ = e.projectService.UpdateProjectStatus(ctx, projectID, newStatus)
		}
	}
	if project.CurrentDeploymentID != nil && e.deploymentService != nil {
		if err := e.deploymentService.SetCurrentDeployment(ctx, projectID, *project.CurrentDeploymentID, newStatus); err != nil {
			return fmt.Errorf("failed to set current deployment: %w", err)
		}
	}

	slog.Info("project app started", "project_id", projectID, "services_started", startedServices)
	return nil
}

// RestartApp restarts the containers for a project, supporting single-container,
// multi-service deployments, and safely skipping one-shot job workloads.
func (e *Engine) RestartApp(ctx context.Context, projectID, ownerID uuid.UUID) error {
	project, err := e.projectService.GetProject(ctx, projectID, ownerID)
	if err != nil {
		return err
	}

	var svcs []*models.Service
	if e.serviceService != nil {
		svcs, err = e.serviceService.ListServices(ctx, projectID)
		if err != nil {
			return fmt.Errorf("failed to list services: %w", err)
		}
	}

	hasRestartableService := false
	for _, svc := range svcs {
		if svc.Classification != models.ClassificationJob && svc.ContainerID != nil && *svc.ContainerID != "" {
			hasRestartableService = true
			break
		}
	}

	if project.CurrentDeploymentID == nil && !hasRestartableService {
		return errors.New("project has no active deployment to restart")
	}

	var deployment *models.Deployment
	if project.CurrentDeploymentID != nil && e.deploymentService != nil {
		deployment, err = e.deploymentService.GetDeployment(ctx, *project.CurrentDeploymentID)
		if err != nil {
			if !hasRestartableService {
				return err
			}
			deployment = nil
		}
	}

	timeout := 10
	restartedLegacy := false
	if deployment != nil && deployment.ContainerID != nil && *deployment.ContainerID != "" {
		e.StopLogCollector(deployment.ID)
		e.StopContainerLogCollector(*deployment.ContainerID)
		if _, err := e.dockerClient.ContainerRestart(ctx, *deployment.ContainerID, client.ContainerRestartOptions{Timeout: &timeout}); err != nil {
			return fmt.Errorf("failed to restart container: %w", err)
		}
		restartedLegacy = true
		redactor := e.buildLogRedactor(ctx, project.ID, nil, deployment.EnvSnapshot)
		e.StartRuntimeLogCollector(RuntimeCollectorParams{
			ContainerID:  *deployment.ContainerID,
			ProjectID:    project.ID,
			DeploymentID: &deployment.ID,
			Redactor:     redactor,
		})
	}

	restartedServices := 0
	if e.serviceService != nil {
		for _, svc := range svcs {
			e.resetHealthStrike(svc.ID)
			if svc.Classification == models.ClassificationJob {
				// Never re-run one-shot jobs during project-wide RestartApp
				continue
			}
			if svc.ContainerID == nil || *svc.ContainerID == "" {
				continue
			}
			if svc.CurrentServiceDeploymentID != nil {
				e.StopLogCollector(*svc.CurrentServiceDeploymentID)
			}
			e.StopContainerLogCollector(*svc.ContainerID)
			if _, err := e.dockerClient.ContainerRestart(ctx, *svc.ContainerID, client.ContainerRestartOptions{Timeout: &timeout}); err != nil {
				return fmt.Errorf("failed to restart service container %s: %w", svc.Name, err)
			}
			restartedServices++
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
			if err := e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusRunning, nil, nil, nil); err != nil {
				return fmt.Errorf("failed to update service status: %w", err)
			}
			if e.trafficRouter != nil && svc.HostPort != nil && *svc.HostPort > 0 && (svc.PublicExposed || svc.Role == models.RoleFrontend) {
				_, _ = e.trafficRouter.SwitchTraffic(svc.ID, project.ID, svc.Name, e.GetAppHostIP(), *svc.HostPort, *svc.ContainerID)
			}
		}
	}

	if !restartedLegacy && restartedServices == 0 && len(svcs) > 0 {
		return errors.New("no restartable service containers found")
	}

	newStatus, err := e.recalculateAndUpdateProjectStatus(ctx, projectID)
	if err != nil {
		return fmt.Errorf("failed to update project status: %w", err)
	}
	if newStatus == "" || len(svcs) == 0 {
		newStatus = models.ProjectStatusRunning
		if e.projectService != nil {
			_ = e.projectService.UpdateProjectStatus(ctx, projectID, newStatus)
		}
	}

	if deployment != nil && e.deploymentService != nil {
		if err := e.deploymentService.UpdateDeploymentStatus(ctx, deployment.ID, models.DeployStatusRunning, nil); err != nil {
			return fmt.Errorf("failed to update deployment status: %w", err)
		}
		if err := e.deploymentService.SetCurrentDeployment(ctx, projectID, deployment.ID, newStatus); err != nil {
			return fmt.Errorf("failed to set current deployment: %w", err)
		}
	}

	slog.Info("project app restarted", "project_id", projectID, "services_restarted", restartedServices, "legacy_restarted", restartedLegacy)
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
	e.resetHealthStrike(svc.ID)
	if svc.CurrentServiceDeploymentID != nil {
		e.StopLogCollector(*svc.CurrentServiceDeploymentID)
	}
	if svc.ContainerID != nil && *svc.ContainerID != "" {
		e.StopContainerLogCollector(*svc.ContainerID)
		if _, err := e.dockerClient.ContainerStop(ctx, *svc.ContainerID, client.ContainerStopOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("failed to stop service container: %w", err)
		}
	}
	if err := e.serviceService.UpdateServiceStatus(ctx, serviceID, models.ServiceStatusStopped, nil, nil, nil); err != nil {
		return fmt.Errorf("failed to update service status: %w", err)
	}
	if e.trafficRouter != nil {
		e.trafficRouter.RemoveRoute(serviceID)
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
	e.resetHealthStrike(svc.ID)
	if svc.ContainerID != nil && *svc.ContainerID != "" {
		if _, err := e.dockerClient.ContainerStart(ctx, *svc.ContainerID, client.ContainerStartOptions{}); err != nil {
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
	if e.trafficRouter != nil && svc.ContainerID != nil && *svc.ContainerID != "" && svc.HostPort != nil && *svc.HostPort > 0 && (svc.PublicExposed || svc.Role == models.RoleFrontend) {
		_, _ = e.trafficRouter.SwitchTraffic(svc.ID, project.ID, svc.Name, e.GetAppHostIP(), *svc.HostPort, *svc.ContainerID)
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
