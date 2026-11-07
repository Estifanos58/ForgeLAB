package docker

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
	ws "github.com/forgelab/backend/internal/websocket"
)

// recalculateAndUpdateProjectStatus derives the project status from its services and persists it.
func (e *Engine) recalculateAndUpdateProjectStatus(ctx context.Context, projectID uuid.UUID) (string, error) {
	if e.serviceService == nil {
		return models.ProjectStatusInactive, nil
	}
	svcs, err := e.serviceService.ListServices(ctx, projectID)
	if err != nil {
		return "", err
	}
	if len(svcs) == 0 {
		return models.ProjectStatusInactive, nil
	}
	newStatus := services.CalculateProjectStatus(svcs)
	if e.projectService != nil {
		_ = e.projectService.UpdateProjectStatus(ctx, projectID, newStatus)
	}
	if e.wsHub != nil {
		projectChannel := fmt.Sprintf("project:%s", projectID.String())
		_ = e.wsHub.PublishEvent(projectChannel, &ws.EventMessage{
			Type:    "project_status",
			Channel: projectChannel,
			Data: map[string]interface{}{
				"project_id": projectID.String(),
				"status":     newStatus,
			},
		})
	}
	return newStatus, nil
}

// toCoarseServiceStatus maps a detailed deployment phase to a coarse service-level status.
// Service.status must remain coarse only: inactive, deploying, running, stopped, failed.
// If the deployment fails or crashes, but a previous healthy container is still running,
// the service status remains running.
func toCoarseServiceStatus(deployStatus string, hasPreviousHealthy bool) string {
	switch deployStatus {
	case models.DeployStatusQueued,
		models.DeployStatusCloning,
		models.DeployStatusBuilding,
		models.DeployStatusStarting,
		models.DeployStatusHealthChecking:
		return models.ServiceStatusDeploying
	case models.DeployStatusRunning:
		return models.ServiceStatusRunning
	case models.DeployStatusStopped:
		return models.ServiceStatusStopped
	case models.DeployStatusFailed, models.DeployStatusCrashed:
		if hasPreviousHealthy {
			return models.ServiceStatusRunning
		}
		return models.ServiceStatusFailed
	default:
		return models.ServiceStatusDeploying
	}
}
