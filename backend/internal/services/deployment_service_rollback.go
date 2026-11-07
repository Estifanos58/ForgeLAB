package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/forgelab/backend/internal/models"
)

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
