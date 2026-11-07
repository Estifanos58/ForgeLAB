package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/security"
)

var (
	ErrDeploymentNotFound            = errors.New("deployment not found")
	ErrNoDeploymentToRollback        = errors.New("no previous deployment to rollback to")
	ErrCannotCancelRunningDeployment = errors.New("cannot cancel a deployment that is already running")
	ErrDeploymentAlreadyTerminal     = errors.New("deployment is already in a terminal state")
	ErrProjectDeleting               = errors.New("cannot perform deployment: project deletion is in progress")
)

// DeploymentService handles deployment-related business logic.
type DeploymentService struct {
	db            *pgxpool.Pool
	secretService *SecretService
	githubService *GitHubService
}

// NewDeploymentService creates a new DeploymentService.
func NewDeploymentService(db *pgxpool.Pool) *DeploymentService {
	return &DeploymentService{db: db}
}

// SetSecretService sets the SecretService instance for env hash calculations.
func (s *DeploymentService) SetSecretService(sec *SecretService) {
	s.secretService = sec
}

// SetGitHubService sets the GitHubService instance for commit SHA resolution.
func (s *DeploymentService) SetGitHubService(gh *GitHubService) {
	s.githubService = gh
}

// resolveSourceRevision computes an immutable source revision:
//   - GitHub commit SHA if available (pinned 40-character SHA), otherwise resolves via githubService
//   - For local directories: fresh content fingerprint from directory if accessible on disk.
//     If source files have changed after plan review, invalidates the plan and returns an explicit error.
//   - For local/agent/upload sources: deterministic source fingerprint from sources table or plan SourceRevision
//
// Fails explicitly if fingerprinting fails.
func (s *DeploymentService) resolveSourceRevision(ctx context.Context, project *models.Project, commitSHA *string) (*string, error) {
	if project == nil {
		return nil, nil
	}

	if project.SourceType == models.SourceTypeGitHub || project.SourceType == models.SourceTypeGitHubPublic {
		var targetSHA string
		if commitSHA != nil && len(*commitSHA) == 40 {
			targetSHA = *commitSHA
		}
		if targetSHA == "" && s.githubService != nil {
			repoRef := project.SourceReference
			if repoRef == "" {
				repoRef = project.RepositoryPath
			}
			parts := strings.Split(repoRef, "/")
			if len(parts) >= 2 {
				owner, repo := parts[0], parts[1]
				branch := project.Branch
				if branch == "" {
					branch = "main"
				}
				if resolvedSHA, err := s.githubService.ResolveCommitSHA(ctx, project.OwnerID, owner, repo, branch); err == nil && len(resolvedSHA) == 40 {
					targetSHA = resolvedSHA
				}
			}
		}
		if targetSHA == "" && project.SourceID != nil && s.db != nil {
			var fp string
			var metaBytes []byte
			if err := s.db.QueryRow(ctx, "SELECT fingerprint, metadata FROM sources WHERE id = $1", *project.SourceID).Scan(&fp, &metaBytes); err == nil {
				if len(fp) == 40 {
					targetSHA = fp
				} else if len(metaBytes) > 0 {
					var meta map[string]interface{}
					if err := json.Unmarshal(metaBytes, &meta); err == nil {
						if csha, ok := meta["commit_sha"].(string); ok && len(csha) == 40 {
							targetSHA = csha
						}
					}
				}
			}
		}
		if targetSHA == "" {
			// Check if project has an authoritative DeploymentPlan with a pinned 40-character commit SHA
			if len(project.DeploymentPlan) > 0 && string(project.DeploymentPlan) != "{}" {
				var plan struct {
					SourceRevision string `json:"source_revision"`
				}
				if err := json.Unmarshal(project.DeploymentPlan, &plan); err == nil {
					rev := strings.TrimSpace(plan.SourceRevision)
					if len(rev) == 40 {
						targetSHA = rev
					}
				}
			}
		}

		if targetSHA != "" {
			if len(project.DeploymentPlan) > 0 && string(project.DeploymentPlan) != "{}" {
				var plan struct {
					SourceRevision string `json:"source_revision"`
				}
				if err := json.Unmarshal(project.DeploymentPlan, &plan); err == nil && strings.TrimSpace(plan.SourceRevision) != "" {
					if plan.SourceRevision != targetSHA {
						if s.db != nil {
							_, _ = s.db.Exec(ctx, "UPDATE projects SET deployment_plan = '{}'::jsonb WHERE id = $1", project.ID)
						}
						project.DeploymentPlan = []byte("{}")
						return nil, fmt.Errorf("remote GitHub repository revision changed after deployment plan review (reviewed: %s, latest: %s); deployment plan has been invalidated, please rediscover and review the plan", plan.SourceRevision, targetSHA)
					}
				}
			}
			return &targetSHA, nil
		}
		// Do not return mutable branch strings for GitHub deployments to guarantee reproducibility
		return nil, fmt.Errorf("failed to pin immutable 40-character commit SHA for GitHub deployment of project '%s'", project.Name)
	}

	// For local directory: if the directory exists on disk, compute fresh content fingerprint.
	// Ensure that its fingerprint, discovery results, service topology, reviewed deployment plan,
	// cache key, and resulting image all correspond to exactly the same content.
	if (project.SourceType == models.SourceTypeLocalDirectory || project.SourceType == models.SourceTypeLocal) && project.RepositoryPath != "" {
		if fi, err := os.Stat(project.RepositoryPath); err == nil && fi.IsDir() {
			fp, fpErr := discovery.ComputeDirectoryContentFingerprint(project.RepositoryPath)
			if fpErr != nil {
				return nil, fmt.Errorf("failed to compute source directory fingerprint for %q: %w", project.RepositoryPath, fpErr)
			}
			if fp != "" {
				// If source files change after review, invalidate the plan and require fresh discovery/review rather than updating only source_revision
				if len(project.DeploymentPlan) > 0 && string(project.DeploymentPlan) != "{}" {
					var plan struct {
						SourceRevision string `json:"source_revision"`
					}
					if err := json.Unmarshal(project.DeploymentPlan, &plan); err == nil && strings.TrimSpace(plan.SourceRevision) != "" {
						if plan.SourceRevision != fp {
							if s.db != nil {
								_, _ = s.db.Exec(ctx, "UPDATE projects SET deployment_plan = '{}'::jsonb WHERE id = $1", project.ID)
							}
							project.DeploymentPlan = []byte("{}")
							return nil, fmt.Errorf("source files changed after deployment plan review (reviewed revision: %s, current revision: %s); deployment plan has been invalidated, please rediscover and review the plan", plan.SourceRevision, fp)
						}
					}
				}
				return &fp, nil
			}
		} else if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to access source directory %q: %w", project.RepositoryPath, err)
		}
	}

	// Check if project has an authoritative DeploymentPlan with SourceRevision (e.g. offline mock or pre-verified plan)
	if len(project.DeploymentPlan) > 0 && string(project.DeploymentPlan) != "{}" {
		var plan struct {
			SourceRevision string `json:"source_revision"`
		}
		if err := json.Unmarshal(project.DeploymentPlan, &plan); err == nil && strings.TrimSpace(plan.SourceRevision) != "" {
			rev := strings.TrimSpace(plan.SourceRevision)
			return &rev, nil
		}
	}

	// For local_agent, local_upload, or local_directory: check if source has an immutable fingerprint in database
	if s.db != nil {
		var srcID uuid.UUID
		if project.SourceID != nil && *project.SourceID != uuid.Nil {
			srcID = *project.SourceID
		} else if parsed, err := uuid.Parse(project.SourceReference); err == nil {
			srcID = parsed
		}
		if srcID != uuid.Nil {
			var fingerprint string
			err := s.db.QueryRow(ctx, "SELECT fingerprint FROM sources WHERE id = $1", srcID).Scan(&fingerprint)
			if err == nil && strings.TrimSpace(fingerprint) != "" {
				fp := strings.TrimSpace(fingerprint)
				if len(project.DeploymentPlan) > 0 && string(project.DeploymentPlan) != "{}" {
					var plan struct {
						SourceRevision string `json:"source_revision"`
					}
					if err := json.Unmarshal(project.DeploymentPlan, &plan); err == nil && plan.SourceRevision != "" {
						if plan.SourceRevision != fp {
							_, _ = s.db.Exec(ctx, "UPDATE projects SET deployment_plan = '{}'::jsonb WHERE id = $1", project.ID)
							project.DeploymentPlan = []byte("{}")
							return nil, fmt.Errorf("source files changed after deployment plan review (reviewed revision: %s, current revision: %s); deployment plan has been invalidated, please rediscover and review the plan", plan.SourceRevision, fp)
						}
					}
				}
				return &fp, nil
			}
		}
	}

	// Fallback to deterministic fingerprint of source reference
	ref := project.SourceReference
	if ref == "" {
		ref = project.RepositoryPath
	}
	if ref != "" {
		h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s", project.SourceType, ref)))
		fp := fmt.Sprintf("fp_%s", hex.EncodeToString(h[:16]))
		return &fp, nil
	}

	return nil, nil
}

// CreateDeployment creates a new deployment record for a project transactionally.
// This does NOT start the deployment — it only creates the record.
// The deployment worker picks up QUEUED deployments.
func (s *DeploymentService) CreateDeployment(ctx context.Context, project *models.Project) (*models.Deployment, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Lock the project row FOR UPDATE to prevent concurrent creation for the same project
	var lockedProjectID uuid.UUID
	var currentProjectStatus string
	err = tx.QueryRow(ctx, "SELECT id, status FROM projects WHERE id = $1 FOR UPDATE", project.ID).Scan(&lockedProjectID, &currentProjectStatus)
	if err != nil {
		return nil, fmt.Errorf("failed to lock project row: %w", err)
	}
	if currentProjectStatus == models.ProjectStatusDeleting {
		return nil, ErrProjectDeleting
	}

	// Limit active in-flight guest deployments to prevent resource exhaustion
	if models.IsGuest(project.OwnerID) {
		var activeGuestDeployments int
		err = tx.QueryRow(ctx,
			`SELECT (
				(SELECT COUNT(*) FROM deployments d JOIN projects p ON d.project_id = p.id WHERE p.owner_id = $1 AND d.status IN ($2, $3, $4, $5, $6))
				+
				(SELECT COUNT(*) FROM service_deployments sd JOIN services s ON sd.service_id = s.id JOIN projects p ON s.project_id = p.id WHERE p.owner_id = $1 AND sd.status IN ($2, $3, $4, $5, $6))
			)`,
			project.OwnerID,
			models.DeployStatusQueued,
			models.DeployStatusCloning,
			models.DeployStatusBuilding,
			models.DeployStatusStarting,
			models.DeployStatusHealthChecking,
		).Scan(&activeGuestDeployments)
		if err != nil {
			return nil, fmt.Errorf("failed to check guest active deployment quota: %w", err)
		}
		if activeGuestDeployments >= 5 {
			return nil, fmt.Errorf("%w: guest deployment concurrency limit reached (maximum 5 active deployments in progress)", ErrValidationFailed)
		}
	}

	// Check for existing active deployment
	// If an existing deployment is still in 'queued' state, the newer deployment supersedes it.
	var activeID *uuid.UUID
	var activeStatus *string
	err = tx.QueryRow(ctx,
		`SELECT id, status FROM deployments 
		 WHERE project_id = $1 AND status IN ($2, $3, $4, $5, $6)
		 ORDER BY created_at DESC LIMIT 1`,
		project.ID,
		models.DeployStatusQueued,
		models.DeployStatusCloning,
		models.DeployStatusBuilding,
		models.DeployStatusStarting,
		models.DeployStatusHealthChecking,
	).Scan(&activeID, &activeStatus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("failed to check active deployments: %w", err)
	}

	if activeID != nil && activeStatus != nil {
		if *activeStatus == models.DeployStatusQueued {
			supersedeReason := "Superseded by newer deployment"
			now := time.Now()
			_, err = tx.Exec(ctx,
				`UPDATE deployments
				 SET status = $1, failure_reason = $2, finished_at = $3
				 WHERE id = $4 AND status = $5`,
				models.DeployStatusFailed, supersedeReason, now, *activeID, models.DeployStatusQueued,
			)
			if err != nil {
				return nil, fmt.Errorf("failed to supersede queued deployment: %w", err)
			}
			_, _ = tx.Exec(ctx,
				`INSERT INTO deployment_logs (id, deployment_id, service_id, timestamp, phase, stream, message)
				 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				uuid.New(), *activeID, nil, now, models.LogPhaseStartup, models.LogStreamSystem, "Deployment superseded by newer queued deployment",
			)
			slog.Info("superseded queued deployment", "deployment_id", *activeID, "project_id", project.ID)
		} else {
			return nil, ErrActiveDeployment
		}
	}

	// Get next deploy number safely inside locked transaction
	var maxNumber *int
	err = tx.QueryRow(ctx,
		"SELECT MAX(deploy_number) FROM deployments WHERE project_id = $1",
		project.ID,
	).Scan(&maxNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get deploy number: %w", err)
	}

	deployNumber := 1
	if maxNumber != nil {
		deployNumber = *maxNumber + 1
	}

	now := time.Now()
	imageTag := fmt.Sprintf("forgelab/%s:%d", project.ID, deployNumber)

	deployment := &models.Deployment{
		ID:             uuid.New(),
		ProjectID:      project.ID,
		DeployNumber:   deployNumber,
		Status:         models.DeployStatusQueued,
		Branch:         project.Branch,
		ImageTag:       &imageTag,
		BuildStrategy:  project.BuildStrategy,
		BuildCommand:   project.BuildCommand,
		StartCommand:   project.StartCommand,
		RuntimeType:    project.RuntimeType,
		InternalPort:   project.InternalPort,
		HealthStrategy: project.HealthStrategy,
		ExecutionMode:  models.ExecutionModeBuild,
		StartedAt:      &now,
		CreatedAt:      now,
	}

	sourceRevision, err := s.resolveSourceRevision(ctx, project, nil)
	if err != nil {
		return nil, err
	}
	if (project.SourceType == models.SourceTypeGitHub || project.SourceType == models.SourceTypeGitHubPublic) && (sourceRevision == nil || len(*sourceRevision) != 40) {
		return nil, fmt.Errorf("failed to pin immutable 40-character commit SHA for GitHub deployment of project '%s'", project.Name)
	}
	deployment.SourceRevision = sourceRevision
	if (project.SourceType == models.SourceTypeGitHub || project.SourceType == models.SourceTypeGitHubPublic) && sourceRevision != nil && len(*sourceRevision) == 40 {
		deployment.CommitSHA = sourceRevision
	}

	if s.secretService != nil {
		envSnap, envHash, err := s.secretService.CreateEnvSnapshot(ctx, project.ID, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create environment snapshot for project %s: %w", project.ID, err)
		}
		deployment.EnvSnapshot = envSnap
		deployment.EnvConfigHash = envHash
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO deployments (
			id, project_id, deploy_number, status, branch, image_tag,
			build_strategy, build_command, start_command, runtime_type, internal_port, health_strategy,
			execution_mode, env_config_hash, env_snapshot, started_at, created_at,
			deployment_strategy, deployment_plan, commit_sha, source_revision
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)`,
		deployment.ID, deployment.ProjectID, deployment.DeployNumber,
		deployment.Status, deployment.Branch, deployment.ImageTag,
		deployment.BuildStrategy, deployment.BuildCommand, deployment.StartCommand,
		deployment.RuntimeType, deployment.InternalPort, deployment.HealthStrategy,
		deployment.ExecutionMode, deployment.EnvConfigHash, deployment.EnvSnapshot,
		deployment.StartedAt, deployment.CreatedAt,
		project.DeploymentStrategy, project.DeploymentPlan,
		deployment.CommitSHA, deployment.SourceRevision,
	)
	if err != nil {
		if strings.Contains(err.Error(), "uq_active_deployment_per_project") || strings.Contains(err.Error(), "uq_deployments_project_number") {
			return nil, ErrActiveDeployment
		}
		return nil, fmt.Errorf("failed to create deployment: %w", err)
	}

	// Create service deployments for all project services
	rows, err := tx.Query(ctx,
		`SELECT id, name, role, build_strategy, build_command, start_command, runtime_type, internal_port,
		        COALESCE(dockerfile_path, ''), COALESCE(build_context, '.'),
		        COALESCE(health_strategy, 'auto'), health_check_path,
		        cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb,
		        COALESCE(classification, 'application'), COALESCE(image, ''),
		        COALESCE(depends_on, '[]'::jsonb), COALESCE(volumes, '[]'::jsonb),
		        COALESCE(networks, '[]'::jsonb), COALESCE(healthcheck_config, '{}'::jsonb),
		        COALESCE(depends_on_conditions, '{}'::jsonb)
		 FROM services WHERE project_id = $1`,
		project.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query services for deployment: %w", err)
	}
	defer rows.Close()

	var svcList []models.Service
	for rows.Next() {
		var s models.Service
		var dependsOnJSON, volumesJSON, networksJSON, hcJSON, docJSON []byte
		if err := rows.Scan(
			&s.ID, &s.Name, &s.Role, &s.BuildStrategy, &s.BuildCommand, &s.StartCommand, &s.RuntimeType,
			&s.InternalPort, &s.DockerfilePath, &s.BuildContext, &s.HealthStrategy, &s.HealthCheckPath,
			&s.CpuMillicores, &s.MemoryMB, &s.PidsLimit, &s.EphemeralStorageMB,
			&s.Classification, &s.Image, &dependsOnJSON, &volumesJSON, &networksJSON, &hcJSON, &docJSON,
		); err != nil {
			return nil, fmt.Errorf("failed to scan service for deployment: %w", err)
		}
		if len(dependsOnJSON) > 0 {
			if err := json.Unmarshal(dependsOnJSON, &s.DependsOn); err != nil {
				return nil, fmt.Errorf("failed to decode depends_on for service %s: %w", s.Name, err)
			}
		}
		if s.DependsOn == nil {
			s.DependsOn = []string{}
		}
		if len(volumesJSON) > 0 {
			if err := json.Unmarshal(volumesJSON, &s.Volumes); err != nil {
				return nil, fmt.Errorf("failed to decode volumes for service %s: %w", s.Name, err)
			}
		}
		if s.Volumes == nil {
			s.Volumes = []models.VolumeMountConfig{}
		}
		if len(networksJSON) > 0 {
			if err := json.Unmarshal(networksJSON, &s.Networks); err != nil {
				return nil, fmt.Errorf("failed to decode networks for service %s: %w", s.Name, err)
			}
		}
		if s.Networks == nil {
			s.Networks = []string{}
		}
		if len(hcJSON) > 0 && string(hcJSON) != "{}" {
			var hc models.HealthCheckConfig
			if err := json.Unmarshal(hcJSON, &hc); err != nil {
				return nil, fmt.Errorf("failed to decode healthcheck_config for service %s: %w", s.Name, err)
			}
			s.HealthCheckConfig = &hc
		}
		if len(docJSON) > 0 && string(docJSON) != "{}" {
			if err := json.Unmarshal(docJSON, &s.DependsOnConditions); err != nil {
				return nil, fmt.Errorf("failed to decode depends_on_conditions for service %s: %w", s.Name, err)
			}
		}
		if s.DependsOnConditions == nil {
			s.DependsOnConditions = map[string]string{}
		}
		svcList = append(svcList, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating services for deployment: %w", err)
	}

	for _, svcItem := range svcList {
		var svcMaxNumber *int
		_ = tx.QueryRow(ctx, "SELECT MAX(deploy_number) FROM service_deployments WHERE service_id = $1", svcItem.ID).Scan(&svcMaxNumber)
		svcDeployNum := 1
		if svcMaxNumber != nil {
			svcDeployNum = *svcMaxNumber + 1
		}

		sourceRevision := deployment.SourceRevision

		var envConfigHash *string
		var envSnapshot []byte
		if s.secretService != nil {
			var snapErr error
			envSnapshot, envConfigHash, snapErr = s.secretService.CreateEnvSnapshot(ctx, project.ID, &svcItem.ID)
			if snapErr != nil {
				return nil, fmt.Errorf("failed to create environment snapshot for service %s: %w", svcItem.Name, snapErr)
			}
		}

		dependsOnBytes, _ := json.Marshal(svcItem.DependsOn)
		safeVols, _ := security.SanitizeServiceVolumes(svcItem.Volumes)
		volumesBytes, _ := json.Marshal(safeVols)
		networksBytes, _ := json.Marshal(svcItem.Networks)
		var hcBytes []byte
		if svcItem.HealthCheckConfig != nil {
			hcBytes, _ = json.Marshal(svcItem.HealthCheckConfig)
		} else {
			hcBytes = []byte("{}")
		}
		dependsOnCondBytes, _ := json.Marshal(svcItem.DependsOnConditions)

		svcDeployID := uuid.New()
		svcTag := fmt.Sprintf("forgelab/%s/%s:%d", project.ID, svcItem.Name, svcDeployNum)
		if _, err := tx.Exec(ctx,
			`INSERT INTO service_deployments (
				id, deployment_id, service_id, deploy_number, status, image_tag, build_strategy,
				build_command, start_command, runtime_type, dockerfile_path, build_context, internal_port,
				health_strategy, health_check_path, cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb,
				source_revision, env_config_hash, execution_mode, env_snapshot,
				started_at, created_at,
				classification, image, depends_on, volumes, networks, healthcheck_config,
				depends_on_conditions
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32)`,
			svcDeployID, deployment.ID, svcItem.ID, svcDeployNum, models.DeployStatusQueued, svcTag,
			svcItem.BuildStrategy, svcItem.BuildCommand, svcItem.StartCommand, svcItem.RuntimeType, svcItem.DockerfilePath, svcItem.BuildContext,
			svcItem.InternalPort, svcItem.HealthStrategy, svcItem.HealthCheckPath,
			svcItem.CpuMillicores, svcItem.MemoryMB, svcItem.PidsLimit, svcItem.EphemeralStorageMB,
			sourceRevision, envConfigHash, models.ExecutionModeBuild, envSnapshot,
			now, now,
			svcItem.Classification, svcItem.Image, dependsOnBytes, volumesBytes, networksBytes, hcBytes,
			dependsOnCondBytes,
		); err != nil {
			return nil, fmt.Errorf("failed to create service deployment record: %w", err)
		}
		// Set service status to deploying but do NOT update current_service_deployment_id.
		// current_service_deployment_id must only be set on successful promotion.
		if _, err := tx.Exec(ctx,
			"UPDATE services SET status = $1, updated_at = $2 WHERE id = $3",
			models.ServiceStatusDeploying, now, svcItem.ID,
		); err != nil {
			return nil, fmt.Errorf("failed to update service deployment status: %w", err)
		}
	}

	// Update project status to deploying
	_, err = tx.Exec(ctx,
		"UPDATE projects SET status = $1, updated_at = $2 WHERE id = $3",
		models.ProjectStatusDeploying, time.Now(), project.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update project status: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit deployment creation: %w", err)
	}

	slog.Info("deployment created",
		"deployment_id", deployment.ID,
		"project_id", project.ID,
		"deploy_number", deployNumber,
	)

	return deployment, nil
}

// CreateServiceDeployment creates a new independent deployment record targeting a single specific service.
// This enforces an active-deployment constraint per service_id, NOT per project.
func (s *DeploymentService) CreateServiceDeployment(ctx context.Context, project *models.Project, targetService *models.Service) (*models.ServiceDeployment, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Check if parent project is currently being deleted
	var parentProjStatus string
	err = tx.QueryRow(ctx, "SELECT status FROM projects WHERE id = $1", project.ID).Scan(&parentProjStatus)
	if err != nil {
		return nil, fmt.Errorf("failed to check project status: %w", err)
	}
	if parentProjStatus == models.ProjectStatusDeleting {
		return nil, ErrProjectDeleting
	}

	// Limit active in-flight guest service deployments to prevent resource exhaustion
	if models.IsGuest(project.OwnerID) {
		var activeGuestDeployments int
		err = tx.QueryRow(ctx,
			`SELECT (
				(SELECT COUNT(*) FROM deployments d JOIN projects p ON d.project_id = p.id WHERE p.owner_id = $1 AND d.status IN ($2, $3, $4, $5, $6))
				+
				(SELECT COUNT(*) FROM service_deployments sd JOIN services s ON sd.service_id = s.id JOIN projects p ON s.project_id = p.id WHERE p.owner_id = $1 AND sd.status IN ($2, $3, $4, $5, $6))
			)`,
			project.OwnerID,
			models.DeployStatusQueued,
			models.DeployStatusCloning,
			models.DeployStatusBuilding,
			models.DeployStatusStarting,
			models.DeployStatusHealthChecking,
		).Scan(&activeGuestDeployments)
		if err != nil {
			return nil, fmt.Errorf("failed to check guest active service deployment quota: %w", err)
		}
		if activeGuestDeployments >= 5 {
			return nil, fmt.Errorf("%w: guest deployment concurrency limit reached (maximum 5 active deployments in progress)", ErrValidationFailed)
		}
	}

	// Lock only the target service row FOR UPDATE to prevent concurrent creation for the exact same service
	var lockedServiceID uuid.UUID
	err = tx.QueryRow(ctx, "SELECT id FROM services WHERE id = $1 FOR UPDATE", targetService.ID).Scan(&lockedServiceID)
	if err != nil {
		return nil, fmt.Errorf("failed to lock service row: %w", err)
	}

	// Enforce active deployment check strictly per service_id (Frontend does not block Backend!)
	// If an existing deployment for this service is still in 'queued' state, the newer deployment supersedes it.
	// If an existing deployment has already started executing (cloning, building, starting, health_checking),
	// reject with ErrActiveDeployment to protect in-flight progress.
	var activeID *uuid.UUID
	var activeStatus *string
	err = tx.QueryRow(ctx,
		`SELECT id, status FROM service_deployments 
		 WHERE service_id = $1 AND status IN ($2, $3, $4, $5, $6)
		 ORDER BY created_at DESC LIMIT 1`,
		targetService.ID,
		models.DeployStatusQueued,
		models.DeployStatusCloning,
		models.DeployStatusBuilding,
		models.DeployStatusStarting,
		models.DeployStatusHealthChecking,
	).Scan(&activeID, &activeStatus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("failed to check active service deployments: %w", err)
	}

	if activeID != nil && activeStatus != nil {
		if *activeStatus == models.DeployStatusQueued {
			supersedeReason := "Superseded by newer deployment"
			now := time.Now()
			_, err = tx.Exec(ctx,
				`UPDATE service_deployments
				 SET status = $1, failure_reason = $2, finished_at = $3,
				     duration_ms = EXTRACT(EPOCH FROM ($3 - COALESCE(started_at, created_at))) * 1000
				 WHERE id = $4 AND status = $5`,
				models.DeployStatusFailed, supersedeReason, now, *activeID, models.DeployStatusQueued,
			)
			if err != nil {
				return nil, fmt.Errorf("failed to supersede queued service deployment: %w", err)
			}
			_, _ = tx.Exec(ctx,
				`INSERT INTO deployment_logs (id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				uuid.New(), nil, *activeID, targetService.ID, now, models.LogPhaseStartup, models.LogStreamSystem, "Deployment superseded by newer queued deployment",
			)
			slog.Info("superseded queued service deployment", "service_deployment_id", *activeID, "service_id", targetService.ID)
		} else {
			return nil, ErrActiveDeployment
		}
	}

	// Get next service deploy number safely inside locked transaction
	var maxNumber *int
	err = tx.QueryRow(ctx,
		"SELECT MAX(deploy_number) FROM service_deployments WHERE service_id = $1",
		targetService.ID,
	).Scan(&maxNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get service deploy number: %w", err)
	}

	deployNumber := 1
	if maxNumber != nil {
		deployNumber = *maxNumber + 1
	}

	now := time.Now()
	imageTag := fmt.Sprintf("forgelab/%s/%s:%d", project.ID, targetService.Name, deployNumber)

	buildStrategy := targetService.BuildStrategy
	if buildStrategy == "" {
		buildStrategy = project.BuildStrategy
	}
	if buildStrategy == "" {
		buildStrategy = models.BuildStrategyAuto
	}
	buildCommand := targetService.BuildCommand
	startCommand := targetService.StartCommand
	runtimeType := targetService.RuntimeType
	if runtimeType == "" {
		runtimeType = project.RuntimeType
	}
	internalPort := targetService.InternalPort
	if internalPort <= 0 {
		internalPort = project.InternalPort
	}
	if internalPort <= 0 {
		internalPort = 8080
	}
	dockerfilePath := targetService.DockerfilePath
	if dockerfilePath == "" {
		dockerfilePath = "Dockerfile"
	}
	buildContext := targetService.BuildContext
	if buildContext == "" {
		buildContext = "."
	}
	healthStrategy := targetService.HealthStrategy
	if healthStrategy == "" {
		healthStrategy = project.HealthStrategy
	}
	if healthStrategy == "" {
		healthStrategy = models.HealthStrategyAuto
	}
	healthCheckPath := targetService.HealthCheckPath

	// Snapshot resource config from the service
	resConfig := targetService.ResourceConfig
	if resConfig.CpuMillicores <= 0 {
		resConfig.CpuMillicores = 1000
	}
	if resConfig.MemoryMB <= 0 {
		resConfig.MemoryMB = 1024
	}
	if resConfig.PidsLimit <= 0 {
		resConfig.PidsLimit = 256
	}

	sourceRevision, err := s.resolveSourceRevision(ctx, project, nil)
	if err != nil {
		return nil, err
	}
	if (project.SourceType == models.SourceTypeGitHub || project.SourceType == models.SourceTypeGitHubPublic) && (sourceRevision == nil || len(*sourceRevision) != 40) {
		return nil, fmt.Errorf("failed to pin immutable 40-character commit SHA for GitHub service deployment of service '%s'", targetService.Name)
	}

	var envConfigHash *string
	var envSnapshot []byte
	if s.secretService != nil {
		var snapErr error
		envSnapshot, envConfigHash, snapErr = s.secretService.CreateEnvSnapshot(ctx, project.ID, &targetService.ID)
		if snapErr != nil {
			return nil, fmt.Errorf("failed to create environment snapshot for service %s: %w", targetService.Name, snapErr)
		}
	}

	svcDeployID := uuid.New()
	serviceDeployment := &models.ServiceDeployment{
		ID:                  svcDeployID,
		DeploymentID:        nil,
		ServiceID:           targetService.ID,
		ServiceName:         targetService.Name,
		DeployNumber:        deployNumber,
		Status:              models.DeployStatusQueued,
		ImageTag:            &imageTag,
		InternalPort:        internalPort,
		BuildStrategy:       buildStrategy,
		BuildCommand:        buildCommand,
		StartCommand:        startCommand,
		RuntimeType:         runtimeType,
		DockerfilePath:      dockerfilePath,
		BuildContext:        buildContext,
		HealthStrategy:      healthStrategy,
		HealthCheckPath:     healthCheckPath,
		ResourceConfig:      resConfig,
		ExecutionMode:       models.ExecutionModeBuild,
		SourceRevision:      sourceRevision,
		EnvConfigHash:       envConfigHash,
		EnvSnapshot:         envSnapshot,
		Classification:      targetService.Classification,
		Image:               targetService.Image,
		DependsOn:           targetService.DependsOn,
		DependsOnConditions: targetService.DependsOnConditions,
		Volumes:             targetService.Volumes,
		Networks:            targetService.Networks,
		HealthCheckConfig:   targetService.HealthCheckConfig,
		StartedAt:           &now,
		CreatedAt:           now,
	}

	dependsOnBytes, _ := json.Marshal(targetService.DependsOn)
	volumesBytes, _ := json.Marshal(targetService.Volumes)
	networksBytes, _ := json.Marshal(targetService.Networks)
	var hcBytes []byte
	if targetService.HealthCheckConfig != nil {
		hcBytes, _ = json.Marshal(targetService.HealthCheckConfig)
	} else {
		hcBytes = []byte("{}")
	}
	dependsOnCondBytes, _ := json.Marshal(targetService.DependsOnConditions)

	_, err = tx.Exec(ctx,
		`INSERT INTO service_deployments (
			id, deployment_id, service_id, deploy_number, status, image_tag,
			build_strategy, build_command, start_command, runtime_type,
			dockerfile_path, build_context, internal_port, health_strategy, health_check_path,
			cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb,
			source_revision, env_config_hash, execution_mode, env_snapshot,
			started_at, created_at,
			classification, image, depends_on, volumes, networks, healthcheck_config,
			depends_on_conditions
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32)`,
		serviceDeployment.ID, serviceDeployment.DeploymentID, serviceDeployment.ServiceID,
		serviceDeployment.DeployNumber, serviceDeployment.Status, serviceDeployment.ImageTag,
		serviceDeployment.BuildStrategy, serviceDeployment.BuildCommand, serviceDeployment.StartCommand,
		serviceDeployment.RuntimeType, serviceDeployment.DockerfilePath, serviceDeployment.BuildContext,
		serviceDeployment.InternalPort, serviceDeployment.HealthStrategy, serviceDeployment.HealthCheckPath,
		resConfig.CpuMillicores, resConfig.MemoryMB, resConfig.PidsLimit, resConfig.EphemeralStorageMB,
		sourceRevision, envConfigHash, models.ExecutionModeBuild, envSnapshot,
		serviceDeployment.StartedAt, serviceDeployment.CreatedAt,
		serviceDeployment.Classification, serviceDeployment.Image, dependsOnBytes, volumesBytes, networksBytes, hcBytes,
		dependsOnCondBytes,
	)
	if err != nil {
		if strings.Contains(err.Error(), "uq_active_service_deployment") || strings.Contains(err.Error(), "uq_service_deployments_service_number") {
			return nil, ErrActiveDeployment
		}
		return nil, fmt.Errorf("failed to create service deployment: %w", err)
	}

	// Set service status to deploying but do NOT update current_service_deployment_id.
	// current_service_deployment_id must only be updated on successful promotion.
	_, err = tx.Exec(ctx,
		"UPDATE services SET status = $1, updated_at = $2 WHERE id = $3",
		models.ServiceStatusDeploying, now, targetService.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update target service status: %w", err)
	}

	// Update project status according to composite service states
	allServices, _ := s.listServicesTx(ctx, tx, project.ID)
	newProjectStatus := CalculateProjectStatus(allServices)
	_, err = tx.Exec(ctx,
		"UPDATE projects SET status = $1, updated_at = $2 WHERE id = $3",
		newProjectStatus, now, project.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update project status: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit service deployment: %w", err)
	}

	slog.Info("service deployment created",
		"service_deployment_id", serviceDeployment.ID,
		"project_id", project.ID,
		"service_id", targetService.ID,
		"service_name", targetService.Name,
		"deploy_number", deployNumber,
	)

	return serviceDeployment, nil
}

// GetDeployment retrieves a deployment by ID.
func (s *DeploymentService) GetDeployment(ctx context.Context, deploymentID uuid.UUID) (*models.Deployment, error) {
	d := &models.Deployment{}
	err := s.db.QueryRow(ctx,
		`SELECT id, project_id, deploy_number, status, commit_sha, branch,
		 image_tag, container_id, source_revision, build_strategy, build_command,
		 start_command, runtime_type, internal_port, health_strategy,
		 COALESCE(execution_mode, 'build'), image_digest, env_config_hash, env_snapshot,
		 started_at, built_at, deployed_at, finished_at, duration_ms, failure_reason, created_at,
		 COALESCE(deployment_strategy, 'dockerfile'), deployment_plan
		 FROM deployments WHERE id = $1`,
		deploymentID,
	).Scan(
		&d.ID, &d.ProjectID, &d.DeployNumber, &d.Status, &d.CommitSHA, &d.Branch,
		&d.ImageTag, &d.ContainerID, &d.SourceRevision, &d.BuildStrategy, &d.BuildCommand,
		&d.StartCommand, &d.RuntimeType, &d.InternalPort, &d.HealthStrategy,
		&d.ExecutionMode, &d.ImageDigest, &d.EnvConfigHash, &d.EnvSnapshot,
		&d.StartedAt, &d.BuiltAt, &d.DeployedAt, &d.FinishedAt, &d.DurationMs, &d.FailureReason, &d.CreatedAt,
		&d.DeploymentStrategy, &d.DeploymentPlan,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDeploymentNotFound
		}
		return nil, fmt.Errorf("failed to get deployment: %w", err)
	}

	svcDeploys, _ := s.ListServiceDeployments(ctx, d.ID)
	d.ServiceDeployments = svcDeploys

	return d, nil
}

// ListDeployments retrieves all deployments for a project with bounded pagination.
func (s *DeploymentService) ListDeployments(ctx context.Context, projectID uuid.UUID, pagination ...int) ([]*models.Deployment, error) {
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
		`SELECT id, project_id, deploy_number, status, commit_sha, branch,
		 image_tag, container_id, source_revision, build_strategy, build_command,
		 start_command, runtime_type, internal_port, health_strategy,
		 COALESCE(execution_mode, 'build'), image_digest, env_config_hash, env_snapshot,
		 started_at, built_at, deployed_at, finished_at, duration_ms, failure_reason, created_at,
		 COALESCE(deployment_strategy, 'dockerfile'), deployment_plan
		 FROM deployments WHERE project_id = $1 ORDER BY deploy_number DESC
		 LIMIT $2 OFFSET $3`,
		projectID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list deployments: %w", err)
	}
	defer rows.Close()

	var deployments []*models.Deployment
	for rows.Next() {
		d := &models.Deployment{}
		err := rows.Scan(
			&d.ID, &d.ProjectID, &d.DeployNumber, &d.Status, &d.CommitSHA, &d.Branch,
			&d.ImageTag, &d.ContainerID, &d.SourceRevision, &d.BuildStrategy, &d.BuildCommand,
			&d.StartCommand, &d.RuntimeType, &d.InternalPort, &d.HealthStrategy,
			&d.ExecutionMode, &d.ImageDigest, &d.EnvConfigHash, &d.EnvSnapshot,
			&d.StartedAt, &d.BuiltAt, &d.DeployedAt, &d.FinishedAt, &d.DurationMs, &d.FailureReason, &d.CreatedAt,
			&d.DeploymentStrategy, &d.DeploymentPlan,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan deployment: %w", err)
		}
		deployments = append(deployments, d)
	}

	if deployments == nil {
		deployments = []*models.Deployment{}
	}

	return deployments, nil
}

// ListActiveDeployments retrieves all deployments that are currently in-flight or actively running.
func (s *DeploymentService) ListActiveDeployments(ctx context.Context) ([]*models.Deployment, error) {
	if s.db == nil {
		return []*models.Deployment{}, nil
	}

	rows, err := s.db.Query(ctx,
		`SELECT id, project_id, deploy_number, status, commit_sha, branch,
		 image_tag, container_id, source_revision,
		 COALESCE(build_strategy, 'auto'), COALESCE(build_command, ''),
		 COALESCE(start_command, ''), COALESCE(runtime_type, 'generic'),
		 COALESCE(internal_port, 8080), COALESCE(health_strategy, 'auto'),
		 COALESCE(execution_mode, 'build'), image_digest, env_config_hash, env_snapshot,
		 started_at, built_at, deployed_at, finished_at, duration_ms, failure_reason, created_at,
		 COALESCE(deployment_strategy, 'dockerfile'), deployment_plan
		 FROM deployments WHERE status IN ('queued', 'running', 'cloning', 'building', 'starting', 'health_checking')
		 ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query active deployments: %w", err)
	}
	defer rows.Close()

	var deployments []*models.Deployment
	for rows.Next() {
		d := &models.Deployment{}
		err := rows.Scan(
			&d.ID, &d.ProjectID, &d.DeployNumber, &d.Status, &d.CommitSHA, &d.Branch,
			&d.ImageTag, &d.ContainerID, &d.SourceRevision, &d.BuildStrategy, &d.BuildCommand,
			&d.StartCommand, &d.RuntimeType, &d.InternalPort, &d.HealthStrategy,
			&d.ExecutionMode, &d.ImageDigest, &d.EnvConfigHash, &d.EnvSnapshot,
			&d.StartedAt, &d.BuiltAt, &d.DeployedAt, &d.FinishedAt, &d.DurationMs, &d.FailureReason, &d.CreatedAt,
			&d.DeploymentStrategy, &d.DeploymentPlan,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan active deployment: %w", err)
		}
		deployments = append(deployments, d)
	}

	if deployments == nil {
		deployments = []*models.Deployment{}
	}
	return deployments, rows.Err()
}

// ListActiveServiceDeployments retrieves all service deployments that are currently in-flight or actively running.
func (s *DeploymentService) ListActiveServiceDeployments(ctx context.Context) ([]*models.ServiceDeployment, error) {
	if s.db == nil {
		return []*models.ServiceDeployment{}, nil
	}

	rows, err := s.db.Query(ctx,
		`SELECT id, deployment_id, service_id, COALESCE(deploy_number, 1), status, container_id, host_port, COALESCE(internal_port, 8080), created_at
		 FROM service_deployments
		 WHERE status IN ('queued', 'cloning', 'building', 'starting', 'health_checking', 'running')`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query active service deployments: %w", err)
	}
	defer rows.Close()

	var sds []*models.ServiceDeployment
	for rows.Next() {
		sd := &models.ServiceDeployment{}
		err := rows.Scan(
			&sd.ID, &sd.DeploymentID, &sd.ServiceID, &sd.DeployNumber, &sd.Status,
			&sd.ContainerID, &sd.HostPort, &sd.InternalPort, &sd.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan active service deployment: %w", err)
		}
		sds = append(sds, sd)
	}

	if sds == nil {
		sds = []*models.ServiceDeployment{}
	}
	return sds, rows.Err()
}

// GetPreviousSuccessfulDeployment finds the most recent RUNNING or STOPPED deployment
// before the given deployment for rollback purposes.
func (s *DeploymentService) GetPreviousSuccessfulDeployment(ctx context.Context, projectID uuid.UUID, beforeDeployNumber int) (*models.Deployment, error) {
	d := &models.Deployment{}
	err := s.db.QueryRow(ctx,
		`SELECT id, project_id, deploy_number, status, commit_sha, branch,
		 image_tag, container_id, started_at, built_at, deployed_at,
		 finished_at, duration_ms, failure_reason, created_at,
		 COALESCE(execution_mode, 'build'), image_digest, env_config_hash, env_snapshot,
		 source_revision, build_strategy, build_command, start_command, runtime_type, internal_port, health_strategy
		 FROM deployments
		 WHERE project_id = $1
		   AND deploy_number < $2
		   AND status IN ($3, $4)
		   AND (image_digest IS NOT NULL OR image_tag IS NOT NULL)
		 ORDER BY deploy_number DESC
		 LIMIT 1`,
		projectID, beforeDeployNumber,
		models.DeployStatusRunning, models.DeployStatusStopped,
	).Scan(
		&d.ID, &d.ProjectID, &d.DeployNumber, &d.Status, &d.CommitSHA,
		&d.Branch, &d.ImageTag, &d.ContainerID, &d.StartedAt, &d.BuiltAt,
		&d.DeployedAt, &d.FinishedAt, &d.DurationMs, &d.FailureReason, &d.CreatedAt,
		&d.ExecutionMode, &d.ImageDigest, &d.EnvConfigHash, &d.EnvSnapshot,
		&d.SourceRevision, &d.BuildStrategy, &d.BuildCommand, &d.StartCommand, &d.RuntimeType, &d.InternalPort, &d.HealthStrategy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoDeploymentToRollback
		}
		return nil, fmt.Errorf("failed to find previous deployment: %w", err)
	}
	return d, nil
}

// RollbackServiceDeployment rolls back an individual service to its previous successful deployment release.
func (s *DeploymentService) RollbackServiceDeployment(ctx context.Context, project *models.Project, targetService *models.Service) (*models.ServiceDeployment, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Lock parent project row and check if project deletion is in progress
	var parentProjectStatus string
	err = tx.QueryRow(ctx, "SELECT status FROM projects WHERE id = $1 FOR UPDATE", project.ID).Scan(&parentProjectStatus)
	if err != nil {
		return nil, fmt.Errorf("failed to lock parent project row: %w", err)
	}
	if parentProjectStatus == models.ProjectStatusDeleting {
		return nil, ErrProjectDeleting
	}

	// Lock the target service row
	var lockedServiceID uuid.UUID
	err = tx.QueryRow(ctx, "SELECT id FROM services WHERE id = $1 FOR UPDATE", targetService.ID).Scan(&lockedServiceID)
	if err != nil {
		return nil, fmt.Errorf("failed to lock service row: %w", err)
	}

	// Verify no active deployments exist for this service
	var activeCount int
	err = tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM service_deployments 
		 WHERE service_id = $1 AND status IN ($2, $3, $4, $5, $6)`,
		targetService.ID,
		models.DeployStatusQueued,
		models.DeployStatusCloning,
		models.DeployStatusBuilding,
		models.DeployStatusStarting,
		models.DeployStatusHealthChecking,
	).Scan(&activeCount)
	if err != nil {
		return nil, fmt.Errorf("failed to check active service deployments: %w", err)
	}
	if activeCount > 0 {
		return nil, ErrActiveDeployment
	}

	// Determine the deployment number boundary to ensure we never select the current deployment
	var beforeDeployNumber int
	if targetService.CurrentServiceDeploymentID != nil {
		_ = tx.QueryRow(ctx, "SELECT deploy_number FROM service_deployments WHERE id = $1", *targetService.CurrentServiceDeploymentID).Scan(&beforeDeployNumber)
	}
	if beforeDeployNumber <= 0 {
		_ = tx.QueryRow(ctx, "SELECT COALESCE(MAX(deploy_number), 0) FROM service_deployments WHERE service_id = $1", targetService.ID).Scan(&beforeDeployNumber)
	}
	if beforeDeployNumber <= 1 {
		return nil, ErrNoDeploymentToRollback
	}

	// Find the most recent successful deployment for this service strictly before the current deployment
	var prev models.ServiceDeployment
	var prevDependsOnJSON, prevVolumesJSON, prevNetworksJSON, prevHcJSON, prevDocJSON []byte
	err = tx.QueryRow(ctx,
		`SELECT id, deploy_number, image_tag, internal_port, build_strategy, build_command,
		        start_command, runtime_type, COALESCE(dockerfile_path, ''),
		        COALESCE(build_context, '.'), COALESCE(health_strategy, 'auto'), health_check_path,
		        cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb,
		        image_digest, source_revision, env_config_hash, COALESCE(execution_mode, 'build'), env_snapshot,
		        COALESCE(classification, 'application'), COALESCE(image, ''),
		        COALESCE(depends_on, '[]'::jsonb), COALESCE(volumes, '[]'::jsonb),
		        COALESCE(networks, '[]'::jsonb), COALESCE(healthcheck_config, '{}'::jsonb),
		        COALESCE(depends_on_conditions, '{}'::jsonb)
		 FROM service_deployments
		 WHERE service_id = $1
		   AND deploy_number < $2
		   AND status IN ($3, $4)
		   AND (image_digest IS NOT NULL OR image_tag IS NOT NULL)
		   AND ($5::uuid IS NULL OR id != $5)
		 ORDER BY deploy_number DESC
		 LIMIT 1`,
		targetService.ID,
		beforeDeployNumber,
		models.DeployStatusRunning,
		models.DeployStatusStopped,
		targetService.CurrentServiceDeploymentID,
	).Scan(
		&prev.ID, &prev.DeployNumber, &prev.ImageTag, &prev.InternalPort,
		&prev.BuildStrategy, &prev.BuildCommand, &prev.StartCommand, &prev.RuntimeType,
		&prev.DockerfilePath, &prev.BuildContext, &prev.HealthStrategy, &prev.HealthCheckPath,
		&prev.CpuMillicores, &prev.MemoryMB, &prev.PidsLimit, &prev.EphemeralStorageMB,
		&prev.ImageDigest, &prev.SourceRevision, &prev.EnvConfigHash, &prev.ExecutionMode, &prev.EnvSnapshot,
		&prev.Classification, &prev.Image, &prevDependsOnJSON, &prevVolumesJSON, &prevNetworksJSON, &prevHcJSON, &prevDocJSON,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoDeploymentToRollback
		}
		return nil, fmt.Errorf("failed to find previous service deployment: %w", err)
	}

	if len(prevDependsOnJSON) > 0 {
		_ = json.Unmarshal(prevDependsOnJSON, &prev.DependsOn)
	}
	if prev.DependsOn == nil {
		prev.DependsOn = []string{}
	}
	if len(prevVolumesJSON) > 0 {
		_ = json.Unmarshal(prevVolumesJSON, &prev.Volumes)
	}
	if prev.Volumes == nil {
		prev.Volumes = []models.VolumeMountConfig{}
	}
	if len(prevNetworksJSON) > 0 {
		_ = json.Unmarshal(prevNetworksJSON, &prev.Networks)
	}
	if prev.Networks == nil {
		prev.Networks = []string{}
	}
	if len(prevHcJSON) > 0 && string(prevHcJSON) != "{}" {
		var hc models.HealthCheckConfig
		if err := json.Unmarshal(prevHcJSON, &hc); err == nil {
			prev.HealthCheckConfig = &hc
		}
	}
	if len(prevDocJSON) > 0 && string(prevDocJSON) != "{}" {
		_ = json.Unmarshal(prevDocJSON, &prev.DependsOnConditions)
	}
	if prev.DependsOnConditions == nil {
		prev.DependsOnConditions = map[string]string{}
	}

	var maxNumber *int
	_ = tx.QueryRow(ctx, "SELECT MAX(deploy_number) FROM service_deployments WHERE service_id = $1", targetService.ID).Scan(&maxNumber)
	deployNumber := 1
	if maxNumber != nil {
		deployNumber = *maxNumber + 1
	}

	// Execution mode: reuse existing immutable image; fail closed if unavailable
	if (prev.ImageDigest == nil || *prev.ImageDigest == "") && (prev.ImageTag == nil || *prev.ImageTag == "") {
		return nil, errors.New("cannot rollback: prior deployment has no immutable image or digest to reuse")
	}
	executionMode := models.ExecutionModeReuseImage

	now := time.Now()
	svcDeployID := uuid.New()
	serviceDeployment := &models.ServiceDeployment{
		ID:                  svcDeployID,
		DeploymentID:        nil,
		ServiceID:           targetService.ID,
		ServiceName:         targetService.Name,
		DeployNumber:        deployNumber,
		Status:              models.DeployStatusQueued,
		ExecutionMode:       executionMode,
		ImageTag:            prev.ImageTag,
		InternalPort:        prev.InternalPort,
		BuildStrategy:       prev.BuildStrategy,
		BuildCommand:        prev.BuildCommand,
		StartCommand:        prev.StartCommand,
		RuntimeType:         prev.RuntimeType,
		DockerfilePath:      prev.DockerfilePath,
		BuildContext:        prev.BuildContext,
		HealthStrategy:      prev.HealthStrategy,
		HealthCheckPath:     prev.HealthCheckPath,
		ResourceConfig:      prev.ResourceConfig,
		ImageDigest:         prev.ImageDigest,
		SourceRevision:      prev.SourceRevision,
		EnvConfigHash:       prev.EnvConfigHash,
		EnvSnapshot:         prev.EnvSnapshot,
		Classification:      prev.Classification,
		Image:               prev.Image,
		DependsOn:           prev.DependsOn,
		DependsOnConditions: prev.DependsOnConditions,
		Volumes:             prev.Volumes,
		Networks:            prev.Networks,
		HealthCheckConfig:   prev.HealthCheckConfig,
		StartedAt:           &now,
		CreatedAt:           now,
	}

	dependsOnBytes, _ := json.Marshal(serviceDeployment.DependsOn)
	volumesBytes, _ := json.Marshal(serviceDeployment.Volumes)
	networksBytes, _ := json.Marshal(serviceDeployment.Networks)
	var hcBytes []byte
	if serviceDeployment.HealthCheckConfig != nil {
		hcBytes, _ = json.Marshal(serviceDeployment.HealthCheckConfig)
	} else {
		hcBytes = []byte("{}")
	}
	dependsOnCondBytes, _ := json.Marshal(serviceDeployment.DependsOnConditions)

	_, err = tx.Exec(ctx,
		`INSERT INTO service_deployments (
			id, deployment_id, service_id, deploy_number, status, image_tag,
			build_strategy, build_command, start_command, runtime_type,
			dockerfile_path, build_context, internal_port, health_strategy, health_check_path,
			cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb,
			image_digest, source_revision, env_config_hash, execution_mode, env_snapshot,
			started_at, created_at,
			classification, image, depends_on, volumes, networks, healthcheck_config,
			depends_on_conditions
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33)`,
		serviceDeployment.ID, serviceDeployment.DeploymentID, serviceDeployment.ServiceID,
		serviceDeployment.DeployNumber, serviceDeployment.Status, serviceDeployment.ImageTag,
		serviceDeployment.BuildStrategy, serviceDeployment.BuildCommand, serviceDeployment.StartCommand,
		serviceDeployment.RuntimeType, serviceDeployment.DockerfilePath, serviceDeployment.BuildContext,
		serviceDeployment.InternalPort, serviceDeployment.HealthStrategy, serviceDeployment.HealthCheckPath,
		serviceDeployment.CpuMillicores, serviceDeployment.MemoryMB, serviceDeployment.PidsLimit, serviceDeployment.EphemeralStorageMB,
		serviceDeployment.ImageDigest, serviceDeployment.SourceRevision, serviceDeployment.EnvConfigHash,
		serviceDeployment.ExecutionMode, serviceDeployment.EnvSnapshot,
		serviceDeployment.StartedAt, serviceDeployment.CreatedAt,
		serviceDeployment.Classification, serviceDeployment.Image, dependsOnBytes, volumesBytes, networksBytes, hcBytes,
		dependsOnCondBytes,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to insert rollback service deployment: %w", err)
	}

	// Set service to deploying but do NOT update current_service_deployment_id.
	// current_service_deployment_id must remain preserved until the rollback deployment is promoted.
	_, err = tx.Exec(ctx,
		"UPDATE services SET status = $1, updated_at = $2 WHERE id = $3",
		models.ServiceStatusDeploying, now, targetService.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update service status for rollback: %w", err)
	}

	allServices, _ := s.listServicesTx(ctx, tx, project.ID)
	newProjectStatus := CalculateProjectStatus(allServices)
	_, _ = tx.Exec(ctx, "UPDATE projects SET status = $1, updated_at = $2 WHERE id = $3", newProjectStatus, now, project.ID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit service rollback: %w", err)
	}

	imgRef := "none"
	if prev.ImageDigest != nil {
		imgRef = *prev.ImageDigest
	} else if prev.ImageTag != nil {
		imgRef = *prev.ImageTag
	}

	slog.Info("service rollback deployment created",
		"service_deployment_id", serviceDeployment.ID,
		"project_id", project.ID,
		"service_id", targetService.ID,
		"deploy_number", deployNumber,
		"execution_mode", executionMode,
		"reused_image_ref", imgRef,
	)

	return serviceDeployment, nil
}

// RollbackDeployment rolls back an entire project release to its previous successful deployment release.
func (s *DeploymentService) RollbackDeployment(ctx context.Context, project *models.Project) (*models.Deployment, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Lock the project row FOR UPDATE
	var lockedProjectID uuid.UUID
	var projectStatus string
	err = tx.QueryRow(ctx, "SELECT id, status FROM projects WHERE id = $1 FOR UPDATE", project.ID).Scan(&lockedProjectID, &projectStatus)
	if err != nil {
		return nil, fmt.Errorf("failed to lock project row: %w", err)
	}
	if projectStatus == models.ProjectStatusDeleting {
		return nil, ErrProjectDeleting
	}

	// Verify no active deployments exist for this project
	var activeCount int
	err = tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM deployments 
		 WHERE project_id = $1 AND status IN ($2, $3, $4, $5, $6)`,
		project.ID,
		models.DeployStatusQueued,
		models.DeployStatusCloning,
		models.DeployStatusBuilding,
		models.DeployStatusStarting,
		models.DeployStatusHealthChecking,
	).Scan(&activeCount)
	if err != nil {
		return nil, fmt.Errorf("failed to check active deployments: %w", err)
	}
	if activeCount > 0 {
		return nil, ErrActiveDeployment
	}

	// Determine the deployment number boundary strictly before the current deployment
	var beforeDeployNumber int
	if project.CurrentDeploymentID != nil {
		_ = tx.QueryRow(ctx, "SELECT deploy_number FROM deployments WHERE id = $1", *project.CurrentDeploymentID).Scan(&beforeDeployNumber)
	}
	if beforeDeployNumber <= 0 {
		_ = tx.QueryRow(ctx, "SELECT COALESCE(MAX(deploy_number), 0) FROM deployments WHERE project_id = $1", project.ID).Scan(&beforeDeployNumber)
	}
	if beforeDeployNumber <= 1 {
		return nil, ErrNoDeploymentToRollback
	}

	// Find the most recent successful deployment for this project strictly before beforeDeployNumber
	var prev models.Deployment
	err = tx.QueryRow(ctx,
		`SELECT id, project_id, deploy_number, status, commit_sha, branch,
		        image_tag, container_id, started_at, built_at, deployed_at,
		        finished_at, duration_ms, failure_reason, created_at,
		        COALESCE(execution_mode, 'build'), image_digest, env_config_hash, env_snapshot,
		        source_revision, build_strategy, build_command, start_command, runtime_type, internal_port, health_strategy,
		        COALESCE(deployment_strategy, 'dockerfile'), deployment_plan
		 FROM deployments
		 WHERE project_id = $1
		   AND deploy_number < $2
		   AND status IN ($3, $4)
		   AND (image_digest IS NOT NULL OR image_tag IS NOT NULL)
		   AND ($5::uuid IS NULL OR id != $5)
		 ORDER BY deploy_number DESC
		 LIMIT 1`,
		project.ID,
		beforeDeployNumber,
		models.DeployStatusRunning,
		models.DeployStatusStopped,
		project.CurrentDeploymentID,
	).Scan(
		&prev.ID, &prev.ProjectID, &prev.DeployNumber, &prev.Status, &prev.CommitSHA,
		&prev.Branch, &prev.ImageTag, &prev.ContainerID, &prev.StartedAt, &prev.BuiltAt,
		&prev.DeployedAt, &prev.FinishedAt, &prev.DurationMs, &prev.FailureReason, &prev.CreatedAt,
		&prev.ExecutionMode, &prev.ImageDigest, &prev.EnvConfigHash, &prev.EnvSnapshot,
		&prev.SourceRevision, &prev.BuildStrategy, &prev.BuildCommand, &prev.StartCommand, &prev.RuntimeType, &prev.InternalPort, &prev.HealthStrategy,
		&prev.DeploymentStrategy, &prev.DeploymentPlan,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoDeploymentToRollback
		}
		return nil, fmt.Errorf("failed to find previous deployment: %w", err)
	}

	// Fetch any services for the project
	rows, err := tx.Query(ctx,
		`SELECT id, name, role, build_strategy, build_command, start_command, runtime_type, internal_port,
		        COALESCE(dockerfile_path, ''), COALESCE(build_context, '.'),
		        COALESCE(health_strategy, 'auto'), health_check_path,
		        cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb
		 FROM services WHERE project_id = $1`,
		project.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query services for rollback: %w", err)
	}
	defer rows.Close()

	var svcList []models.Service
	for rows.Next() {
		var s models.Service
		if err := rows.Scan(
			&s.ID, &s.Name, &s.Role, &s.BuildStrategy, &s.BuildCommand, &s.StartCommand, &s.RuntimeType,
			&s.InternalPort, &s.DockerfilePath, &s.BuildContext, &s.HealthStrategy, &s.HealthCheckPath,
			&s.CpuMillicores, &s.MemoryMB, &s.PidsLimit, &s.EphemeralStorageMB,
		); err != nil {
			return nil, fmt.Errorf("failed to scan service for rollback: %w", err)
		}
		svcList = append(svcList, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating services for rollback: %w", err)
	}

	// In single-container legacy mode, require immutable image or tag
	if len(svcList) == 0 {
		if (prev.ImageDigest == nil || *prev.ImageDigest == "") && (prev.ImageTag == nil || *prev.ImageTag == "") {
			return nil, errors.New("cannot rollback: prior deployment has no immutable image or digest to reuse")
		}
	}

	var maxNumber *int
	_ = tx.QueryRow(ctx, "SELECT MAX(deploy_number) FROM deployments WHERE project_id = $1", project.ID).Scan(&maxNumber)
	deployNumber := 1
	if maxNumber != nil {
		deployNumber = *maxNumber + 1
	}

	now := time.Now()
	newDeployID := uuid.New()
	newDeploy := &models.Deployment{
		ID:                 newDeployID,
		ProjectID:          project.ID,
		DeployNumber:       deployNumber,
		Status:             models.DeployStatusQueued,
		Branch:             prev.Branch,
		ExecutionMode:      models.ExecutionModeReuseImage,
		ImageDigest:        prev.ImageDigest,
		ImageTag:           prev.ImageTag,
		SourceRevision:     prev.SourceRevision,
		EnvConfigHash:      prev.EnvConfigHash,
		EnvSnapshot:        prev.EnvSnapshot,
		BuildStrategy:      prev.BuildStrategy,
		BuildCommand:       prev.BuildCommand,
		StartCommand:       prev.StartCommand,
		RuntimeType:        prev.RuntimeType,
		InternalPort:       prev.InternalPort,
		HealthStrategy:     prev.HealthStrategy,
		DeploymentStrategy: prev.DeploymentStrategy,
		DeploymentPlan:     prev.DeploymentPlan,
		StartedAt:          &now,
		CreatedAt:          now,
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO deployments (
			id, project_id, deploy_number, status, branch, image_tag,
			build_strategy, build_command, start_command, runtime_type, internal_port, health_strategy,
			execution_mode, image_digest, env_config_hash, env_snapshot, source_revision,
			started_at, created_at,
			deployment_strategy, deployment_plan
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)`,
		newDeploy.ID, newDeploy.ProjectID, newDeploy.DeployNumber,
		newDeploy.Status, newDeploy.Branch, newDeploy.ImageTag,
		newDeploy.BuildStrategy, newDeploy.BuildCommand, newDeploy.StartCommand,
		newDeploy.RuntimeType, newDeploy.InternalPort, newDeploy.HealthStrategy,
		newDeploy.ExecutionMode, newDeploy.ImageDigest, newDeploy.EnvConfigHash, newDeploy.EnvSnapshot, newDeploy.SourceRevision,
		newDeploy.StartedAt, newDeploy.CreatedAt,
		newDeploy.DeploymentStrategy, newDeploy.DeploymentPlan,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to insert rollback deployment: %w", err)
	}

	// For multi-service projects, create rollback service deployment for each service
	for _, svcItem := range svcList {
		var prevSvc models.ServiceDeployment
		var prevDependsOnJSON, prevVolumesJSON, prevNetworksJSON, prevHcJSON, prevDocJSON []byte
		sErr := tx.QueryRow(ctx,
			`SELECT id, deploy_number, image_tag, internal_port, build_strategy, build_command,
			        start_command, runtime_type, COALESCE(dockerfile_path, ''),
			        COALESCE(build_context, '.'), COALESCE(health_strategy, 'auto'), health_check_path,
			        cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb,
			        image_digest, source_revision, env_config_hash, COALESCE(execution_mode, 'build'), env_snapshot,
			        COALESCE(classification, 'application'), COALESCE(image, ''),
			        COALESCE(depends_on, '[]'::jsonb), COALESCE(volumes, '[]'::jsonb),
			        COALESCE(networks, '[]'::jsonb), COALESCE(healthcheck_config, '{}'::jsonb),
			        COALESCE(depends_on_conditions, '{}'::jsonb)
			 FROM service_deployments
			 WHERE service_id = $1
			   AND (deployment_id = $2 OR (status IN ($3, $4) AND (image_digest IS NOT NULL OR image_tag IS NOT NULL)))
			 ORDER BY deploy_number DESC
			 LIMIT 1`,
			svcItem.ID, prev.ID, models.DeployStatusRunning, models.DeployStatusStopped,
		).Scan(
			&prevSvc.ID, &prevSvc.DeployNumber, &prevSvc.ImageTag, &prevSvc.InternalPort,
			&prevSvc.BuildStrategy, &prevSvc.BuildCommand, &prevSvc.StartCommand, &prevSvc.RuntimeType,
			&prevSvc.DockerfilePath, &prevSvc.BuildContext, &prevSvc.HealthStrategy, &prevSvc.HealthCheckPath,
			&prevSvc.CpuMillicores, &prevSvc.MemoryMB, &prevSvc.PidsLimit, &prevSvc.EphemeralStorageMB,
			&prevSvc.ImageDigest, &prevSvc.SourceRevision, &prevSvc.EnvConfigHash, &prevSvc.ExecutionMode, &prevSvc.EnvSnapshot,
			&prevSvc.Classification, &prevSvc.Image, &prevDependsOnJSON, &prevVolumesJSON, &prevNetworksJSON, &prevHcJSON, &prevDocJSON,
		)

		if len(prevDependsOnJSON) > 0 {
			_ = json.Unmarshal(prevDependsOnJSON, &prevSvc.DependsOn)
		}
		if prevSvc.DependsOn == nil {
			prevSvc.DependsOn = []string{}
		}
		if len(prevVolumesJSON) > 0 {
			_ = json.Unmarshal(prevVolumesJSON, &prevSvc.Volumes)
		}
		if prevSvc.Volumes == nil {
			prevSvc.Volumes = []models.VolumeMountConfig{}
		}
		if len(prevNetworksJSON) > 0 {
			_ = json.Unmarshal(prevNetworksJSON, &prevSvc.Networks)
		}
		if prevSvc.Networks == nil {
			prevSvc.Networks = []string{}
		}
		if len(prevHcJSON) > 0 && string(prevHcJSON) != "{}" {
			var hc models.HealthCheckConfig
			if err := json.Unmarshal(prevHcJSON, &hc); err == nil {
				prevSvc.HealthCheckConfig = &hc
			}
		}
		if len(prevDocJSON) > 0 && string(prevDocJSON) != "{}" {
			_ = json.Unmarshal(prevDocJSON, &prevSvc.DependsOnConditions)
		}
		if prevSvc.DependsOnConditions == nil {
			prevSvc.DependsOnConditions = map[string]string{}
		}

		var svcMaxNum *int
		_ = tx.QueryRow(ctx, "SELECT MAX(deploy_number) FROM service_deployments WHERE service_id = $1", svcItem.ID).Scan(&svcMaxNum)
		svcDeployNum := 1
		if svcMaxNum != nil {
			svcDeployNum = *svcMaxNum + 1
		}

		svcDeployID := uuid.New()
		sd := &models.ServiceDeployment{
			ID:                  svcDeployID,
			DeploymentID:        &newDeploy.ID,
			ServiceID:           svcItem.ID,
			ServiceName:         svcItem.Name,
			DeployNumber:        svcDeployNum,
			Status:              models.DeployStatusQueued,
			ExecutionMode:       models.ExecutionModeReuseImage,
			ImageTag:            prevSvc.ImageTag,
			ImageDigest:         prevSvc.ImageDigest,
			InternalPort:        svcItem.InternalPort,
			BuildStrategy:       svcItem.BuildStrategy,
			BuildCommand:        svcItem.BuildCommand,
			StartCommand:        svcItem.StartCommand,
			RuntimeType:         svcItem.RuntimeType,
			DockerfilePath:      svcItem.DockerfilePath,
			BuildContext:        svcItem.BuildContext,
			HealthStrategy:      svcItem.HealthStrategy,
			HealthCheckPath:     svcItem.HealthCheckPath,
			ResourceConfig:      svcItem.ResourceConfig,
			SourceRevision:      prevSvc.SourceRevision,
			EnvConfigHash:       prevSvc.EnvConfigHash,
			EnvSnapshot:         prevSvc.EnvSnapshot,
			Classification:      prevSvc.Classification,
			Image:               prevSvc.Image,
			DependsOn:           prevSvc.DependsOn,
			DependsOnConditions: prevSvc.DependsOnConditions,
			Volumes:             prevSvc.Volumes,
			Networks:            prevSvc.Networks,
			HealthCheckConfig:   prevSvc.HealthCheckConfig,
			StartedAt:           &now,
			CreatedAt:           now,
		}
		if sErr == nil && (prevSvc.ImageDigest != nil || prevSvc.ImageTag != nil) {
			sd.InternalPort = prevSvc.InternalPort
			sd.BuildStrategy = prevSvc.BuildStrategy
			sd.BuildCommand = prevSvc.BuildCommand
			sd.StartCommand = prevSvc.StartCommand
			sd.RuntimeType = prevSvc.RuntimeType
			sd.DockerfilePath = prevSvc.DockerfilePath
			sd.BuildContext = prevSvc.BuildContext
			sd.HealthStrategy = prevSvc.HealthStrategy
			sd.HealthCheckPath = prevSvc.HealthCheckPath
			sd.ResourceConfig = prevSvc.ResourceConfig
		}

		dependsOnBytes, _ := json.Marshal(sd.DependsOn)
		volumesBytes, _ := json.Marshal(sd.Volumes)
		networksBytes, _ := json.Marshal(sd.Networks)
		var hcBytes []byte
		if sd.HealthCheckConfig != nil {
			hcBytes, _ = json.Marshal(sd.HealthCheckConfig)
		} else {
			hcBytes = []byte("{}")
		}
		dependsOnCondBytes, _ := json.Marshal(sd.DependsOnConditions)

		if _, insErr := tx.Exec(ctx,
			`INSERT INTO service_deployments (
				id, deployment_id, service_id, deploy_number, status, image_tag,
				build_strategy, build_command, start_command, runtime_type,
				dockerfile_path, build_context, internal_port, health_strategy, health_check_path,
				cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb,
				image_digest, source_revision, env_config_hash, execution_mode, env_snapshot,
				started_at, created_at,
				classification, image, depends_on, volumes, networks, healthcheck_config,
				depends_on_conditions
			 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33)`,
			sd.ID, sd.DeploymentID, sd.ServiceID, sd.DeployNumber, sd.Status, sd.ImageTag,
			sd.BuildStrategy, sd.BuildCommand, sd.StartCommand, sd.RuntimeType,
			sd.DockerfilePath, sd.BuildContext, sd.InternalPort, sd.HealthStrategy, sd.HealthCheckPath,
			sd.CpuMillicores, sd.MemoryMB, sd.PidsLimit, sd.EphemeralStorageMB,
			sd.ImageDigest, sd.SourceRevision, sd.EnvConfigHash, sd.ExecutionMode, sd.EnvSnapshot,
			sd.StartedAt, sd.CreatedAt,
			sd.Classification, sd.Image, dependsOnBytes, volumesBytes, networksBytes, hcBytes,
			dependsOnCondBytes,
		); insErr != nil {
			return nil, fmt.Errorf("failed to insert rollback service deployment for service %s: %w", svcItem.Name, insErr)
		}

		_, _ = tx.Exec(ctx, "UPDATE services SET status = $1, updated_at = $2 WHERE id = $3", models.ServiceStatusDeploying, now, svcItem.ID)
		newDeploy.ServiceDeployments = append(newDeploy.ServiceDeployments, sd)
	}

	_, err = tx.Exec(ctx,
		"UPDATE projects SET status = $1, updated_at = $2 WHERE id = $3",
		models.ProjectStatusDeploying, now, project.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update project status for rollback: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit project rollback: %w", err)
	}

	slog.Info("project rollback deployment created",
		"deployment_id", newDeploy.ID,
		"project_id", project.ID,
		"deploy_number", newDeploy.DeployNumber,
		"execution_mode", newDeploy.ExecutionMode,
	)

	return newDeploy, nil
}

// GetServiceDeployment retrieves an individual service deployment record by ID.
func (s *DeploymentService) GetServiceDeployment(ctx context.Context, serviceDeploymentID uuid.UUID) (*models.ServiceDeployment, error) {
	sd := &models.ServiceDeployment{}
	var publicExposed bool
	var dependsOnJSON, volumesJSON, networksJSON, hcJSON, docJSON []byte
	err := s.db.QueryRow(ctx,
		`SELECT sd.id, sd.deployment_id, sd.service_id, s.name, s.public_exposed, sd.deploy_number,
		        sd.status, sd.image_tag, sd.container_id, sd.host_port, sd.internal_port,
		        sd.build_strategy, sd.build_command, sd.start_command, sd.runtime_type,
		        COALESCE(sd.dockerfile_path, ''), COALESCE(sd.build_context, '.'),
		        COALESCE(sd.health_strategy, 'auto'), sd.health_check_path,
		        sd.cpu_millicores, sd.memory_mb, sd.pids_limit, sd.ephemeral_storage_mb,
		        sd.image_digest, sd.source_revision, sd.env_config_hash,
		        COALESCE(sd.execution_mode, 'build'), sd.env_snapshot,
		        sd.started_at, sd.built_at, sd.deployed_at, sd.finished_at,
		        sd.duration_ms, sd.failure_reason, sd.created_at,
		        COALESCE(sd.classification, 'application'), COALESCE(sd.image, ''),
		        COALESCE(sd.depends_on, '[]'::jsonb), COALESCE(sd.volumes, '[]'::jsonb),
		        COALESCE(sd.networks, '[]'::jsonb), COALESCE(sd.healthcheck_config, '{}'::jsonb),
		        COALESCE(sd.depends_on_conditions, '{}'::jsonb)
		 FROM service_deployments sd
		 JOIN services s ON s.id = sd.service_id
		 WHERE sd.id = $1`,
		serviceDeploymentID,
	).Scan(
		&sd.ID, &sd.DeploymentID, &sd.ServiceID, &sd.ServiceName, &publicExposed, &sd.DeployNumber,
		&sd.Status, &sd.ImageTag, &sd.ContainerID, &sd.HostPort, &sd.InternalPort,
		&sd.BuildStrategy, &sd.BuildCommand, &sd.StartCommand, &sd.RuntimeType,
		&sd.DockerfilePath, &sd.BuildContext, &sd.HealthStrategy, &sd.HealthCheckPath,
		&sd.CpuMillicores, &sd.MemoryMB, &sd.PidsLimit, &sd.EphemeralStorageMB,
		&sd.ImageDigest, &sd.SourceRevision, &sd.EnvConfigHash,
		&sd.ExecutionMode, &sd.EnvSnapshot,
		&sd.StartedAt, &sd.BuiltAt, &sd.DeployedAt, &sd.FinishedAt,
		&sd.DurationMs, &sd.FailureReason, &sd.CreatedAt,
		&sd.Classification, &sd.Image, &dependsOnJSON, &volumesJSON, &networksJSON, &hcJSON, &docJSON,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDeploymentNotFound
		}
		return nil, fmt.Errorf("failed to get service deployment: %w", err)
	}

	if len(dependsOnJSON) > 0 {
		if err := json.Unmarshal(dependsOnJSON, &sd.DependsOn); err != nil {
			return nil, fmt.Errorf("failed to decode depends_on: %w", err)
		}
	}
	if sd.DependsOn == nil {
		sd.DependsOn = []string{}
	}
	if len(volumesJSON) > 0 {
		if err := json.Unmarshal(volumesJSON, &sd.Volumes); err != nil {
			return nil, fmt.Errorf("failed to decode volumes: %w", err)
		}
	}
	if sd.Volumes == nil {
		sd.Volumes = []models.VolumeMountConfig{}
	}
	if len(networksJSON) > 0 {
		if err := json.Unmarshal(networksJSON, &sd.Networks); err != nil {
			return nil, fmt.Errorf("failed to decode networks: %w", err)
		}
	}
	if sd.Networks == nil {
		sd.Networks = []string{}
	}
	if len(hcJSON) > 0 && string(hcJSON) != "{}" {
		var hc models.HealthCheckConfig
		if err := json.Unmarshal(hcJSON, &hc); err != nil {
			return nil, fmt.Errorf("failed to decode healthcheck_config: %w", err)
		}
		sd.HealthCheckConfig = &hc
	}
	if len(docJSON) > 0 && string(docJSON) != "{}" {
		if err := json.Unmarshal(docJSON, &sd.DependsOnConditions); err != nil {
			return nil, fmt.Errorf("failed to decode depends_on_conditions: %w", err)
		}
	}
	if sd.DependsOnConditions == nil {
		sd.DependsOnConditions = map[string]string{}
	}

	if publicExposed && sd.HostPort != nil && *sd.HostPort > 0 && sd.Status == models.DeployStatusRunning {
		url := fmt.Sprintf("http://%s:%d", resolvePublicHost(), *sd.HostPort)
		sd.PreviewURL = &url
	} else {
		sd.PreviewURL = nil
	}

	return sd, nil
} // ListServiceDeploymentsByService retrieves all service deployments for a specific service ordered by deploy number.
func (s *DeploymentService) ListServiceDeploymentsByService(ctx context.Context, serviceID uuid.UUID) ([]*models.ServiceDeployment, error) {
	rows, err := s.db.Query(ctx,
		`SELECT sd.id, sd.deployment_id, sd.service_id, s.name, s.public_exposed, sd.deploy_number,
		        sd.status, sd.image_tag, sd.container_id, sd.host_port, sd.internal_port,
		        sd.build_strategy, sd.build_command, sd.start_command, sd.runtime_type,
		        COALESCE(sd.dockerfile_path, ''), COALESCE(sd.build_context, '.'),
		        COALESCE(sd.health_strategy, 'auto'), sd.health_check_path,
		        sd.cpu_millicores, sd.memory_mb, sd.pids_limit, sd.ephemeral_storage_mb,
		        sd.image_digest, sd.source_revision, sd.env_config_hash,
		        COALESCE(sd.execution_mode, 'build'), sd.env_snapshot,
		        sd.started_at, sd.built_at, sd.deployed_at, sd.finished_at,
		        sd.duration_ms, sd.failure_reason, sd.created_at,
		        COALESCE(sd.classification, 'application'), COALESCE(sd.image, ''),
		        COALESCE(sd.depends_on, '[]'::jsonb), COALESCE(sd.volumes, '[]'::jsonb),
		        COALESCE(sd.networks, '[]'::jsonb), COALESCE(sd.healthcheck_config, '{}'::jsonb),
		        COALESCE(sd.depends_on_conditions, '{}'::jsonb)
		 FROM service_deployments sd
		 JOIN services s ON s.id = sd.service_id
		 WHERE sd.service_id = $1
		 ORDER BY sd.deploy_number DESC`,
		serviceID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list service deployments: %w", err)
	}
	defer rows.Close()

	publicHost := resolvePublicHost()
	var list []*models.ServiceDeployment
	for rows.Next() {
		sd := &models.ServiceDeployment{}
		var publicExposed bool
		var dependsOnJSON, volumesJSON, networksJSON, hcJSON, docJSON []byte
		err := rows.Scan(
			&sd.ID, &sd.DeploymentID, &sd.ServiceID, &sd.ServiceName, &publicExposed, &sd.DeployNumber,
			&sd.Status, &sd.ImageTag, &sd.ContainerID, &sd.HostPort, &sd.InternalPort,
			&sd.BuildStrategy, &sd.BuildCommand, &sd.StartCommand, &sd.RuntimeType,
			&sd.DockerfilePath, &sd.BuildContext, &sd.HealthStrategy, &sd.HealthCheckPath,
			&sd.CpuMillicores, &sd.MemoryMB, &sd.PidsLimit, &sd.EphemeralStorageMB,
			&sd.ImageDigest, &sd.SourceRevision, &sd.EnvConfigHash,
			&sd.ExecutionMode, &sd.EnvSnapshot,
			&sd.StartedAt, &sd.BuiltAt, &sd.DeployedAt, &sd.FinishedAt,
			&sd.DurationMs, &sd.FailureReason, &sd.CreatedAt,
			&sd.Classification, &sd.Image, &dependsOnJSON, &volumesJSON, &networksJSON, &hcJSON, &docJSON,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan service deployment: %w", err)
		}
		if len(dependsOnJSON) > 0 {
			if err := json.Unmarshal(dependsOnJSON, &sd.DependsOn); err != nil {
				return nil, fmt.Errorf("failed to decode depends_on for service deployment %s: %w", sd.ID, err)
			}
		}
		if sd.DependsOn == nil {
			sd.DependsOn = []string{}
		}
		if len(volumesJSON) > 0 {
			if err := json.Unmarshal(volumesJSON, &sd.Volumes); err != nil {
				return nil, fmt.Errorf("failed to decode volumes for service deployment %s: %w", sd.ID, err)
			}
		}
		if sd.Volumes == nil {
			sd.Volumes = []models.VolumeMountConfig{}
		}
		if len(networksJSON) > 0 {
			if err := json.Unmarshal(networksJSON, &sd.Networks); err != nil {
				return nil, fmt.Errorf("failed to decode networks for service deployment %s: %w", sd.ID, err)
			}
		}
		if sd.Networks == nil {
			sd.Networks = []string{}
		}
		if len(hcJSON) > 0 && string(hcJSON) != "{}" {
			var hc models.HealthCheckConfig
			if err := json.Unmarshal(hcJSON, &hc); err != nil {
				return nil, fmt.Errorf("failed to decode healthcheck_config for service deployment %s: %w", sd.ID, err)
			}
			sd.HealthCheckConfig = &hc
		}
		if len(docJSON) > 0 && string(docJSON) != "{}" {
			if err := json.Unmarshal(docJSON, &sd.DependsOnConditions); err != nil {
				return nil, fmt.Errorf("failed to decode depends_on_conditions for service deployment %s: %w", sd.ID, err)
			}
		}
		if sd.DependsOnConditions == nil {
			sd.DependsOnConditions = map[string]string{}
		}
		if publicExposed && sd.HostPort != nil && *sd.HostPort > 0 && sd.Status == models.DeployStatusRunning {
			url := fmt.Sprintf("http://%s:%d", publicHost, *sd.HostPort)
			sd.PreviewURL = &url
		} else {
			sd.PreviewURL = nil
		}
		list = append(list, sd)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating service deployments: %w", err)
	}

	if list == nil {
		list = []*models.ServiceDeployment{}
	}

	return list, nil
}

// ListServiceDeployments retrieves all service deployment records for a release.
func (s *DeploymentService) ListServiceDeployments(ctx context.Context, deploymentID uuid.UUID) ([]*models.ServiceDeployment, error) {
	rows, err := s.db.Query(ctx,
		`SELECT sd.id, sd.deployment_id, sd.service_id, s.name, s.public_exposed, sd.deploy_number,
		        sd.status, sd.image_tag, sd.container_id, sd.host_port, sd.internal_port,
		        sd.build_strategy, sd.build_command, sd.start_command, sd.runtime_type,
		        COALESCE(sd.dockerfile_path, ''), COALESCE(sd.build_context, '.'),
		        COALESCE(sd.health_strategy, 'auto'), sd.health_check_path,
		        sd.cpu_millicores, sd.memory_mb, sd.pids_limit, sd.ephemeral_storage_mb,
		        sd.image_digest, sd.source_revision, sd.env_config_hash,
		        COALESCE(sd.execution_mode, 'build'), sd.env_snapshot,
		        sd.started_at, sd.built_at, sd.deployed_at, sd.finished_at,
		        sd.duration_ms, sd.failure_reason, sd.created_at,
		        COALESCE(sd.classification, 'application'), COALESCE(sd.image, ''),
		        COALESCE(sd.depends_on, '[]'::jsonb), COALESCE(sd.volumes, '[]'::jsonb),
		        COALESCE(sd.networks, '[]'::jsonb), COALESCE(sd.healthcheck_config, '{}'::jsonb),
		        COALESCE(sd.depends_on_conditions, '{}'::jsonb)
		 FROM service_deployments sd
		 JOIN services s ON s.id = sd.service_id
		 WHERE sd.deployment_id = $1
		 ORDER BY sd.created_at ASC`,
		deploymentID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list service deployments: %w", err)
	}
	defer rows.Close()

	publicHost := resolvePublicHost()
	var list []*models.ServiceDeployment
	for rows.Next() {
		sd := &models.ServiceDeployment{}
		var publicExposed bool
		var dependsOnJSON, volumesJSON, networksJSON, hcJSON, docJSON []byte
		err := rows.Scan(
			&sd.ID, &sd.DeploymentID, &sd.ServiceID, &sd.ServiceName, &publicExposed, &sd.DeployNumber,
			&sd.Status, &sd.ImageTag, &sd.ContainerID, &sd.HostPort, &sd.InternalPort,
			&sd.BuildStrategy, &sd.BuildCommand, &sd.StartCommand, &sd.RuntimeType,
			&sd.DockerfilePath, &sd.BuildContext, &sd.HealthStrategy, &sd.HealthCheckPath,
			&sd.CpuMillicores, &sd.MemoryMB, &sd.PidsLimit, &sd.EphemeralStorageMB,
			&sd.ImageDigest, &sd.SourceRevision, &sd.EnvConfigHash,
			&sd.ExecutionMode, &sd.EnvSnapshot,
			&sd.StartedAt, &sd.BuiltAt, &sd.DeployedAt, &sd.FinishedAt,
			&sd.DurationMs, &sd.FailureReason, &sd.CreatedAt,
			&sd.Classification, &sd.Image, &dependsOnJSON, &volumesJSON, &networksJSON, &hcJSON, &docJSON,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan service deployment: %w", err)
		}
		if len(dependsOnJSON) > 0 {
			if err := json.Unmarshal(dependsOnJSON, &sd.DependsOn); err != nil {
				return nil, fmt.Errorf("failed to decode depends_on for service deployment %s: %w", sd.ID, err)
			}
		}
		if sd.DependsOn == nil {
			sd.DependsOn = []string{}
		}
		if len(volumesJSON) > 0 {
			if err := json.Unmarshal(volumesJSON, &sd.Volumes); err != nil {
				return nil, fmt.Errorf("failed to decode volumes for service deployment %s: %w", sd.ID, err)
			}
		}
		if sd.Volumes == nil {
			sd.Volumes = []models.VolumeMountConfig{}
		}
		if len(networksJSON) > 0 {
			if err := json.Unmarshal(networksJSON, &sd.Networks); err != nil {
				return nil, fmt.Errorf("failed to decode networks for service deployment %s: %w", sd.ID, err)
			}
		}
		if sd.Networks == nil {
			sd.Networks = []string{}
		}
		if len(hcJSON) > 0 && string(hcJSON) != "{}" {
			var hc models.HealthCheckConfig
			if err := json.Unmarshal(hcJSON, &hc); err != nil {
				return nil, fmt.Errorf("failed to decode healthcheck_config for service deployment %s: %w", sd.ID, err)
			}
			sd.HealthCheckConfig = &hc
		}
		if len(docJSON) > 0 && string(docJSON) != "{}" {
			if err := json.Unmarshal(docJSON, &sd.DependsOnConditions); err != nil {
				return nil, fmt.Errorf("failed to decode depends_on_conditions for service deployment %s: %w", sd.ID, err)
			}
		}
		if sd.DependsOnConditions == nil {
			sd.DependsOnConditions = map[string]string{}
		}
		if publicExposed && sd.HostPort != nil && *sd.HostPort > 0 && sd.Status == models.DeployStatusRunning {
			url := fmt.Sprintf("http://%s:%d", publicHost, *sd.HostPort)
			sd.PreviewURL = &url
		} else {
			sd.PreviewURL = nil
		}
		list = append(list, sd)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating service deployments: %w", err)
	}

	if list == nil {
		list = []*models.ServiceDeployment{}
	}

	return list, nil
}

func (s *DeploymentService) listServicesTx(ctx context.Context, tx pgx.Tx, projectID uuid.UUID) ([]*models.Service, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, project_id, source_id, name, role, source_path, runtime_type, framework, package_manager,
		        build_strategy, build_command, start_command, COALESCE(dockerfile_path, ''), build_context,
		        internal_port, host_port, public_exposed, health_strategy, health_check_path, health_check_enabled,
		        status, container_id, image_tag, current_service_deployment_id, created_at, updated_at,
		        COALESCE(classification, 'application'), COALESCE(image, ''),
		        COALESCE(depends_on, '[]'::jsonb), COALESCE(volumes, '[]'::jsonb),
		        COALESCE(networks, '[]'::jsonb), COALESCE(healthcheck_config, '{}'::jsonb),
		        COALESCE(depends_on_conditions, '{}'::jsonb)
		 FROM services WHERE project_id = $1`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*models.Service
	for rows.Next() {
		svc := &models.Service{}
		var dependsOnJSON, volumesJSON, networksJSON, hcJSON, docJSON []byte
		err := rows.Scan(
			&svc.ID, &svc.ProjectID, &svc.SourceID, &svc.Name, &svc.Role, &svc.SourcePath, &svc.RuntimeType,
			&svc.Framework, &svc.PackageManager, &svc.BuildStrategy, &svc.BuildCommand,
			&svc.StartCommand, &svc.DockerfilePath, &svc.BuildContext, &svc.InternalPort, &svc.HostPort,
			&svc.PublicExposed, &svc.HealthStrategy, &svc.HealthCheckPath, &svc.HealthCheckEnabled,
			&svc.Status, &svc.ContainerID, &svc.ImageTag, &svc.CurrentServiceDeploymentID,
			&svc.CreatedAt, &svc.UpdatedAt,
			&svc.Classification, &svc.Image,
			&dependsOnJSON, &volumesJSON, &networksJSON, &hcJSON, &docJSON,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan service: %w", err)
		}
		if len(dependsOnJSON) > 0 {
			if err := json.Unmarshal(dependsOnJSON, &svc.DependsOn); err != nil {
				return nil, fmt.Errorf("failed to decode depends_on for service %s: %w", svc.Name, err)
			}
		}
		if svc.DependsOn == nil {
			svc.DependsOn = []string{}
		}
		if len(volumesJSON) > 0 {
			if err := json.Unmarshal(volumesJSON, &svc.Volumes); err != nil {
				return nil, fmt.Errorf("failed to decode volumes for service %s: %w", svc.Name, err)
			}
		}
		if svc.Volumes == nil {
			svc.Volumes = []models.VolumeMountConfig{}
		}
		if len(networksJSON) > 0 {
			if err := json.Unmarshal(networksJSON, &svc.Networks); err != nil {
				return nil, fmt.Errorf("failed to decode networks for service %s: %w", svc.Name, err)
			}
		}
		if svc.Networks == nil {
			svc.Networks = []string{}
		}
		if len(hcJSON) > 0 && string(hcJSON) != "{}" {
			var hc models.HealthCheckConfig
			if err := json.Unmarshal(hcJSON, &hc); err != nil {
				return nil, fmt.Errorf("failed to decode healthcheck_config for service %s: %w", svc.Name, err)
			}
			svc.HealthCheckConfig = &hc
		}
		if len(docJSON) > 0 && string(docJSON) != "{}" {
			if err := json.Unmarshal(docJSON, &svc.DependsOnConditions); err != nil {
				return nil, fmt.Errorf("failed to decode depends_on_conditions for service %s: %w", svc.Name, err)
			}
		}
		if svc.DependsOnConditions == nil {
			svc.DependsOnConditions = map[string]string{}
		}
		list = append(list, svc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating services: %w", err)
	}
	return list, nil
}

func resolvePublicHost() string {
	publicHost := os.Getenv("FORGELAB_PUBLIC_HOST")
	if publicHost == "" {
		publicHost = os.Getenv("PUBLIC_HOST")
	}
	if publicHost == "" {
		publicHost = "localhost"
	}
	return publicHost
}
