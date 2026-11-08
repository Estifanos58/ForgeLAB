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
)

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
		User:                targetService.User,
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
