package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/forgelab/backend/internal/models"
)

// GetDeployment retrieves a deployment by ID.
func (s *DeploymentService) GetDeployment(ctx context.Context, deploymentID uuid.UUID) (*models.Deployment, error) {
	if s.getDeploymentForTest != nil {
		return s.getDeploymentForTest(ctx, deploymentID)
	}
	if s.db == nil {
		return nil, ErrDeploymentNotFound
	}
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

	svcDeploys, err := s.ListServiceDeployments(ctx, d.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load service deployments for deployment %s: %w", d.ID, err)
	}
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

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating deployments: %w", err)
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

// GetServiceDeployment retrieves an individual service deployment record by ID.
func (s *DeploymentService) GetServiceDeployment(ctx context.Context, serviceDeploymentID uuid.UUID) (*models.ServiceDeployment, error) {
	if s.getServiceDeploymentForTest != nil {
		return s.getServiceDeploymentForTest(ctx, serviceDeploymentID)
	}
	if s.db == nil {
		return nil, ErrDeploymentNotFound
	}
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
}

// ListServiceDeploymentsByService retrieves all service deployments for a specific service ordered by deploy number.
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
	if s.listServiceDeploymentsForTest != nil {
		return s.listServiceDeploymentsForTest(ctx, deploymentID)
	}
	if s.db == nil {
		return []*models.ServiceDeployment{}, nil
	}
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
