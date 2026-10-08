package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"encoding/json"
	"net/http"
	"path/filepath"

	"github.com/forgelab/backend/internal/agent"
	"github.com/forgelab/backend/internal/envparser"
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
	db             *pgxpool.Pool
	pathValidator  *security.PathValidator
	sourceService  *SourceService
	githubService  *GitHubService
	serviceService *ServiceService
	secretService  *SecretService
}

// NewProjectService creates a new ProjectService.
func NewProjectService(
	db *pgxpool.Pool,
	validator *security.PathValidator,
	sourceService *SourceService,
	githubService *GitHubService,
) *ProjectService {
	return &ProjectService{
		db:             db,
		pathValidator:  validator,
		sourceService:  sourceService,
		githubService:  githubService,
		serviceService: NewServiceService(db),
	}
}

func (s *ProjectService) getServiceService() *ServiceService {
	if s.serviceService != nil {
		return s.serviceService
	}
	return NewServiceService(s.db)
}

func (s *ProjectService) SetServiceService(ss *ServiceService) {
	s.serviceService = ss
}

func (s *ProjectService) SetSecretService(sec *SecretService) {
	s.secretService = sec
}

type CreateServiceInput struct {
	Name              string                     `json:"name"`
	Role              string                     `json:"role"`
	Classification    string                     `json:"classification,omitempty"`
	SourcePath        string                     `json:"source_path"`
	Runtime           string                     `json:"runtime"`
	RuntimeType       string                     `json:"runtime_type"`
	Framework         string                     `json:"framework"`
	PackageManager    string                     `json:"package_manager"`
	BuildStrategy     string                     `json:"build_strategy"`
	Image             string                     `json:"image,omitempty"`
	BuildCandidates   []models.BuildCandidate    `json:"build_candidates"`
	BuildCommand      string                     `json:"build_command"`
	StartCommand      string                     `json:"start_command"`
	DockerfilePath    string                     `json:"dockerfile_path"`
	BuildContext      string                     `json:"build_context"`
	InternalPort      int                        `json:"internal_port"`
	HostPort          *int                       `json:"host_port"`
	PublicExposed     bool                       `json:"public_exposed"`
	HealthStrategy    string                     `json:"health_strategy"`
	HealthCheckPath   *string                    `json:"health_check_path"`
	HealthCheckConfig *models.HealthCheckConfig  `json:"healthcheck_config,omitempty"`
	DependsOn           []string                   `json:"depends_on,omitempty"`
	DependsOnConditions map[string]string          `json:"depends_on_conditions,omitempty"`
	Volumes             []models.VolumeMountConfig `json:"volumes,omitempty"`
	Networks            []string                   `json:"networks,omitempty"`
}

// CreateProjectInput holds the data needed to create a project.
type CreateProjectInput struct {
	Name               string               `json:"name"`
	SourceType         string               `json:"source_type"`      // "local", "local_directory", "local_upload", "local_agent", "github"
	SourceReference    string               `json:"source_reference"` // repo "owner/repo", upload source_id, or agent source_id
	AgentID            string               `json:"agent_id"`
	RepositoryPath     string               `json:"repository_path"` // legacy/optional host path
	Branch             string               `json:"branch"`
	DeploymentStrategy string               `json:"deployment_strategy"` // "compose", "dockerfile", "auto", "custom"
	DeploymentPlan     json.RawMessage      `json:"deployment_plan,omitempty"`
	DockerfilePath     string               `json:"dockerfile_path"`
	BuildContext       string               `json:"build_context"`
	BuildStrategy      string               `json:"build_strategy"` // "auto" or "dockerfile"
	BuildCommand       string               `json:"build_command"`
	StartCommand       string               `json:"start_command"`
	RuntimeType        string               `json:"runtime_type"`
	InternalPort       int                  `json:"internal_port"`
	HealthCheckPath    *string              `json:"health_check_path"`
	HealthStrategy     string               `json:"health_strategy"`
	Services           []CreateServiceInput `json:"services"`
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
		if strings.TrimSpace(input.RepositoryPath) != "" {
			sourceType = models.SourceTypeLocalDirectory
		} else if strings.TrimSpace(input.SourceReference) != "" {
			sourceType = models.SourceTypeLocalUpload
		} else {
			sourceType = models.SourceTypeLocalDirectory
		}
	}

	sourceRef := strings.TrimSpace(input.SourceReference)
	repoPath := strings.TrimSpace(input.RepositoryPath)

	switch sourceType {
	case models.SourceTypeGitHub:
		if sourceRef == "" {
			return nil, fmt.Errorf("%w: github repository (owner/name) is required", ErrValidationFailed)
		}
		if s.githubService != nil {
			status, err := s.githubService.GetStatus(ctx, ownerID)
			if err != nil || !status.Connected {
				return nil, ErrGitHubNotConnected
			}
			if status.NeedsReauth {
				return nil, ErrGitHubNeedsReauth
			}
		}
	case models.SourceTypeLocalAgent:
		if sourceRef == "" {
			return nil, fmt.Errorf("%w: source session ID is required for local agent import", ErrValidationFailed)
		}
		sourceUUID, err := uuid.Parse(sourceRef)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid agent source ID", ErrInvalidSource)
		}

		// Validate source ownership/session/agent association server-side rather than trusting frontend IDs
		var verifiedAgentID string
		var verifiedInDB bool

		if s.sourceService != nil {
			if existingSource, err := s.sourceService.GetSource(ctx, sourceUUID, ownerID); err == nil && existingSource != nil {
				verifiedInDB = true
				if existingSource.AgentID != "" {
					verifiedAgentID = existingSource.AgentID
				}
			}
		}

		if !verifiedInDB && s.db != nil {
			var dbOwner uuid.UUID
			var dbAgentID string
			err := s.db.QueryRow(ctx, "SELECT owner_id, agent_id FROM sources WHERE id = $1", sourceUUID).Scan(&dbOwner, &dbAgentID)
			if err == nil {
				if dbOwner != ownerID {
					return nil, fmt.Errorf("%w: agent source does not belong to user", ErrInvalidSource)
				}
				verifiedInDB = true
				verifiedAgentID = dbAgentID
			}
		}

		// Fallback check against in-memory session manager
		if sm := agent.GetGlobalSessionManager(); sm != nil {
			if sess, err := sm.FindSessionBySourceID(sourceUUID); err == nil && sess != nil {
				if sess.UserID != ownerID {
					return nil, fmt.Errorf("%w: agent source session does not belong to user", ErrInvalidSource)
				}
				if sess.Consumed {
					return nil, fmt.Errorf("%w: agent source session has already been consumed", ErrInvalidSource)
				}
				if verifiedAgentID == "" {
					verifiedAgentID = sess.AgentID
				}
			} else if !verifiedInDB {
				return nil, fmt.Errorf("%w: agent source not found or unauthorized", ErrInvalidSource)
			}
		} else if !verifiedInDB {
			return nil, fmt.Errorf("%w: agent source not found or unauthorized", ErrInvalidSource)
		}

		if verifiedAgentID != "" {
			if input.AgentID != "" && input.AgentID != verifiedAgentID {
				return nil, fmt.Errorf("%w: agent ID mismatch with authenticated source", ErrValidationFailed)
			}
			input.AgentID = verifiedAgentID
		}

		repoPath = ""
	case models.SourceTypeLocalDirectory:
		if repoPath == "" {
			return nil, fmt.Errorf("%w: repository path is required for local directory projects", ErrValidationFailed)
		}
		if s.pathValidator != nil {
			canonicalPath, err := s.pathValidator.ValidateSourcePath(repoPath)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalidSource, err)
			}
			repoPath = canonicalPath
		}
		sourceRef = "" // Clear source_reference for local directory
	case models.SourceTypeLocalUpload:
		if sourceRef == "" {
			return nil, fmt.Errorf("%w: source upload ID is required for uploaded archive projects", ErrValidationFailed)
		}
		sourceUUID, err := uuid.Parse(sourceRef)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid source upload ID", ErrInvalidSource)
		}
		if s.sourceService != nil {
			if _, err := s.sourceService.GetSourcePath(ctx, ownerID, sourceUUID); err != nil {
				return nil, fmt.Errorf("%w: source files not found, expired, or access denied", ErrInvalidSource)
			}
		}
		repoPath = "" // Clear repository_path for uploaded sources
	case models.SourceTypeLocal:
		// Legacy alias
		if repoPath != "" {
			sourceType = models.SourceTypeLocalDirectory
			if s.pathValidator != nil {
				canonicalPath, err := s.pathValidator.ValidateSourcePath(repoPath)
				if err != nil {
					return nil, fmt.Errorf("%w: %v", ErrInvalidSource, err)
				}
				repoPath = canonicalPath
			}
			sourceRef = ""
		} else if sourceRef != "" {
			sourceType = models.SourceTypeLocalUpload
			sourceUUID, err := uuid.Parse(sourceRef)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid source upload ID", ErrInvalidSource)
			}
			if s.sourceService != nil {
				if _, err := s.sourceService.GetSourcePath(ctx, ownerID, sourceUUID); err != nil {
					return nil, fmt.Errorf("%w: source files not found, expired, or access denied", ErrInvalidSource)
				}
			}
			repoPath = ""
		} else {
			return nil, fmt.Errorf("%w: local source requires uploaded files or valid host repository path", ErrInvalidSource)
		}
	default:
		return nil, fmt.Errorf("%w: unsupported source type %q", ErrValidationFailed, sourceType)
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
	if dockerfilePath == "" && buildStrategy == models.BuildStrategyDockerfile {
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

	// If services provided, sync primary project metadata from first service
	if len(input.Services) > 0 {
		firstSvc := input.Services[0]
		if strings.TrimSpace(firstSvc.RuntimeType) != "" {
			runtimeType = strings.TrimSpace(firstSvc.RuntimeType)
		} else if strings.TrimSpace(firstSvc.Runtime) != "" {
			runtimeType = strings.TrimSpace(firstSvc.Runtime)
		}
		if firstSvc.InternalPort > 0 {
			internalPort = firstSvc.InternalPort
		}
		if strings.TrimSpace(firstSvc.BuildStrategy) != "" {
			buildStrategy = strings.TrimSpace(firstSvc.BuildStrategy)
		}
	}

	healthCheckPath := "/health"
	if input.HealthCheckPath != nil {
		healthCheckPath = *input.HealthCheckPath
	}

	now := time.Now()

	// Register or link source in sources table
	var sourceIDPtr *uuid.UUID
	sourceUUID := uuid.New()
	if parsedUUID, err := uuid.Parse(sourceRef); err == nil {
		sourceUUID = parsedUUID
	}
	agentIDStr := strings.TrimSpace(input.AgentID)
	sourceIDPtr = &sourceUUID

	deploymentStrategy := strings.TrimSpace(input.DeploymentStrategy)
	if deploymentStrategy == "" {
		deploymentStrategy = "auto"
	}
	deploymentPlan := input.DeploymentPlan
	if len(deploymentPlan) == 0 {
		deploymentPlan = json.RawMessage("{}")
	}

	project := &models.Project{
		ID:                 uuid.New(),
		OwnerID:            ownerID,
		SourceID:           sourceIDPtr,
		Name:               name,
		Slug:               slug,
		SourceType:         sourceType,
		SourceReference:    sourceRef,
		RepositoryPath:     repoPath,
		Branch:             branch,
		DeploymentStrategy: deploymentStrategy,
		DeploymentPlan:     deploymentPlan,
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

	// Prepare service models
	var servicesToCreate []*models.Service
	if len(input.Services) > 0 {
		for _, svcIn := range input.Services {
			svcName := strings.TrimSpace(svcIn.Name)
			if svcName == "" {
				svcName = project.Name
			}
			svcRole := strings.TrimSpace(svcIn.Role)
			if svcRole == "" {
				svcRole = models.RoleOther
			}
			svcStrat := strings.TrimSpace(svcIn.BuildStrategy)
			if svcStrat == "" {
				svcStrat = models.BuildStrategyAuto
			}
			svcPort := svcIn.InternalPort
			if svcPort <= 0 {
				svcPort = project.InternalPort
			}
			svcSourcePath := strings.TrimSpace(svcIn.SourcePath)
			if svcSourcePath == "" {
				svcSourcePath = "."
			}
			if sourceType == models.SourceTypeLocalAgent {
				if s.pathValidator != nil {
					cleanRel, err := s.pathValidator.ValidateRelativeServicePath(svcSourcePath)
					if err != nil {
						return nil, fmt.Errorf("%w: invalid service source path %q for local agent: %v", ErrValidationFailed, svcSourcePath, err)
					}
					svcSourcePath = cleanRel
				}
			}
			svcHealthStrat := strings.TrimSpace(svcIn.HealthStrategy)
			if svcHealthStrat == "" {
				svcHealthStrat = models.HealthStrategyAuto
			}
			svcHealthPath := "/health"
			if svcIn.HealthCheckPath != nil && *svcIn.HealthCheckPath != "" {
				svcHealthPath = *svcIn.HealthCheckPath
			}

			rt := strings.TrimSpace(svcIn.RuntimeType)
			if rt == "" {
				rt = strings.TrimSpace(svcIn.Runtime)
			}
			if rt == "" {
				rt = "generic"
			}

			dfPath := strings.TrimSpace(svcIn.DockerfilePath)
			if dfPath == "" && svcStrat == models.BuildStrategyDockerfile {
				dfPath = "Dockerfile"
			}
			bCtx := strings.TrimSpace(svcIn.BuildContext)
			if bCtx == "" {
				bCtx = svcSourcePath
			}
			if sourceType == models.SourceTypeLocalAgent {
				if s.pathValidator != nil {
					cleanCtx, err := s.pathValidator.ValidateRelativeServicePath(bCtx)
					if err != nil {
						return nil, fmt.Errorf("%w: invalid service build context %q for local agent: %v", ErrValidationFailed, bCtx, err)
					}
					bCtx = cleanCtx
				}
			}

			svcClassification := strings.TrimSpace(svcIn.Classification)
			if svcClassification == "" {
				svcClassification = models.ClassificationApplication
			}

			svc := &models.Service{
				ID:                  uuid.New(),
				ProjectID:           project.ID,
				SourceID:            project.SourceID,
				Name:                svcName,
				Role:                svcRole,
				Classification:      svcClassification,
				Image:               strings.TrimSpace(svcIn.Image),
				DependsOn:           svcIn.DependsOn,
				DependsOnConditions: svcIn.DependsOnConditions,
				Volumes:             svcIn.Volumes,
				Networks:            svcIn.Networks,
				HealthCheckConfig:   svcIn.HealthCheckConfig,
				SourcePath:         svcSourcePath,
				RuntimeType:        rt,
				Framework:          svcIn.Framework,
				PackageManager:     svcIn.PackageManager,
				BuildStrategy:      svcStrat,
				BuildCandidates:    svcIn.BuildCandidates,
				BuildCommand:       strings.TrimSpace(svcIn.BuildCommand),
				StartCommand:       strings.TrimSpace(svcIn.StartCommand),
				DockerfilePath:     dfPath,
				BuildContext:       bCtx,
				InternalPort:       svcPort,
				HostPort:           svcIn.HostPort,
				PublicExposed:      svcIn.PublicExposed,
				HealthStrategy:     svcHealthStrat,
				HealthCheckPath:    &svcHealthPath,
				HealthCheckEnabled: true,
				Status:             models.ProjectStatusInactive,
			}
			servicesToCreate = append(servicesToCreate, svc)
		}
	} else {
		// Single service fallback ensuring every project is modeled with at least 1 service
		srcPath := project.BuildContext
		if srcPath == "" {
			srcPath = "."
		}
		if sourceType == models.SourceTypeLocalAgent {
			if s.pathValidator != nil {
				cleanRel, err := s.pathValidator.ValidateRelativeServicePath(srcPath)
				if err != nil {
					cleanRel = "."
				}
				srcPath = cleanRel
			}
		}
		svc := &models.Service{
			ID:                 uuid.New(),
			ProjectID:          project.ID,
			SourceID:           project.SourceID,
			Name:               project.Name,
			Role:               models.RoleOther,
			Classification:     models.ClassificationApplication,
			Networks:           []string{},
			DependsOn:          []string{},
			Volumes:            []models.VolumeMountConfig{},
			SourcePath:         srcPath,
			RuntimeType:        project.RuntimeType,
			BuildStrategy:      project.BuildStrategy,
			BuildCommand:       project.BuildCommand,
			StartCommand:       project.StartCommand,
			DockerfilePath:     project.DockerfilePath,
			BuildContext:       project.BuildContext,
			InternalPort:       project.InternalPort,
			PublicExposed:      true,
			HealthStrategy:     project.HealthStrategy,
			HealthCheckPath:    project.HealthCheckPath,
			HealthCheckEnabled: project.HealthCheckEnabled,
			Status:             project.Status,
		}
		servicesToCreate = append(servicesToCreate, svc)
	}

	// Transactional persistence for project, source, and services
	tx, err := s.db.Begin(ctx)
	if err != nil {
		slog.Error("failed to begin transaction during project creation",
			"project_name", name,
			"owner_id", ownerID,
			"error", err,
		)
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. Insert or update source record
	_, err = tx.Exec(ctx,
		`INSERT INTO sources (id, owner_id, source_type, source_reference, agent_id, metadata, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, '{}', NOW(), NOW())
		 ON CONFLICT (id) DO UPDATE SET
		    source_type = EXCLUDED.source_type,
		    source_reference = EXCLUDED.source_reference,
		    agent_id = EXCLUDED.agent_id,
		    updated_at = NOW()`,
		sourceUUID, ownerID, sourceType, sourceRef, agentIDStr,
	)
	if err != nil {
		slog.Error("source persistence failure during project creation",
			"failure_stage", "source_persistence",
			"project_name", name,
			"owner_id", ownerID,
			"source_id", sourceUUID,
			"source_type", sourceType,
			"error", err,
		)
		return nil, fmt.Errorf("source persistence failure: %w", err)
	}

	// 2. Insert project record
	_, err = tx.Exec(ctx,
		`INSERT INTO projects (
			id, owner_id, source_id, name, slug, source_type, source_reference, repository_path, branch,
			dockerfile_path, build_context, build_strategy, build_command, start_command,
			runtime_type, internal_port, health_check_path, health_check_enabled, health_strategy,
			status, deployment_strategy, deployment_plan, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11, $12, $13, $14,
			$15, $16, $17, $18, $19,
			$20, $21, $22, $23, $24
		)`,
		project.ID, project.OwnerID, project.SourceID, project.Name, project.Slug, project.SourceType, project.SourceReference,
		project.RepositoryPath, project.Branch, project.DockerfilePath, project.BuildContext,
		project.BuildStrategy, project.BuildCommand, project.StartCommand,
		project.RuntimeType, project.InternalPort, project.HealthCheckPath, project.HealthCheckEnabled,
		project.HealthStrategy, project.Status, project.DeploymentStrategy, project.DeploymentPlan,
		project.CreatedAt, project.UpdatedAt,
	)
	if err != nil {
		if strings.Contains(err.Error(), "uq_projects_owner_slug") {
			return nil, ErrProjectSlugTaken
		}
		slog.Error("project persistence failure during project creation",
			"failure_stage", "project_persistence",
			"project_name", name,
			"project_id", project.ID,
			"owner_id", ownerID,
			"slug", slug,
			"error", err,
		)
		return nil, fmt.Errorf("project persistence failure: %w", err)
	}

	// 3. Insert services transactionally
	svcService := s.getServiceService()
	for _, svc := range servicesToCreate {
		if err := svcService.CreateServiceTx(ctx, tx, svc); err != nil {
			slog.Error("service persistence failure during project creation",
				"failure_stage", "service_persistence",
				"project_name", name,
				"project_id", project.ID,
				"owner_id", ownerID,
				"service_name", svc.Name,
				"service_id", svc.ID,
				"error", err,
			)
			return nil, fmt.Errorf("service persistence failure for %q: %w", svc.Name, err)
		}
		project.Services = append(project.Services, svc)
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("transaction commit failure during project creation",
			"failure_stage", "transaction_commit",
			"project_name", name,
			"project_id", project.ID,
			"owner_id", ownerID,
			"error", err,
		)
		return nil, fmt.Errorf("transaction commit failure: %w", err)
	}

	// If local agent source, mark session consumed in SessionManager
	if sourceType == models.SourceTypeLocalAgent {
		if sm := agent.GetGlobalSessionManager(); sm != nil {
			if sess, err := sm.FindSessionBySourceID(sourceUUID); err == nil && sess != nil {
				_ = sm.MarkConsumed(sess.ID)
			}
		}
	}

	// Automatically import .env variables if present in the local repository
	if s.secretService != nil {
		if count, err := s.ImportProjectEnvironment(ctx, project, ownerID); err == nil && count > 0 {
			slog.Info("imported environment variables from local repository", "project_id", project.ID, "count", count)
		}
	}

	s.populateProjectPreviewURL(project)
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
	}
	s.populateProjectPreviewURL(p)
	return p, nil
}

// UpdateProjectStatus updates the overall status of a project.
func (s *ProjectService) UpdateProjectStatus(ctx context.Context, projectID uuid.UUID, status string) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.Exec(ctx, "UPDATE projects SET status = $1, updated_at = NOW() WHERE id = $2", status, projectID)
	return err
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

func (s *ProjectService) resolveAuthoritativeHost() string {
	if host := os.Getenv("FORGELAB_PUBLIC_HOST"); host != "" {
		return host
	}
	if host := os.Getenv("PUBLIC_HOST"); host != "" {
		return host
	}
	if frontendURL := os.Getenv("FRONTEND_URL"); frontendURL != "" {
		if u, err := url.Parse(frontendURL); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	return "localhost"
}

func (s *ProjectService) populateProjectPreviewURL(p *models.Project) {
	if p == nil {
		return
	}
	publicHost := s.resolveAuthoritativeHost()

	var primarySvcURL *string
	var frontendSvcURL *string

	for _, svc := range p.Services {
		if svc.HostPort != nil && *svc.HostPort > 0 && svc.PublicExposed {
			urlStr := fmt.Sprintf("http://%s:%d", publicHost, *svc.HostPort)
			svc.PreviewURL = &urlStr

			if svc.Role == models.RoleFrontend && frontendSvcURL == nil {
				frontendSvcURL = &urlStr
			} else if primarySvcURL == nil {
				primarySvcURL = &urlStr
			}
		}
	}

	if frontendSvcURL != nil {
		p.PreviewURL = frontendSvcURL
	} else if primarySvcURL != nil {
		p.PreviewURL = primarySvcURL
	} else if p.Port != nil && *p.Port > 0 {
		urlStr := fmt.Sprintf("http://%s:%d", publicHost, *p.Port)
		p.PreviewURL = &urlStr
	}
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

func resolveAgentBaseURL() string {
	return agent.ResolveBaseURL()
}

// ImportProjectEnvironment imports .env files from a local source repository into ForgeLAB's encrypted environment variable store.
// Handles both root repository .env and service-specific .env in each service's source_path.
// Service-specific variables override root variables for that service.
func (s *ProjectService) ImportProjectEnvironment(ctx context.Context, project *models.Project, ownerID uuid.UUID) (int, error) {
	if s.secretService == nil {
		return 0, nil
	}

	var entries []ImportEnvVarEntry

	if project.SourceType == models.SourceTypeLocalAgent && project.SourceReference != "" {
		sourceUUID, err := uuid.Parse(project.SourceReference)
		if err != nil {
			return 0, fmt.Errorf("invalid source UUID: %w", err)
		}

		var agentToken string
		if s.sourceService != nil {
			agentToken, _ = s.sourceService.GetDecryptedAgentToken(ctx, ownerID, sourceUUID)
		}

		baseURL := resolveAgentBaseURL()
		var serviceQueryParts []string
		for _, svc := range project.Services {
			if svc.SourcePath != "" && svc.SourcePath != "." {
				serviceQueryParts = append(serviceQueryParts, "service_path="+url.QueryEscape(svc.SourcePath))
			}
		}

		agentURL := fmt.Sprintf("%s/api/agent/sources/%s/environment", baseURL, project.SourceReference)
		if len(serviceQueryParts) > 0 {
			agentURL += "?" + strings.Join(serviceQueryParts, "&")
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, agentURL, nil)
		if err != nil {
			slog.Warn("failed to create agent environment request", "error", err)
			return 0, nil
		}
		if agentToken != "" {
			req.Header.Set("Authorization", "Bearer "+agentToken)
			req.Header.Set("X-Agent-Session-Token", agentToken)
		}

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			slog.Warn("agent environment request failed", "error", err)
			return 0, nil
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			slog.Warn("agent environment request returned non-OK", "status", resp.StatusCode)
			return 0, nil
		}

		var envResp struct {
			Root     map[string]string            `json:"root"`
			Services map[string]map[string]string `json:"services"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envResp); err != nil {
			slog.Warn("failed to decode agent environment response", "error", err)
			return 0, nil
		}

		// Root .env mapped to project-level
		for k, v := range envResp.Root {
			entries = append(entries, ImportEnvVarEntry{
				ServiceID: nil,
				Key:       k,
				Value:     v,
				Scope:     models.EnvScopeRuntime,
			})
		}

		// Service-specific .env mapped to service
		for _, svc := range project.Services {
			if svcEnv, ok := envResp.Services[svc.SourcePath]; ok {
				svcID := svc.ID
				for k, v := range svcEnv {
					entries = append(entries, ImportEnvVarEntry{
						ServiceID: &svcID,
						Key:       k,
						Value:     v,
						Scope:     models.EnvScopeRuntime,
					})
				}
			}
		}
	} else if (project.SourceType == models.SourceTypeLocalDirectory || project.SourceType == models.SourceTypeLocal) && project.RepositoryPath != "" {
		// Root .env
		rootEnvPath := filepath.Join(project.RepositoryPath, ".env")
		if rootVars, err := envparser.ParseFile(rootEnvPath); err == nil {
			for k, v := range rootVars {
				entries = append(entries, ImportEnvVarEntry{
					ServiceID: nil,
					Key:       k,
					Value:     v,
					Scope:     models.EnvScopeRuntime,
				})
			}
		}

		// Service-specific .env
		for _, svc := range project.Services {
			if svc.SourcePath != "" && svc.SourcePath != "." {
				cleanRel, err := security.ValidateRelativeServicePath(svc.SourcePath)
				if err != nil {
					continue
				}
				svcEnvPath := filepath.Join(project.RepositoryPath, filepath.FromSlash(cleanRel), ".env")
				if svcVars, err := envparser.ParseFile(svcEnvPath); err == nil && len(svcVars) > 0 {
					svcID := svc.ID
					for k, v := range svcVars {
						entries = append(entries, ImportEnvVarEntry{
							ServiceID: &svcID,
							Key:       k,
							Value:     v,
							Scope:     models.EnvScopeRuntime,
						})
					}
				}
			}
		}
	}

	if len(entries) == 0 {
		return 0, nil
	}

	return s.secretService.ImportEnvVarsIfMissing(ctx, project.ID, ownerID, entries)
}
