package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/forgelab/backend/internal/models"
)

// GetProject retrieves a project by ID, verifying ownership.
func (s *ProjectService) GetProject(ctx context.Context, projectID, ownerID uuid.UUID) (*models.Project, error) {
	if s.projectGetterForTest != nil {
		return s.projectGetterForTest(ctx, projectID, ownerID)
	}

	project, err := s.GetProjectByIDWithoutOwnership(ctx, projectID)
	if err != nil {
		return nil, err
	}

	if project.OwnerID != ownerID {
		return nil, ErrProjectNotOwned
	}

	return project, nil
}

// ListProjects retrieves all projects for a user with bounded pagination and batch-loaded services (no N+1).
func (s *ProjectService) ListProjects(ctx context.Context, ownerID uuid.UUID, pagination ...int) ([]*models.Project, error) {
	limit := 50
	offset := 0
	if len(pagination) > 0 && pagination[0] > 0 {
		limit = pagination[0]
		if limit > 200 {
			limit = 200
		}
	}
	if len(pagination) > 1 && pagination[1] >= 0 {
		offset = pagination[1]
	}

	rows, err := s.db.Query(ctx,
		`SELECT id, owner_id, source_id, name, slug, source_type, source_reference, repository_path, branch,
		 dockerfile_path, build_context, build_strategy, build_command, start_command,
		 runtime_type, internal_port, health_check_path, health_check_enabled, health_strategy,
		 status, current_deployment_id, port, created_at, updated_at,
		 COALESCE(deployment_strategy, 'auto'), COALESCE(deployment_plan, '{}'::jsonb)
		 FROM projects WHERE owner_id = $1 ORDER BY created_at DESC
		 LIMIT $2 OFFSET $3`,
		ownerID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}
	defer rows.Close()

	var projects []*models.Project
	var projectIDs []uuid.UUID
	for rows.Next() {
		p := &models.Project{}
		err := rows.Scan(
			&p.ID, &p.OwnerID, &p.SourceID, &p.Name, &p.Slug, &p.SourceType, &p.SourceReference, &p.RepositoryPath,
			&p.Branch, &p.DockerfilePath, &p.BuildContext, &p.BuildStrategy, &p.BuildCommand,
			&p.StartCommand, &p.RuntimeType, &p.InternalPort, &p.HealthCheckPath,
			&p.HealthCheckEnabled, &p.HealthStrategy, &p.Status, &p.CurrentDeploymentID, &p.Port,
			&p.CreatedAt, &p.UpdatedAt, &p.DeploymentStrategy, &p.DeploymentPlan,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan project: %w", err)
		}
		projects = append(projects, p)
		projectIDs = append(projectIDs, p.ID)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error reading projects: %w", err)
	}

	// Single batch query for all services across all fetched projects (resolves N+1 query issue)
	var servicesByProject map[uuid.UUID][]*models.Service
	if s.db != nil && len(projectIDs) > 0 {
		servicesByProject, _ = s.getServiceService().ListServicesByProjectIDs(ctx, projectIDs)
	}

	for _, p := range projects {
		var svcs []*models.Service
		if servicesByProject != nil {
			svcs = servicesByProject[p.ID]
		}
		if len(svcs) == 0 {
			svcs = []*models.Service{
				{
					ID:                 p.ID,
					ProjectID:          p.ID,
					SourceID:           p.SourceID,
					Name:               p.Name,
					Role:               models.RoleOther,
					Classification:     models.ClassificationApplication,
					SourcePath:         p.BuildContext,
					RuntimeType:        p.RuntimeType,
					BuildStrategy:      p.BuildStrategy,
					BuildCommand:       p.BuildCommand,
					StartCommand:       p.StartCommand,
					DockerfilePath:     p.DockerfilePath,
					BuildContext:       p.BuildContext,
					InternalPort:       p.InternalPort,
					HostPort:           p.Port,
					PublicExposed:      true,
					HealthStrategy:     p.HealthStrategy,
					HealthCheckPath:    p.HealthCheckPath,
					HealthCheckEnabled: p.HealthCheckEnabled,
					Status:             p.Status,
				},
			}
		}
		p.Services = svcs
		if len(svcs) > 0 {
			derivedStatus := CalculateProjectStatus(svcs)
			if p.Status != models.ProjectStatusDeploying && p.Status != models.ProjectStatusDeleting && p.Status != derivedStatus {
				p.Status = derivedStatus
				_ = s.UpdateProjectStatus(ctx, p.ID, derivedStatus)
			}
		}
		s.populateProjectPreviewURL(p)
	}

	if projects == nil {
		projects = []*models.Project{}
	}

	return projects, nil
}

// ListActiveProjects retrieves all projects currently marked as running or active.
func (s *ProjectService) ListActiveProjects(ctx context.Context) ([]*models.Project, error) {
	if s.db == nil {
		return []*models.Project{}, nil
	}

	rows, err := s.db.Query(ctx,
		`SELECT id, owner_id, source_id, name, slug, source_type, source_reference, repository_path, branch,
		 dockerfile_path, build_context, build_strategy, build_command, start_command,
		 runtime_type, internal_port, health_check_path, health_check_enabled, health_strategy,
		 status, current_deployment_id, port, created_at, updated_at,
		 COALESCE(deployment_strategy, 'auto'), COALESCE(deployment_plan, '{}'::jsonb)
		 FROM projects WHERE status IN ('running', 'partially_running', 'deploying') ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query active projects: %w", err)
	}
	defer rows.Close()

	var projects []*models.Project
	for rows.Next() {
		p := &models.Project{}
		err := rows.Scan(
			&p.ID, &p.OwnerID, &p.SourceID, &p.Name, &p.Slug, &p.SourceType, &p.SourceReference, &p.RepositoryPath,
			&p.Branch, &p.DockerfilePath, &p.BuildContext, &p.BuildStrategy, &p.BuildCommand,
			&p.StartCommand, &p.RuntimeType, &p.InternalPort, &p.HealthCheckPath,
			&p.HealthCheckEnabled, &p.HealthStrategy, &p.Status, &p.CurrentDeploymentID, &p.Port,
			&p.CreatedAt, &p.UpdatedAt, &p.DeploymentStrategy, &p.DeploymentPlan,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan active project: %w", err)
		}
		projects = append(projects, p)
	}

	if projects == nil {
		projects = []*models.Project{}
	}
	return projects, rows.Err()
}

// UpdateProject updates project settings.
func (s *ProjectService) UpdateProject(ctx context.Context, projectID, ownerID uuid.UUID, input UpdateProjectInput) (*models.Project, error) {
	project, err := s.GetProject(ctx, projectID, ownerID)
	if err != nil {
		return nil, err
	}

	if input.Name != nil {
		project.Name = *input.Name
		project.Slug = generateSlug(*input.Name)
	}
	if input.SourceReference != nil {
		newRef := strings.TrimSpace(*input.SourceReference)
		if newRef != "" && project.SourceType == models.SourceTypeLocal {
			sourceUUID, err := uuid.Parse(newRef)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid source upload ID", ErrInvalidSource)
			}
			if s.sourceService != nil {
				if _, err := s.sourceService.GetSourcePath(ctx, ownerID, sourceUUID); err != nil {
					return nil, fmt.Errorf("%w: source files not found, expired, or access denied", ErrInvalidSource)
				}
			}
		}
		project.SourceReference = newRef
	}
	if input.RepositoryPath != nil {
		newPath := strings.TrimSpace(*input.RepositoryPath)
		if project.SourceType == models.SourceTypeLocalAgent {
			newPath = ""
		} else if newPath != "" && (project.SourceType == models.SourceTypeLocalDirectory || project.SourceType == models.SourceTypeLocal) {
			if s.pathValidator != nil {
				canonical, err := s.pathValidator.ValidateSourcePath(newPath)
				if err != nil {
					return nil, fmt.Errorf("%w: %v", ErrInvalidSource, err)
				}
				newPath = canonical
			}
		}
		project.RepositoryPath = newPath
	}
	if input.Branch != nil {
		project.Branch = *input.Branch
	}
	if input.DockerfilePath != nil {
		project.DockerfilePath = *input.DockerfilePath
	}
	if input.BuildContext != nil {
		bCtx := *input.BuildContext
		if project.SourceType == models.SourceTypeLocalAgent {
			if s.pathValidator != nil {
				cleanRel, err := s.pathValidator.ValidateRelativeServicePath(bCtx)
				if err != nil {
					return nil, fmt.Errorf("%w: invalid build context path for local agent: %v", ErrValidationFailed, err)
				}
				bCtx = cleanRel
			}
		}
		project.BuildContext = bCtx
	}
	if input.BuildStrategy != nil {
		project.BuildStrategy = *input.BuildStrategy
	}
	if input.BuildCommand != nil {
		project.BuildCommand = *input.BuildCommand
	}
	if input.StartCommand != nil {
		project.StartCommand = *input.StartCommand
	}
	if input.RuntimeType != nil {
		project.RuntimeType = *input.RuntimeType
	}
	if input.InternalPort != nil {
		project.InternalPort = *input.InternalPort
	}
	if input.HealthCheckPath != nil {
		project.HealthCheckPath = input.HealthCheckPath
	}
	if input.HealthCheckEnabled != nil {
		project.HealthCheckEnabled = *input.HealthCheckEnabled
	}
	if input.HealthStrategy != nil {
		project.HealthStrategy = *input.HealthStrategy
	}

	project.UpdatedAt = time.Now()

	_, err = s.db.Exec(ctx,
		`UPDATE projects SET
		 name = $1, slug = $2, source_reference = $3, repository_path = $4, branch = $5,
		 dockerfile_path = $6, build_context = $7, build_strategy = $8, build_command = $9,
		 start_command = $10, runtime_type = $11, internal_port = $12, health_check_path = $13,
		 health_check_enabled = $14, health_strategy = $15, updated_at = $16
		 WHERE id = $17`,
		project.Name, project.Slug, project.SourceReference, project.RepositoryPath, project.Branch,
		project.DockerfilePath, project.BuildContext, project.BuildStrategy, project.BuildCommand,
		project.StartCommand, project.RuntimeType, project.InternalPort, project.HealthCheckPath,
		project.HealthCheckEnabled, project.HealthStrategy, project.UpdatedAt, project.ID,
	)
	if err != nil {
		if strings.Contains(err.Error(), "uq_projects_owner_slug") {
			return nil, ErrProjectSlugTaken
		}
		return nil, fmt.Errorf("failed to update project: %w", err)
	}

	s.populateProjectPreviewURL(project)
	slog.Info("project updated", "project_id", project.ID)
	return project, nil
}

// DeleteProject deletes a project and its associated resources.
// It is idempotent and error-aware: if cleanup fails, it does NOT permanently delete
// the database records, but leaves the project in a retryable failed state.
func (s *ProjectService) DeleteProject(ctx context.Context, projectID, ownerID uuid.UUID, cleanupFunc func(ctx context.Context, projectID uuid.UUID) error) error {
	project, err := s.GetProject(ctx, projectID, ownerID)
	if err != nil {
		return err
	}

	// 1. Mark project as deleting in DB so no deployments can be created or queued
	if s.db != nil {
		if _, err := s.db.Exec(ctx, "UPDATE projects SET status = $1, updated_at = NOW() WHERE id = $2", models.ProjectStatusDeleting, project.ID); err != nil {
			return fmt.Errorf("failed to mark project as deleting: %w", err)
		}
	}

	// 2. Drain / cancel active or queued deployments
	if s.deploymentService != nil {
		if err := s.deploymentService.CancelAllProjectDeployments(ctx, project.ID); err != nil {
			return fmt.Errorf("failed to cancel project deployments: %w", err)
		}
	} else if s.db != nil {
		now := time.Now()
		reason := "Project deletion in progress"
		if _, err := s.db.Exec(ctx,
			`UPDATE deployments SET
			 status = $1, failure_reason = $2, finished_at = $3
			 WHERE project_id = $4 AND status IN ($5, $6, $7, $8, $9)`,
			models.DeployStatusFailed, reason, now, project.ID,
			models.DeployStatusQueued, models.DeployStatusCloning, models.DeployStatusBuilding,
			models.DeployStatusStarting, models.DeployStatusHealthChecking,
		); err != nil {
			return fmt.Errorf("failed to fail active deployments: %w", err)
		}
		if _, err := s.db.Exec(ctx,
			`UPDATE service_deployments SET
			 status = $1, failure_reason = $2, finished_at = $3
			 WHERE service_id IN (SELECT id FROM services WHERE project_id = $4)
			   AND status IN ($5, $6, $7, $8, $9)`,
			models.DeployStatusFailed, reason, now, project.ID,
			models.DeployStatusQueued, models.DeployStatusCloning, models.DeployStatusBuilding,
			models.DeployStatusStarting, models.DeployStatusHealthChecking,
		); err != nil {
			return fmt.Errorf("failed to fail active service deployments: %w", err)
		}
	}

	// 3. Error-aware resource cleanup
	if cleanupFunc != nil {
		if err := cleanupFunc(ctx, project.ID); err != nil {
			slog.Error("resource cleanup failed during project deletion", "project_id", project.ID, "error", err)
			// Do not permanently delete project records when cleanup fails; provide a retryable cleanup path.
			if s.db != nil {
				if _, dbErr := s.db.Exec(ctx, "UPDATE projects SET status = $1, updated_at = NOW() WHERE id = $2", models.ProjectStatusFailed, project.ID); dbErr != nil {
					return fmt.Errorf("cleanup failed: %w; additionally failed to update project status: %v", err, dbErr)
				}
			}
			return fmt.Errorf("cleanup failed: %w (project preserved for retry)", err)
		}
	}

	// 4. Cleanup succeeded: permanently remove project records
	if s.db != nil {
		_, err = s.db.Exec(ctx, "DELETE FROM projects WHERE id = $1", project.ID)
		if err != nil {
			return fmt.Errorf("failed to delete project: %w", err)
		}
	}

	slog.Info("project deleted", "project_id", project.ID)
	return nil
}

// GetProjectByIDWithoutOwnership retrieves a project by ID without ownership check.
func (s *ProjectService) GetProjectByIDWithoutOwnership(ctx context.Context, projectID uuid.UUID) (*models.Project, error) {
	if s.db == nil {
		return nil, ErrProjectNotFound
	}

	p := &models.Project{}
	err := s.db.QueryRow(ctx,
		`SELECT id, owner_id, source_id, name, slug, source_type, source_reference, repository_path, branch,
		 dockerfile_path, build_context, build_strategy, build_command, start_command,
		 runtime_type, internal_port, health_check_path, health_check_enabled, health_strategy,
		 status, current_deployment_id, port, created_at, updated_at,
		 COALESCE(deployment_strategy, 'auto'), COALESCE(deployment_plan, '{}'::jsonb)
		 FROM projects WHERE id = $1`,
		projectID,
	).Scan(
		&p.ID, &p.OwnerID, &p.SourceID, &p.Name, &p.Slug, &p.SourceType, &p.SourceReference, &p.RepositoryPath,
		&p.Branch, &p.DockerfilePath, &p.BuildContext, &p.BuildStrategy, &p.BuildCommand,
		&p.StartCommand, &p.RuntimeType, &p.InternalPort, &p.HealthCheckPath,
		&p.HealthCheckEnabled, &p.HealthStrategy, &p.Status, &p.CurrentDeploymentID, &p.Port,
		&p.CreatedAt, &p.UpdatedAt, &p.DeploymentStrategy, &p.DeploymentPlan,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrProjectNotFound
		}
		return nil, fmt.Errorf("failed to get project: %w", err)
	}
	if s.db != nil {
		svcs, _ := s.getServiceService().ListServices(ctx, p.ID)
		if len(svcs) == 0 {
			svcs = []*models.Service{
				{
					ID:                 p.ID,
					ProjectID:          p.ID,
					SourceID:           p.SourceID,
					Name:               p.Name,
					Role:               models.RoleOther,
					Classification:     models.ClassificationApplication,
					SourcePath:         p.BuildContext,
					RuntimeType:        p.RuntimeType,
					BuildStrategy:      p.BuildStrategy,
					BuildCommand:       p.BuildCommand,
					StartCommand:       p.StartCommand,
					DockerfilePath:     p.DockerfilePath,
					BuildContext:       p.BuildContext,
					InternalPort:       p.InternalPort,
					HostPort:           p.Port,
					PublicExposed:      true,
					HealthStrategy:     p.HealthStrategy,
					HealthCheckPath:    p.HealthCheckPath,
					HealthCheckEnabled: p.HealthCheckEnabled,
					Status:             p.Status,
				},
			}
		}
		p.Services = svcs
		if len(svcs) > 0 {
			derivedStatus := CalculateProjectStatus(svcs)
			if p.Status != models.ProjectStatusDeploying && p.Status != models.ProjectStatusDeleting && p.Status != derivedStatus {
				p.Status = derivedStatus
				_ = s.UpdateProjectStatus(ctx, p.ID, derivedStatus)
			}
		}
	}
	s.populateProjectPreviewURL(p)
	return p, nil
}
