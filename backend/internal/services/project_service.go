package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/models"
)

var (
	ErrProjectNotFound    = errors.New("project not found")
	ErrProjectSlugTaken   = errors.New("project slug already in use")
	ErrProjectNotOwned    = errors.New("project does not belong to user")
	ErrActiveDeployment   = errors.New("project has an active deployment in progress")
)

// slugRegexp matches valid slugs: lowercase letters, numbers, hyphens.
var slugRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

// ProjectService handles project-related business logic.
type ProjectService struct {
	db *pgxpool.Pool
}

// NewProjectService creates a new ProjectService.
func NewProjectService(db *pgxpool.Pool) *ProjectService {
	return &ProjectService{db: db}
}

// CreateProjectInput holds the data needed to create a project.
type CreateProjectInput struct {
	Name           string  `json:"name"`
	RepositoryPath string  `json:"repository_path"`
	Branch         string  `json:"branch"`
	DockerfilePath string  `json:"dockerfile_path"`
	BuildContext   string  `json:"build_context"`
	HealthCheckPath *string `json:"health_check_path"`
}

// UpdateProjectInput holds the data that can be updated on a project.
type UpdateProjectInput struct {
	Name               *string `json:"name"`
	RepositoryPath     *string `json:"repository_path"`
	Branch             *string `json:"branch"`
	DockerfilePath     *string `json:"dockerfile_path"`
	BuildContext       *string `json:"build_context"`
	HealthCheckPath    *string `json:"health_check_path"`
	HealthCheckEnabled *bool   `json:"health_check_enabled"`
}

// CreateProject creates a new project for the authenticated user.
func (s *ProjectService) CreateProject(ctx context.Context, ownerID uuid.UUID, input CreateProjectInput) (*models.Project, error) {
	// Generate slug from name
	slug := generateSlug(input.Name)

	// Set defaults
	branch := input.Branch
	if branch == "" {
		branch = "main"
	}
	dockerfilePath := input.DockerfilePath
	if dockerfilePath == "" {
		dockerfilePath = "Dockerfile"
	}
	buildContext := input.BuildContext
	if buildContext == "" {
		buildContext = "."
	}
	healthCheckPath := "/health"
	if input.HealthCheckPath != nil {
		healthCheckPath = *input.HealthCheckPath
	}

	project := &models.Project{
		ID:                 uuid.New(),
		OwnerID:            ownerID,
		Name:               input.Name,
		Slug:               slug,
		SourceType:         "local",
		RepositoryPath:     input.RepositoryPath,
		Branch:             branch,
		DockerfilePath:     dockerfilePath,
		BuildContext:        buildContext,
		HealthCheckPath:    &healthCheckPath,
		HealthCheckEnabled: true,
		Status:             models.ProjectStatusInactive,
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}

	_, err := s.db.Exec(ctx,
		`INSERT INTO projects (id, owner_id, name, slug, source_type, repository_path, branch,
		 dockerfile_path, build_context, health_check_path, health_check_enabled, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		project.ID, project.OwnerID, project.Name, project.Slug, project.SourceType,
		project.RepositoryPath, project.Branch, project.DockerfilePath, project.BuildContext,
		project.HealthCheckPath, project.HealthCheckEnabled, project.Status,
		project.CreatedAt, project.UpdatedAt,
	)
	if err != nil {
		if strings.Contains(err.Error(), "uq_projects_owner_slug") {
			return nil, ErrProjectSlugTaken
		}
		return nil, fmt.Errorf("failed to create project: %w", err)
	}

	slog.Info("project created", "project_id", project.ID, "name", project.Name, "owner_id", ownerID)
	return project, nil
}

// GetProject retrieves a project by ID, verifying ownership.
func (s *ProjectService) GetProject(ctx context.Context, projectID, ownerID uuid.UUID) (*models.Project, error) {
	project, err := s.getProjectByID(ctx, projectID)
	if err != nil {
		return nil, err
	}

	if project.OwnerID != ownerID {
		return nil, ErrProjectNotOwned
	}

	return project, nil
}

// ListProjects retrieves all projects for a user.
func (s *ProjectService) ListProjects(ctx context.Context, ownerID uuid.UUID) ([]*models.Project, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, owner_id, name, slug, source_type, repository_path, branch,
		 dockerfile_path, build_context, health_check_path, health_check_enabled,
		 status, current_deployment_id, port, created_at, updated_at
		 FROM projects WHERE owner_id = $1 ORDER BY created_at DESC`,
		ownerID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}
	defer rows.Close()

	var projects []*models.Project
	for rows.Next() {
		p := &models.Project{}
		err := rows.Scan(
			&p.ID, &p.OwnerID, &p.Name, &p.Slug, &p.SourceType, &p.RepositoryPath,
			&p.Branch, &p.DockerfilePath, &p.BuildContext, &p.HealthCheckPath,
			&p.HealthCheckEnabled, &p.Status, &p.CurrentDeploymentID, &p.Port,
			&p.CreatedAt, &p.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan project: %w", err)
		}
		projects = append(projects, p)
	}

	if projects == nil {
		projects = []*models.Project{}
	}

	return projects, nil
}

// UpdateProject updates project settings.
func (s *ProjectService) UpdateProject(ctx context.Context, projectID, ownerID uuid.UUID, input UpdateProjectInput) (*models.Project, error) {
	project, err := s.GetProject(ctx, projectID, ownerID)
	if err != nil {
		return nil, err
	}

	// Apply updates
	if input.Name != nil {
		project.Name = *input.Name
		project.Slug = generateSlug(*input.Name)
	}
	if input.RepositoryPath != nil {
		project.RepositoryPath = *input.RepositoryPath
	}
	if input.Branch != nil {
		project.Branch = *input.Branch
	}
	if input.DockerfilePath != nil {
		project.DockerfilePath = *input.DockerfilePath
	}
	if input.BuildContext != nil {
		project.BuildContext = *input.BuildContext
	}
	if input.HealthCheckPath != nil {
		project.HealthCheckPath = input.HealthCheckPath
	}
	if input.HealthCheckEnabled != nil {
		project.HealthCheckEnabled = *input.HealthCheckEnabled
	}

	project.UpdatedAt = time.Now()

	_, err = s.db.Exec(ctx,
		`UPDATE projects SET name = $1, slug = $2, repository_path = $3, branch = $4,
		 dockerfile_path = $5, build_context = $6, health_check_path = $7,
		 health_check_enabled = $8, updated_at = $9
		 WHERE id = $10`,
		project.Name, project.Slug, project.RepositoryPath, project.Branch,
		project.DockerfilePath, project.BuildContext, project.HealthCheckPath,
		project.HealthCheckEnabled, project.UpdatedAt, project.ID,
	)
	if err != nil {
		if strings.Contains(err.Error(), "uq_projects_owner_slug") {
			return nil, ErrProjectSlugTaken
		}
		return nil, fmt.Errorf("failed to update project: %w", err)
	}

	slog.Info("project updated", "project_id", project.ID)
	return project, nil
}

// DeleteProject deletes a project and its associated resources.
func (s *ProjectService) DeleteProject(ctx context.Context, projectID, ownerID uuid.UUID) error {
	project, err := s.GetProject(ctx, projectID, ownerID)
	if err != nil {
		return err
	}

	// TODO: Stop running containers before deleting

	_, err = s.db.Exec(ctx, "DELETE FROM projects WHERE id = $1", project.ID)
	if err != nil {
		return fmt.Errorf("failed to delete project: %w", err)
	}

	slog.Info("project deleted", "project_id", project.ID)
	return nil
}

// getProjectByID retrieves a project by ID without ownership check.
func (s *ProjectService) getProjectByID(ctx context.Context, projectID uuid.UUID) (*models.Project, error) {
	p := &models.Project{}
	err := s.db.QueryRow(ctx,
		`SELECT id, owner_id, name, slug, source_type, repository_path, branch,
		 dockerfile_path, build_context, health_check_path, health_check_enabled,
		 status, current_deployment_id, port, created_at, updated_at
		 FROM projects WHERE id = $1`,
		projectID,
	).Scan(
		&p.ID, &p.OwnerID, &p.Name, &p.Slug, &p.SourceType, &p.RepositoryPath,
		&p.Branch, &p.DockerfilePath, &p.BuildContext, &p.HealthCheckPath,
		&p.HealthCheckEnabled, &p.Status, &p.CurrentDeploymentID, &p.Port,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrProjectNotFound
		}
		return nil, fmt.Errorf("failed to get project: %w", err)
	}
	return p, nil
}

// generateSlug creates a URL-friendly slug from a project name.
func generateSlug(name string) string {
	slug := strings.ToLower(name)
	// Replace spaces and underscores with hyphens
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = strings.ReplaceAll(slug, "_", "-")
	// Remove any non-alphanumeric, non-hyphen characters
	reg := regexp.MustCompile(`[^a-z0-9-]`)
	slug = reg.ReplaceAllString(slug, "")
	// Collapse multiple hyphens
	reg = regexp.MustCompile(`-+`)
	slug = reg.ReplaceAllString(slug, "-")
	// Trim leading/trailing hyphens
	slug = strings.Trim(slug, "-")

	if slug == "" {
		slug = "project"
	}
	return slug
}
