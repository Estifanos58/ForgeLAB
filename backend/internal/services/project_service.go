package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/agent"
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
	db                         *pgxpool.Pool
	pathValidator              *security.PathValidator
	sourceService              *SourceService
	githubService              *GitHubService
	serviceService             *ServiceService
	secretService              *SecretService
	deploymentService          *DeploymentService
	importService              *RepositoryImportService
	projectGetterForTest       func(ctx context.Context, projectID, ownerID uuid.UUID) (*models.Project, error)
	projectsListerForTest      func(ctx context.Context, ownerID uuid.UUID, limit, offset int) ([]*models.Project, error)
	projectByIDForTest         func(ctx context.Context, projectID uuid.UUID) (*models.Project, error)
	updateProjectStatusForTest func(ctx context.Context, projectID uuid.UUID, status string) error
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

func (s *ProjectService) SetDeploymentService(ds *DeploymentService) {
	s.deploymentService = ds
}

func (s *ProjectService) SetImportService(is *RepositoryImportService) {
	s.importService = is
}

// SetProjectGetterForTest sets a test hook for GetProject.
func (s *ProjectService) SetProjectGetterForTest(fn func(ctx context.Context, projectID, ownerID uuid.UUID) (*models.Project, error)) {
	s.projectGetterForTest = fn
}

// SetProjectsListerForTest sets a test hook for ListProjects.
func (s *ProjectService) SetProjectsListerForTest(fn func(ctx context.Context, ownerID uuid.UUID, limit, offset int) ([]*models.Project, error)) {
	s.projectsListerForTest = fn
}

// SetProjectByIDForTest sets a test hook for GetProjectByIDWithoutOwnership.
func (s *ProjectService) SetProjectByIDForTest(fn func(ctx context.Context, projectID uuid.UUID) (*models.Project, error)) {
	s.projectByIDForTest = fn
}

// SetUpdateProjectStatusForTest sets a test hook for UpdateProjectStatus.
func (s *ProjectService) SetUpdateProjectStatusForTest(fn func(ctx context.Context, projectID uuid.UUID, status string) error) {
	s.updateProjectStatusForTest = fn
}

type CreateServiceInput struct {
	Name                string                     `json:"name"`
	Role                string                     `json:"role"`
	Classification      string                     `json:"classification,omitempty"`
	SourcePath          string                     `json:"source_path"`
	Runtime             string                     `json:"runtime"`
	RuntimeType         string                     `json:"runtime_type"`
	Framework           string                     `json:"framework"`
	PackageManager      string                     `json:"package_manager"`
	BuildStrategy       string                     `json:"build_strategy"`
	Image               string                     `json:"image,omitempty"`
	BuildCandidates     []models.BuildCandidate    `json:"build_candidates"`
	BuildCommand        string                     `json:"build_command"`
	StartCommand        string                     `json:"start_command"`
	DockerfilePath      string                     `json:"dockerfile_path"`
	BuildContext        string                     `json:"build_context"`
	InternalPort        int                        `json:"internal_port"`
	HostPort            *int                       `json:"host_port"`
	PublicExposed       *bool                      `json:"public_exposed"`
	HealthStrategy      string                     `json:"health_strategy"`
	HealthCheckPath     *string                    `json:"health_check_path"`
	HealthCheckConfig   *models.HealthCheckConfig  `json:"healthcheck_config,omitempty"`
	DependsOn           []string                   `json:"depends_on,omitempty"`
	DependsOnConditions map[string]string          `json:"depends_on_conditions,omitempty"`
	Volumes             []models.VolumeMountConfig `json:"volumes,omitempty"`
	Networks            []string                   `json:"networks,omitempty"`
	User                string                     `json:"user,omitempty"`
	DeploymentMode      string                     `json:"deployment_mode,omitempty"`
}

// CreateProjectInput holds the data needed to create a project.
type CreateProjectInput struct {
	Name               string               `json:"name"`
	SourceType         string               `json:"source_type"`      // "local", "local_directory", "local_upload", "local_agent", "github", "github_public"
	SourceReference    string               `json:"source_reference"` // repo "owner/repo", upload source_id, or agent source_id
	AgentID            string               `json:"agent_id"`
	RepositoryPath     string               `json:"repository_path"` // legacy/optional host path
	Branch             string               `json:"branch"`
	DeploymentStrategy string               `json:"deployment_strategy"` // "compose", "dockerfile", "auto", "custom"
	DeploymentMode     string               `json:"deployment_mode,omitempty"`
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
	ImportID           *uuid.UUID           `json:"import_id,omitempty"`
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
	DeploymentMode     *string `json:"deployment_mode"`
}

// CreateProject creates a new project for the authenticated user.
func (s *ProjectService) CreateProject(ctx context.Context, ownerID uuid.UUID, input CreateProjectInput) (*models.Project, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, fmt.Errorf("%w: project name is required", ErrValidationFailed)
	}

	if models.IsGuest(ownerID) {
		if s.db != nil {
			var guestCount int
			err := s.db.QueryRow(ctx, "SELECT COUNT(*) FROM projects WHERE owner_id = $1", ownerID).Scan(&guestCount)
			if err != nil {
				return nil, fmt.Errorf("failed to verify guest project quota: %w", err)
			}
			if guestCount >= 20 {
				return nil, fmt.Errorf("%w: guest deployment limit reached (maximum 20 projects)", ErrValidationFailed)
			}
		}
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

	var importedRecord *models.RepositoryImport
	if input.ImportID != nil {
		if s.importService != nil {
			imp, err := s.importService.GetImport(ctx, ownerID, *input.ImportID)
			if err != nil {
				return nil, fmt.Errorf("%w: import not found or unauthorized: %v", ErrInvalidSource, err)
			}
			if imp.Status != models.ImportStatusReady {
				return nil, fmt.Errorf("%w: repository import is not ready (status: %s)", ErrValidationFailed, imp.Status)
			}
			importedRecord = imp
		} else if s.db != nil {
			imp := &models.RepositoryImport{}
			err := s.db.QueryRow(ctx,
				`SELECT id, owner_id, repository_full_name, repository_id, is_private, branch, commit_sha, root_dir, source_type, status, snapshot_path, deployment_plan 
				 FROM repository_imports WHERE id = $1 AND owner_id = $2`,
				*input.ImportID, ownerID,
			).Scan(&imp.ID, &imp.OwnerID, &imp.RepositoryFullName, &imp.RepositoryID, &imp.IsPrivate, &imp.Branch, &imp.CommitSHA, &imp.RootDir, &imp.SourceType, &imp.Status, &imp.SnapshotPath, &imp.DeploymentPlan)
			if err != nil {
				return nil, fmt.Errorf("%w: import not found or unauthorized: %v", ErrInvalidSource, err)
			}
			if imp.Status != models.ImportStatusReady {
				return nil, fmt.Errorf("%w: repository import is not ready (status: %s)", ErrValidationFailed, imp.Status)
			}
			importedRecord = imp
		}
	}

	if importedRecord != nil {
		sourceRef = importedRecord.RepositoryFullName
		if input.Branch == "" {
			input.Branch = importedRecord.Branch
		}
		if importedRecord.IsPrivate {
			sourceType = models.SourceTypeGitHub
		} else {
			sourceType = models.SourceTypeGitHubPublic
		}
		if repoPath == "" && importedRecord.SnapshotPath != "" {
			repoPath = importedRecord.SnapshotPath
		}
		if len(input.DeploymentPlan) == 0 || string(input.DeploymentPlan) == "{}" {
			if len(importedRecord.DeploymentPlan) > 0 && string(importedRecord.DeploymentPlan) != "{}" {
				input.DeploymentPlan = importedRecord.DeploymentPlan
			}
		}
	}

	switch sourceType {
	case models.SourceTypeGitHub, models.SourceTypeGitHubPublic:
		if sourceRef == "" {
			return nil, fmt.Errorf("%w: github repository (owner/name) is required", ErrValidationFailed)
		}
		if importedRecord != nil {
			// Access was already verified and repository snapshot downloaded during import
		} else if sourceType == models.SourceTypeGitHub {
			if models.IsGuest(ownerID) {
				return nil, fmt.Errorf("%w: registration required for github deployments", ErrValidationFailed)
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
	deploymentMode := strings.ToLower(strings.TrimSpace(input.DeploymentMode))
	if deploymentMode == "" {
		deploymentMode = models.DeploymentModeStandard
	} else if !models.IsValidDeploymentMode(deploymentMode) {
		return nil, fmt.Errorf("%w: invalid deployment_mode %q (must be 'standard' or 'blue_green')", ErrValidationFailed, input.DeploymentMode)
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
		DeploymentMode:     deploymentMode,
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

			var isPublic bool
			if svcIn.PublicExposed != nil {
				isPublic = *svcIn.PublicExposed
			} else {
				// Backward compatibility for older API clients omitting public_exposed:
				// Never expose infrastructure/databases by default; expose web frontends or single apps.
				if svcClassification == models.ClassificationInfrastructure || strings.Contains(strings.ToLower(svcRole), "db") || strings.Contains(strings.ToLower(svcRole), "database") || strings.Contains(strings.ToLower(svcName), "postgres") || strings.Contains(strings.ToLower(svcName), "redis") {
					isPublic = false
				} else if svcRole == models.RoleFrontend || strings.Contains(strings.ToLower(svcRole), "web") || strings.Contains(strings.ToLower(svcName), "frontend") || (len(input.Services) == 1 && svcClassification == models.ClassificationApplication) {
					isPublic = true
				} else {
					isPublic = false
				}
			}

			sanitizedVolumes, _, err := security.ValidateAndSanitizeServiceVolumes(svcIn.Volumes)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid volume configuration for service %q: %v", ErrValidationFailed, svcName, err)
			}

			svcMode := strings.ToLower(strings.TrimSpace(svcIn.DeploymentMode))
			if svcMode == "" {
				svcMode = project.DeploymentMode
			} else if !models.IsValidDeploymentMode(svcMode) {
				return nil, fmt.Errorf("%w: invalid deployment_mode %q for service %q", ErrValidationFailed, svcIn.DeploymentMode, svcName)
			}

			svc := &models.Service{
				ID:                  uuid.New(),
				ProjectID:           project.ID,
				SourceID:            project.SourceID,
				Name:                svcName,
				Role:                svcRole,
				Classification:      svcClassification,
				Image:               strings.TrimSpace(svcIn.Image),
				User:                strings.TrimSpace(svcIn.User),
				DependsOn:           svcIn.DependsOn,
				DependsOnConditions: svcIn.DependsOnConditions,
				Volumes:             sanitizedVolumes,
				Networks:            svcIn.Networks,
				HealthCheckConfig:   svcIn.HealthCheckConfig,
				SourcePath:          svcSourcePath,
				RuntimeType:         rt,
				Framework:           svcIn.Framework,
				PackageManager:      svcIn.PackageManager,
				BuildStrategy:       svcStrat,
				BuildCandidates:     svcIn.BuildCandidates,
				BuildCommand:        strings.TrimSpace(svcIn.BuildCommand),
				StartCommand:        strings.TrimSpace(svcIn.StartCommand),
				DockerfilePath:      dfPath,
				BuildContext:        bCtx,
				InternalPort:        svcPort,
				HostPort:            svcIn.HostPort,
				PublicExposed:       isPublic,
				HealthStrategy:      svcHealthStrat,
				HealthCheckPath:     &svcHealthPath,
				HealthCheckEnabled:  true,
				DeploymentMode:      svcMode,
				Status:              models.ProjectStatusInactive,
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
			DeploymentMode:     project.DeploymentMode,
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

	// For guest project creation, serialize access and atomically verify quota using row lock on guest user
	if models.IsGuest(ownerID) {
		var lockedID uuid.UUID
		err = tx.QueryRow(ctx, "SELECT id FROM users WHERE id = $1 FOR UPDATE", ownerID).Scan(&lockedID)
		if err != nil {
			slog.Error("failed to acquire guest quota lock", "owner_id", ownerID, "error", err)
			return nil, fmt.Errorf("failed to acquire guest quota lock: %w", err)
		}

		var currentGuestCount int
		err = tx.QueryRow(ctx, "SELECT COUNT(*) FROM projects WHERE owner_id = $1", ownerID).Scan(&currentGuestCount)
		if err != nil {
			slog.Error("failed to count guest projects within transaction", "owner_id", ownerID, "error", err)
			return nil, fmt.Errorf("failed to check guest project quota: %w", err)
		}
		if currentGuestCount >= 20 {
			return nil, fmt.Errorf("%w: guest deployment limit reached (maximum 20 projects)", ErrValidationFailed)
		}
		if len(servicesToCreate) > 10 {
			return nil, fmt.Errorf("%w: guest projects can have at most 10 services", ErrValidationFailed)
		}
		for _, s := range servicesToCreate {
			if s.CpuMillicores > 1000 {
				return nil, fmt.Errorf("%w: guest quota exceeded: maximum 1000 millicores (1 core) per service", ErrValidationFailed)
			}
			if s.MemoryMB > 1024 {
				return nil, fmt.Errorf("%w: guest quota exceeded: maximum 1024 MB (1 GB) per service", ErrValidationFailed)
			}
		}
	}

	// 1. Insert or update source record
	fingerprint := ""
	metadataJSON := []byte("{}")
	if importedRecord != nil {
		fingerprint = importedRecord.CommitSHA
		metaMap := map[string]interface{}{
			"snapshot_path": importedRecord.SnapshotPath,
			"commit_sha":    importedRecord.CommitSHA,
			"import_id":     importedRecord.ID.String(),
			"root_dir":      importedRecord.RootDir,
			"source_type":   importedRecord.SourceType,
			"is_private":    importedRecord.IsPrivate,
		}
		if b, err := json.Marshal(metaMap); err == nil {
			metadataJSON = b
		}
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO sources (id, owner_id, source_type, source_reference, agent_id, fingerprint, metadata, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW())
		 ON CONFLICT (id) DO UPDATE SET
		    source_type = EXCLUDED.source_type,
		    source_reference = EXCLUDED.source_reference,
		    agent_id = EXCLUDED.agent_id,
		    fingerprint = EXCLUDED.fingerprint,
		    metadata = EXCLUDED.metadata,
		    updated_at = NOW()`,
		sourceUUID, ownerID, sourceType, sourceRef, agentIDStr, fingerprint, metadataJSON,
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
			status, deployment_strategy, deployment_plan, deployment_mode, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11, $12, $13, $14,
			$15, $16, $17, $18, $19,
			$20, $21, $22, $23, $24, $25
		)`,
		project.ID, project.OwnerID, project.SourceID, project.Name, project.Slug, project.SourceType, project.SourceReference,
		project.RepositoryPath, project.Branch, project.DockerfilePath, project.BuildContext,
		project.BuildStrategy, project.BuildCommand, project.StartCommand,
		project.RuntimeType, project.InternalPort, project.HealthCheckPath, project.HealthCheckEnabled,
		project.HealthStrategy, project.Status, project.DeploymentStrategy, project.DeploymentPlan, project.DeploymentMode,
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

	// If imported repository, mark the import as completed
	if importedRecord != nil {
		if s.importService != nil {
			_ = s.importService.MarkCompleted(ctx, ownerID, importedRecord.ID, project.ID)
		} else if s.db != nil {
			_, _ = s.db.Exec(ctx, `UPDATE repository_imports SET status = 'completed', project_id = $1, completed_at = NOW(), updated_at = NOW() WHERE id = $2 AND owner_id = $3`, project.ID, importedRecord.ID, ownerID)
		}
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
