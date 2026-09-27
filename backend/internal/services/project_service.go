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
	"github.com/forgelab/backend/internal/security"
)

var (
	ErrProjectNotFound  = errors.New("project not found")
	ErrProjectSlugTaken = errors.New("project slug already in use")
	ErrProjectNotOwned  = errors.New("project does not belong to user")
	ErrActiveDeployment = errors.New("project has an active deployment in progress")
	ErrValidationFailed = errors.New("validation failed")
	ErrInvalidSource    = errors.New("invalid or unavailable project source")
)

// ProjectService handles project-related business logic.
type ProjectService struct {
	db            *pgxpool.Pool
	pathValidator *security.PathValidator
	sourceService *SourceService
	githubService *GitHubService
}

// NewProjectService creates a new ProjectService.
func NewProjectService(
	db *pgxpool.Pool,
	validator *security.PathValidator,
	sourceService *SourceService,
	githubService *GitHubService,
) *ProjectService {
	return &ProjectService{
		db:            db,
		pathValidator: validator,
		sourceService: sourceService,
		githubService: githubService,
	}
}

// CreateProjectInput holds the data needed to create a project.
type CreateProjectInput struct {
	Name            string  `json:"name"`
	SourceType      string  `json:"source_type"` // "local" or "github"
	SourceReference string  `json:"source_reference"` // repo "owner/repo" or local upload source_id
	RepositoryPath  string  `json:"repository_path"` // legacy/optional host path
	Branch          string  `json:"branch"`
	DockerfilePath  string  `json:"dockerfile_path"`
	BuildContext    string  `json:"build_context"`
	BuildStrategy   string  `json:"build_strategy"` // "auto" or "dockerfile"
	BuildCommand    string  `json:"build_command"`
	StartCommand    string  `json:"start_command"`
	RuntimeType     string  `json:"runtime_type"`
	InternalPort    int     `json:"internal_port"`
	HealthCheckPath *string `json:"health_check_path"`
	HealthStrategy  string  `json:"health_strategy"`
}

// UpdateProjectInput holds the data that can be updated on a project.
type UpdateProjectInput struct {
	Name               *string `json:"name"`
	SourceReference    *string `json:"source_reference"`
	RepositoryPath     *string `json:"repository_path"`
	Branch             *string `json:"branch"`
	DockerfilePath     *string `json:"dockerfile_path"`
	BuildContext       *string `json:"build_context"`
	BuildStrategy      *string `json:"build_strategy"`
	BuildCommand       *string `json:"build_command"`
	StartCommand       *string `json:"start_command"`
	RuntimeType        *string `json:"runtime_type"`
	InternalPort       *int    `json:"internal_port"`
	HealthCheckPath    *string `json:"health_check_path"`
	HealthCheckEnabled *bool   `json:"health_check_enabled"`
	HealthStrategy     *string `json:"health_strategy"`
}

// CreateProject creates a new project for the authenticated user.
func (s *ProjectService) CreateProject(ctx context.Context, ownerID uuid.UUID, input CreateProjectInput) (*models.Project, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, fmt.Errorf("%w: project name is required", ErrValidationFailed)
	}

	sourceType := strings.ToLower(strings.TrimSpace(input.SourceType))
	if sourceType == "" {
		sourceType = models.SourceTypeLocal
	}
	if sourceType != models.SourceTypeLocal && sourceType != models.SourceTypeGitHub {
		return nil, fmt.Errorf("%w: unsupported source type %q", ErrValidationFailed, sourceType)
	}

	sourceRef := strings.TrimSpace(input.SourceReference)
	repoPath := strings.TrimSpace(input.RepositoryPath)

	if sourceType == models.SourceTypeGitHub {
		if sourceRef == "" {
			return nil, fmt.Errorf("%w: github repository (owner/name) is required", ErrValidationFailed)
		}
		if s.githubService != nil {
			status, err := s.githubService.GetStatus(ctx, ownerID)
			if err != nil || !status.Connected {
				return nil, ErrGitHubNotConnected
			}
		}
	} else if sourceType == models.SourceTypeLocal {
		if sourceRef != "" {
			sourceUUID, err := uuid.Parse(sourceRef)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid source upload ID", ErrInvalidSource)
			}
			if s.sourceService != nil {
				if _, err := s.sourceService.GetSourcePath(ctx, ownerID, sourceUUID); err != nil {
					return nil, fmt.Errorf("%w: source files not found, expired, or access denied", ErrInvalidSource)
				}
			}
		} else if repoPath != "" {
			if s.pathValidator != nil {
				canonicalPath, err := s.pathValidator.ValidateSourcePath(repoPath)
				if err != nil {
					return nil, fmt.Errorf("%w: %v", ErrInvalidSource, err)
				}
				repoPath = canonicalPath
			}
		} else {
			return nil, fmt.Errorf("%w: local source requires uploaded files or valid host repository path", ErrInvalidSource)
		}
	}

	slug := generateSlug(name)

	branch := strings.TrimSpace(input.Branch)
	if branch == "" {
		branch = "main"
	}
	buildStrategy := strings.ToLower(strings.TrimSpace(input.BuildStrategy))
	if buildStrategy == "" {
		buildStrategy = models.BuildStrategyAuto
	}
	dockerfilePath := strings.TrimSpace(input.DockerfilePath)
	if dockerfilePath == "" {
		dockerfilePath = "Dockerfile"
	}
	buildContext := strings.TrimSpace(input.BuildContext)
	if buildContext == "" {
		buildContext = "."
	}
	internalPort := input.InternalPort
	if internalPort <= 0 || internalPort > 65535 {
		internalPort = 8080
	}
	healthStrategy := strings.ToLower(strings.TrimSpace(input.HealthStrategy))
	if healthStrategy == "" {
		healthStrategy = models.HealthStrategyAuto
	}
	runtimeType := strings.TrimSpace(input.RuntimeType)
	if runtimeType == "" {
		runtimeType = "generic"
	}

	healthCheckPath := "/health"
	if input.HealthCheckPath != nil {
		healthCheckPath = *input.HealthCheckPath
	}

	now := time.Now()
	project := &models.Project{
		ID:                 uuid.New(),
		OwnerID:            ownerID,
		Name:               name,
		Slug:               slug,
		SourceType:         sourceType,
		SourceReference:    sourceRef,
		RepositoryPath:     repoPath,
		Branch:             branch,
		DockerfilePath:     dockerfilePath,
		BuildContext:       buildContext,
		BuildStrategy:      buildStrategy,
		BuildCommand:       strings.TrimSpace(input.BuildCommand),
		StartCommand:       strings.TrimSpace(input.StartCommand),
		RuntimeType:        runtimeType,
		InternalPort:       internalPort,
		HealthCheckPath:    &healthCheckPath,
		HealthCheckEnabled: true,
		HealthStrategy:     healthStrategy,
		Status:             models.ProjectStatusInactive,
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	_, err := s.db.Exec(ctx,
		`INSERT INTO projects (
			id, owner_id, name, slug, source_type, source_reference, repository_path, branch,
			dockerfile_path, build_context, build_strategy, build_command, start_command,
			runtime_type, internal_port, health_check_path, health_check_enabled, health_strategy,
			status, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13,
			$14, $15, $16, $17, $18,
			$19, $20, $21
		)`,
		project.ID, project.OwnerID, project.Name, project.Slug, project.SourceType, project.SourceReference,
		project.RepositoryPath, project.Branch, project.DockerfilePath, project.BuildContext,
		project.BuildStrategy, project.BuildCommand, project.StartCommand,
		project.RuntimeType, project.InternalPort, project.HealthCheckPath, project.HealthCheckEnabled,
		project.HealthStrategy, project.Status, project.CreatedAt, project.UpdatedAt,
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
	project, err := s.GetProjectByIDWithoutOwnership(ctx, projectID)
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
		`SELECT id, owner_id, name, slug, source_type, source_reference, repository_path, branch,
		 dockerfile_path, build_context, build_strategy, build_command, start_command,
		 runtime_type, internal_port, health_check_path, health_check_enabled, health_strategy,
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
			&p.ID, &p.OwnerID, &p.Name, &p.Slug, &p.SourceType, &p.SourceReference, &p.RepositoryPath,
			&p.Branch, &p.DockerfilePath, &p.BuildContext, &p.BuildStrategy, &p.BuildCommand,
			&p.StartCommand, &p.RuntimeType, &p.InternalPort, &p.HealthCheckPath,
			&p.HealthCheckEnabled, &p.HealthStrategy, &p.Status, &p.CurrentDeploymentID, &p.Port,
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

	slog.Info("project updated", "project_id", project.ID)
	return project, nil
}

// DeleteProject deletes a project and its associated resources.
func (s *ProjectService) DeleteProject(ctx context.Context, projectID, ownerID uuid.UUID, cleanupFunc func(ctx context.Context, projectID uuid.UUID)) error {
	project, err := s.GetProject(ctx, projectID, ownerID)
	if err != nil {
		return err
	}

	if cleanupFunc != nil {
		cleanupFunc(ctx, project.ID)
	}

	_, err = s.db.Exec(ctx, "DELETE FROM projects WHERE id = $1", project.ID)
	if err != nil {
		return fmt.Errorf("failed to delete project: %w", err)
	}

	slog.Info("project deleted", "project_id", project.ID)
	return nil
}

// GetProjectByIDWithoutOwnership retrieves a project by ID without ownership check.
func (s *ProjectService) GetProjectByIDWithoutOwnership(ctx context.Context, projectID uuid.UUID) (*models.Project, error) {
	p := &models.Project{}
	err := s.db.QueryRow(ctx,
		`SELECT id, owner_id, name, slug, source_type, source_reference, repository_path, branch,
		 dockerfile_path, build_context, build_strategy, build_command, start_command,
		 runtime_type, internal_port, health_check_path, health_check_enabled, health_strategy,
		 status, current_deployment_id, port, created_at, updated_at
		 FROM projects WHERE id = $1`,
		projectID,
	).Scan(
		&p.ID, &p.OwnerID, &p.Name, &p.Slug, &p.SourceType, &p.SourceReference, &p.RepositoryPath,
		&p.Branch, &p.DockerfilePath, &p.BuildContext, &p.BuildStrategy, &p.BuildCommand,
		&p.StartCommand, &p.RuntimeType, &p.InternalPort, &p.HealthCheckPath,
		&p.HealthCheckEnabled, &p.HealthStrategy, &p.Status, &p.CurrentDeploymentID, &p.Port,
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

// UpdateProjectPort updates the assigned host port for a project.
func (s *ProjectService) UpdateProjectPort(ctx context.Context, projectID uuid.UUID, port int) (*models.Project, error) {
	_, err := s.db.Exec(ctx,
		"UPDATE projects SET port = $1, updated_at = $2 WHERE id = $3",
		port, time.Now(), projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update project port: %w", err)
	}
	return s.GetProjectByIDWithoutOwnership(ctx, projectID)
}

func generateSlug(name string) string {
	slug := strings.ToLower(name)
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = strings.ReplaceAll(slug, "_", "-")
	reg := regexp.MustCompile(`[^a-z0-9-]`)
	slug = reg.ReplaceAllString(slug, "")
	reg = regexp.MustCompile(`-+`)
	slug = reg.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "project"
	}
	return slug
}
