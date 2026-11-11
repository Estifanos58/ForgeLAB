package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/security"
)

func isValidGitCommitSHA(sha string) bool {
	if len(sha) != 40 {
		return false
	}
	for i := 0; i < len(sha); i++ {
		c := sha[i]
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

// CreateDeployment creates a new deployment record for a project transactionally.
// This does NOT start the deployment — it only creates the record.
// The deployment worker picks up QUEUED deployments.
func (s *DeploymentService) CreateDeployment(ctx context.Context, project *models.Project) (*models.Deployment, error) {
	return s.createDeploymentInternal(ctx, project, nil)
}

// CreateDeploymentWithCommit creates a queued deployment pinned to an explicit commit SHA (e.g. from a
// verified push-to-deploy webhook), bypassing stale manual plan revision blocks while preserving plan topology.
func (s *DeploymentService) CreateDeploymentWithCommit(ctx context.Context, project *models.Project, commitSHA *string) (*models.Deployment, error) {
	if s.createDeploymentWithCommitForTest != nil {
		return s.createDeploymentWithCommitForTest(ctx, project, commitSHA)
	}
	return s.createDeploymentInternal(ctx, project, commitSHA)
}

func (s *DeploymentService) createDeploymentInternal(ctx context.Context, project *models.Project, explicitCommit *string) (*models.Deployment, error) {
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
			if _, logErr := tx.Exec(ctx,
				`INSERT INTO deployment_logs (deployment_id, service_id, timestamp, phase, stream, message)
				 VALUES ($1, $2, $3, $4, $5, $6)`,
				*activeID, nil, now, models.LogPhaseStartup, models.LogStreamSystem, "Deployment superseded by newer queued deployment",
			); logErr != nil {
				return nil, fmt.Errorf("failed to log superseded deployment: %w", logErr)
			}
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

	var sourceRevision *string
	if explicitCommit != nil && strings.TrimSpace(*explicitCommit) != "" {
		pinned := strings.ToLower(strings.TrimSpace(*explicitCommit))
		if !isValidGitCommitSHA(pinned) {
			return nil, fmt.Errorf("%w: commit SHA must be a 40-character hexadecimal string", ErrValidationFailed)
		}
		sourceRevision = &pinned
		deployment.SourceRevision = sourceRevision
		deployment.CommitSHA = sourceRevision
	} else {
		sourceRevision, err = s.resolveSourceRevision(ctx, project, nil)
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
		if err := tx.QueryRow(ctx, "SELECT MAX(deploy_number) FROM service_deployments WHERE service_id = $1", svcItem.ID).Scan(&svcMaxNumber); err != nil {
			return nil, fmt.Errorf("failed to get max deploy number for service %s: %w", svcItem.Name, err)
		}
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

		dependsOnBytes, err := json.Marshal(svcItem.DependsOn)
		if err != nil {
			return nil, fmt.Errorf("failed to encode depends_on for service %s: %w", svcItem.Name, err)
		}
		safeVols, stripped, err := security.ValidateAndSanitizeServiceVolumes(svcItem.Volumes)
		if err != nil {
			return nil, fmt.Errorf("failed to sanitize volumes for service %s: %w", svcItem.Name, err)
		}
		if stripped {
			slog.Warn("stripped forbidden docker socket volume mount while creating service deployment", "project_id", project.ID, "service_id", svcItem.ID, "service_name", svcItem.Name)
		}
		volumesBytes, err := json.Marshal(safeVols)
		if err != nil {
			return nil, fmt.Errorf("failed to encode volumes for service %s: %w", svcItem.Name, err)
		}
		networksBytes, err := json.Marshal(svcItem.Networks)
		if err != nil {
			return nil, fmt.Errorf("failed to encode networks for service %s: %w", svcItem.Name, err)
		}
		var hcBytes []byte
		if svcItem.HealthCheckConfig != nil {
			hcBytes, err = json.Marshal(svcItem.HealthCheckConfig)
			if err != nil {
				return nil, fmt.Errorf("failed to encode healthcheck_config for service %s: %w", svcItem.Name, err)
			}
		} else {
			hcBytes = []byte("{}")
		}
		dependsOnCondBytes, err := json.Marshal(svcItem.DependsOnConditions)
		if err != nil {
			return nil, fmt.Errorf("failed to encode depends_on_conditions for service %s: %w", svcItem.Name, err)
		}

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
