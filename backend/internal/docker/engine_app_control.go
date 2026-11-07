package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/google/uuid"

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
