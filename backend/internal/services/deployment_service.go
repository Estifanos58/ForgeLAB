package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/models"
)

var (
	ErrDeploymentNotFound     = errors.New("deployment not found")
	ErrNoDeploymentToRollback = errors.New("no previous deployment to rollback to")
)

// DeploymentService handles deployment-related business logic.
type DeploymentService struct {
	db            *pgxpool.Pool
	secretService *SecretService
}

// NewDeploymentService creates a new DeploymentService.
func NewDeploymentService(db *pgxpool.Pool) *DeploymentService {
	return &DeploymentService{db: db}
}

// SetSecretService sets the SecretService instance for env hash calculations.
func (s *DeploymentService) SetSecretService(sec *SecretService) {
	s.secretService = sec
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
	err = tx.QueryRow(ctx, "SELECT id FROM projects WHERE id = $1 FOR UPDATE", project.ID).Scan(&lockedProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to lock project row: %w", err)
	}

	// Check for existing active deployment
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
		StartedAt:      &now,
		CreatedAt:      now,
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO deployments (
			id, project_id, deploy_number, status, branch, image_tag,
			build_strategy, build_command, start_command, runtime_type, internal_port, health_strategy,
			started_at, created_at
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		deployment.ID, deployment.ProjectID, deployment.DeployNumber,
		deployment.Status, deployment.Branch, deployment.ImageTag,
		deployment.BuildStrategy, deployment.BuildCommand, deployment.StartCommand,
		deployment.RuntimeType, deployment.InternalPort, deployment.HealthStrategy,
		deployment.StartedAt, deployment.CreatedAt,
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
		        COALESCE(dockerfile_path, 'Dockerfile'), COALESCE(build_context, '.'),
		        COALESCE(health_strategy, 'auto'), health_check_path,
		        cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb
		 FROM services WHERE project_id = $1`,
		project.ID,
	)
	var svcList []models.Service
	if err == nil {
		for rows.Next() {
			var s models.Service
			if err := rows.Scan(
				&s.ID, &s.Name, &s.Role, &s.BuildStrategy, &s.BuildCommand, &s.StartCommand, &s.RuntimeType,
				&s.InternalPort, &s.DockerfilePath, &s.BuildContext, &s.HealthStrategy, &s.HealthCheckPath,
				&s.CpuMillicores, &s.MemoryMB, &s.PidsLimit, &s.EphemeralStorageMB,
			); err == nil {
				svcList = append(svcList, s)
			}
		}
		rows.Close()
	}

	for _, svcItem := range svcList {
		var svcMaxNumber *int
		_ = tx.QueryRow(ctx, "SELECT MAX(deploy_number) FROM service_deployments WHERE service_id = $1", svcItem.ID).Scan(&svcMaxNumber)
		svcDeployNum := 1
		if svcMaxNumber != nil {
			svcDeployNum = *svcMaxNumber + 1
		}

		var sourceRevision *string
		if project.SourceType == models.SourceTypeGitHub && project.Branch != "" {
			b := project.Branch
			sourceRevision = &b
		} else if project.SourceReference != "" {
			sr := project.SourceReference
			sourceRevision = &sr
		} else if project.RepositoryPath != "" {
			rp := project.RepositoryPath
			sourceRevision = &rp
		}

		var envConfigHash *string
		var envSnapshot []byte
		if s.secretService != nil {
			envSnapshot, envConfigHash, _ = s.secretService.CreateEnvSnapshot(ctx, project.ID, &svcItem.ID)
		}

		svcDeployID := uuid.New()
		svcTag := fmt.Sprintf("forgelab/%s/%s:%d", project.ID, svcItem.Name, svcDeployNum)
		if _, err := tx.Exec(ctx,
			`INSERT INTO service_deployments (
				id, deployment_id, service_id, deploy_number, status, image_tag, build_strategy,
				build_command, start_command, runtime_type, dockerfile_path, build_context, internal_port,
				health_strategy, health_check_path, cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb,
				source_revision, env_config_hash, execution_mode, env_snapshot,
				started_at, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25)`,
			svcDeployID, deployment.ID, svcItem.ID, svcDeployNum, models.DeployStatusQueued, svcTag,
			svcItem.BuildStrategy, svcItem.BuildCommand, svcItem.StartCommand, svcItem.RuntimeType, svcItem.DockerfilePath, svcItem.BuildContext,
			svcItem.InternalPort, svcItem.HealthStrategy, svcItem.HealthCheckPath,
			svcItem.CpuMillicores, svcItem.MemoryMB, svcItem.PidsLimit, svcItem.EphemeralStorageMB,
			sourceRevision, envConfigHash, models.ExecutionModeBuild, envSnapshot,
			now, now,
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

	// Lock only the target service row FOR UPDATE to prevent concurrent creation for the exact same service
	var lockedServiceID uuid.UUID
	err = tx.QueryRow(ctx, "SELECT id FROM services WHERE id = $1 FOR UPDATE", targetService.ID).Scan(&lockedServiceID)
	if err != nil {
		return nil, fmt.Errorf("failed to lock service row: %w", err)
	}

	// Enforce active deployment check strictly per service_id (Frontend does not block Backend!)
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

	var sourceRevision *string
	if project.SourceType == models.SourceTypeGitHub && project.Branch != "" {
		b := project.Branch
		sourceRevision = &b
	} else if project.SourceReference != "" {
		sr := project.SourceReference
		sourceRevision = &sr
	} else if project.RepositoryPath != "" {
		rp := project.RepositoryPath
		sourceRevision = &rp
	}

	var envConfigHash *string
	var envSnapshot []byte
	if s.secretService != nil {
		envSnapshot, envConfigHash, _ = s.secretService.CreateEnvSnapshot(ctx, project.ID, &targetService.ID)
	}

	svcDeployID := uuid.New()
	serviceDeployment := &models.ServiceDeployment{
		ID:              svcDeployID,
		DeploymentID:    nil,
		ServiceID:       targetService.ID,
		ServiceName:     targetService.Name,
		DeployNumber:    deployNumber,
		Status:          models.DeployStatusQueued,
		ImageTag:        &imageTag,
		InternalPort:    internalPort,
		BuildStrategy:   buildStrategy,
		BuildCommand:    buildCommand,
		StartCommand:    startCommand,
		RuntimeType:     runtimeType,
		DockerfilePath:  dockerfilePath,
		BuildContext:    buildContext,
		HealthStrategy:  healthStrategy,
		HealthCheckPath: healthCheckPath,
		ResourceConfig:  resConfig,
		ExecutionMode:   models.ExecutionModeBuild,
		SourceRevision:  sourceRevision,
		EnvConfigHash:   envConfigHash,
		EnvSnapshot:     envSnapshot,
		StartedAt:       &now,
		CreatedAt:       now,
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO service_deployments (
			id, deployment_id, service_id, deploy_number, status, image_tag,
			build_strategy, build_command, start_command, runtime_type,
			dockerfile_path, build_context, internal_port, health_strategy, health_check_path,
			cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb,
			source_revision, env_config_hash, execution_mode, env_snapshot,
			started_at, created_at
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25)`,
		serviceDeployment.ID, serviceDeployment.DeploymentID, serviceDeployment.ServiceID,
		serviceDeployment.DeployNumber, serviceDeployment.Status, serviceDeployment.ImageTag,
		serviceDeployment.BuildStrategy, serviceDeployment.BuildCommand, serviceDeployment.StartCommand,
		serviceDeployment.RuntimeType, serviceDeployment.DockerfilePath, serviceDeployment.BuildContext,
		serviceDeployment.InternalPort, serviceDeployment.HealthStrategy, serviceDeployment.HealthCheckPath,
		resConfig.CpuMillicores, resConfig.MemoryMB, resConfig.PidsLimit, resConfig.EphemeralStorageMB,
		sourceRevision, envConfigHash, models.ExecutionModeBuild, envSnapshot,
		serviceDeployment.StartedAt, serviceDeployment.CreatedAt,
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

// UpdateDeploymentStatus updates the status of a deployment enforcing valid state transitions atomically.
func (s *DeploymentService) UpdateDeploymentStatus(ctx context.Context, deploymentID uuid.UUID, newStatus string, failureReason *string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. Fetch current status with row-level exclusive lock
	var currentStatus string
	err = tx.QueryRow(ctx, "SELECT status FROM deployments WHERE id = $1 FOR UPDATE", deploymentID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDeploymentNotFound
		}
		return fmt.Errorf("failed to fetch deployment status: %w", err)
	}

	// 2. Validate state transition
	if err := models.ValidateStateTransition(currentStatus, newStatus); err != nil {
		return err
	}

	now := time.Now()

	// Build the update query based on the new status
	query := `UPDATE deployments SET status = $1, `
	args := []interface{}{newStatus}
	argIdx := 2

	switch newStatus {
	case models.DeployStatusBuilding:
		// No additional timestamp
	case models.DeployStatusStarting:
		query += fmt.Sprintf("built_at = $%d, ", argIdx)
		args = append(args, now)
		argIdx++
	case models.DeployStatusHealthChecking:
		query += fmt.Sprintf("deployed_at = $%d, ", argIdx)
		args = append(args, now)
		argIdx++
	case models.DeployStatusRunning:
		query += fmt.Sprintf("finished_at = $%d, ", argIdx)
		args = append(args, now)
		argIdx++
	case models.DeployStatusFailed:
		query += fmt.Sprintf("finished_at = $%d, failure_reason = $%d, ", argIdx, argIdx+1)
		args = append(args, now, failureReason)
		argIdx += 2
	case models.DeployStatusStopped:
		query += fmt.Sprintf("finished_at = $%d, ", argIdx)
		args = append(args, now)
		argIdx++
	case models.DeployStatusCrashed:
		query += fmt.Sprintf("finished_at = $%d, ", argIdx)
		args = append(args, now)
		argIdx++
	}

	// Calculate duration if terminal state
	if newStatus == models.DeployStatusRunning || newStatus == models.DeployStatusFailed ||
		newStatus == models.DeployStatusStopped || newStatus == models.DeployStatusCrashed {
		query += fmt.Sprintf("duration_ms = EXTRACT(EPOCH FROM ($%d - started_at)) * 1000, ", argIdx)
		args = append(args, now)
		argIdx++
	}

	// Remove trailing comma and space, add WHERE
	query = query[:len(query)-2]
	query += fmt.Sprintf(" WHERE id = $%d", argIdx)
	args = append(args, deploymentID)

	_, err = tx.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to update deployment status: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit deployment status update: %w", err)
	}

	slog.Info("deployment status updated atomically", "deployment_id", deploymentID, "status", newStatus)
	return nil
}

// UpdateDeploymentContainer updates the container ID for a deployment.
func (s *DeploymentService) UpdateDeploymentContainer(ctx context.Context, deploymentID uuid.UUID, containerID string) error {
	_, err := s.db.Exec(ctx,
		"UPDATE deployments SET container_id = $1 WHERE id = $2",
		containerID, deploymentID,
	)
	if err != nil {
		return fmt.Errorf("failed to update container ID: %w", err)
	}
	return nil
}

// SetCurrentDeployment updates the project's current deployment and status.
func (s *DeploymentService) SetCurrentDeployment(ctx context.Context, projectID, deploymentID uuid.UUID, projectStatus string) error {
	_, err := s.db.Exec(ctx,
		"UPDATE projects SET current_deployment_id = $1, status = $2, updated_at = $3 WHERE id = $4",
		deploymentID, projectStatus, time.Now(), projectID,
	)
	if err != nil {
		return fmt.Errorf("failed to set current deployment: %w", err)
	}
	return nil
}

// GetDeployment retrieves a deployment by ID.
func (s *DeploymentService) GetDeployment(ctx context.Context, deploymentID uuid.UUID) (*models.Deployment, error) {
	d := &models.Deployment{}
	err := s.db.QueryRow(ctx,
		`SELECT id, project_id, deploy_number, status, commit_sha, branch,
		 image_tag, container_id, source_revision, build_strategy, build_command,
		 start_command, runtime_type, internal_port, health_strategy,
		 started_at, built_at, deployed_at, finished_at, duration_ms, failure_reason, created_at
		 FROM deployments WHERE id = $1`,
		deploymentID,
	).Scan(
		&d.ID, &d.ProjectID, &d.DeployNumber, &d.Status, &d.CommitSHA, &d.Branch,
		&d.ImageTag, &d.ContainerID, &d.SourceRevision, &d.BuildStrategy, &d.BuildCommand,
		&d.StartCommand, &d.RuntimeType, &d.InternalPort, &d.HealthStrategy,
		&d.StartedAt, &d.BuiltAt, &d.DeployedAt, &d.FinishedAt, &d.DurationMs, &d.FailureReason, &d.CreatedAt,
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

// ListDeployments retrieves all deployments for a project.
func (s *DeploymentService) ListDeployments(ctx context.Context, projectID uuid.UUID) ([]*models.Deployment, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, project_id, deploy_number, status, commit_sha, branch,
		 image_tag, container_id, source_revision, build_strategy, build_command,
		 start_command, runtime_type, internal_port, health_strategy,
		 started_at, built_at, deployed_at, finished_at, duration_ms, failure_reason, created_at
		 FROM deployments WHERE project_id = $1 ORDER BY deploy_number DESC`,
		projectID,
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
			&d.StartedAt, &d.BuiltAt, &d.DeployedAt, &d.FinishedAt, &d.DurationMs, &d.FailureReason, &d.CreatedAt,
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

// GetPreviousSuccessfulDeployment finds the most recent RUNNING or STOPPED deployment
// before the given deployment for rollback purposes.
func (s *DeploymentService) GetPreviousSuccessfulDeployment(ctx context.Context, projectID uuid.UUID, beforeDeployNumber int) (*models.Deployment, error) {
	d := &models.Deployment{}
	err := s.db.QueryRow(ctx,
		`SELECT id, project_id, deploy_number, status, commit_sha, branch,
		 image_tag, container_id, started_at, built_at, deployed_at,
		 finished_at, duration_ms, failure_reason, created_at
		 FROM deployments
		 WHERE project_id = $1
		   AND deploy_number < $2
		   AND status IN ($3, $4)
		   AND image_tag IS NOT NULL
		 ORDER BY deploy_number DESC
		 LIMIT 1`,
		projectID, beforeDeployNumber,
		models.DeployStatusRunning, models.DeployStatusStopped,
	).Scan(
		&d.ID, &d.ProjectID, &d.DeployNumber, &d.Status, &d.CommitSHA,
		&d.Branch, &d.ImageTag, &d.ContainerID, &d.StartedAt, &d.BuiltAt,
		&d.DeployedAt, &d.FinishedAt, &d.DurationMs, &d.FailureReason, &d.CreatedAt,
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
	err = tx.QueryRow(ctx,
		`SELECT id, deploy_number, image_tag, internal_port, build_strategy, build_command,
		        start_command, runtime_type, COALESCE(dockerfile_path, 'Dockerfile'),
		        COALESCE(build_context, '.'), COALESCE(health_strategy, 'auto'), health_check_path,
		        cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb,
		        image_digest, source_revision, env_config_hash, COALESCE(execution_mode, 'build'), env_snapshot
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
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoDeploymentToRollback
		}
		return nil, fmt.Errorf("failed to find previous service deployment: %w", err)
	}

	var maxNumber *int
	_ = tx.QueryRow(ctx, "SELECT MAX(deploy_number) FROM service_deployments WHERE service_id = $1", targetService.ID).Scan(&maxNumber)
	deployNumber := 1
	if maxNumber != nil {
		deployNumber = *maxNumber + 1
	}

	// Execution mode: reuse existing immutable image whenever available instead of forcing source rebuild
	executionMode := models.ExecutionModeReuseImage
	if (prev.ImageDigest == nil || *prev.ImageDigest == "") && (prev.ImageTag == nil || *prev.ImageTag == "") {
		executionMode = models.ExecutionModeBuild
	}

	now := time.Now()
	svcDeployID := uuid.New()
	serviceDeployment := &models.ServiceDeployment{
		ID:              svcDeployID,
		DeploymentID:    nil,
		ServiceID:       targetService.ID,
		ServiceName:     targetService.Name,
		DeployNumber:    deployNumber,
		Status:          models.DeployStatusQueued,
		ExecutionMode:   executionMode,
		ImageTag:        prev.ImageTag,
		InternalPort:    prev.InternalPort,
		BuildStrategy:   prev.BuildStrategy,
		BuildCommand:    prev.BuildCommand,
		StartCommand:    prev.StartCommand,
		RuntimeType:     prev.RuntimeType,
		DockerfilePath:  prev.DockerfilePath,
		BuildContext:    prev.BuildContext,
		HealthStrategy:  prev.HealthStrategy,
		HealthCheckPath: prev.HealthCheckPath,
		ResourceConfig:  prev.ResourceConfig,
		ImageDigest:     prev.ImageDigest,
		SourceRevision:  prev.SourceRevision,
		EnvConfigHash:   prev.EnvConfigHash,
		EnvSnapshot:     prev.EnvSnapshot,
		StartedAt:       &now,
		CreatedAt:       now,
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO service_deployments (
			id, deployment_id, service_id, deploy_number, status, image_tag,
			build_strategy, build_command, start_command, runtime_type,
			dockerfile_path, build_context, internal_port, health_strategy, health_check_path,
			cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb,
			image_digest, source_revision, env_config_hash, execution_mode, env_snapshot,
			started_at, created_at
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26)`,
		serviceDeployment.ID, serviceDeployment.DeploymentID, serviceDeployment.ServiceID,
		serviceDeployment.DeployNumber, serviceDeployment.Status, serviceDeployment.ImageTag,
		serviceDeployment.BuildStrategy, serviceDeployment.BuildCommand, serviceDeployment.StartCommand,
		serviceDeployment.RuntimeType, serviceDeployment.DockerfilePath, serviceDeployment.BuildContext,
		serviceDeployment.InternalPort, serviceDeployment.HealthStrategy, serviceDeployment.HealthCheckPath,
		serviceDeployment.CpuMillicores, serviceDeployment.MemoryMB, serviceDeployment.PidsLimit, serviceDeployment.EphemeralStorageMB,
		serviceDeployment.ImageDigest, serviceDeployment.SourceRevision, serviceDeployment.EnvConfigHash,
		serviceDeployment.ExecutionMode, serviceDeployment.EnvSnapshot,
		serviceDeployment.StartedAt, serviceDeployment.CreatedAt,
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

// GetServiceDeployment retrieves an individual service deployment record by ID.
func (s *DeploymentService) GetServiceDeployment(ctx context.Context, serviceDeploymentID uuid.UUID) (*models.ServiceDeployment, error) {
	sd := &models.ServiceDeployment{}
	var publicExposed bool
	err := s.db.QueryRow(ctx,
		`SELECT sd.id, sd.deployment_id, sd.service_id, s.name, s.public_exposed, sd.deploy_number,
		        sd.status, sd.image_tag, sd.container_id, sd.host_port, sd.internal_port,
		        sd.build_strategy, sd.build_command, sd.start_command, sd.runtime_type,
		        COALESCE(sd.dockerfile_path, 'Dockerfile'), COALESCE(sd.build_context, '.'),
		        COALESCE(sd.health_strategy, 'auto'), sd.health_check_path,
		        sd.cpu_millicores, sd.memory_mb, sd.pids_limit, sd.ephemeral_storage_mb,
		        sd.image_digest, sd.source_revision, sd.env_config_hash,
		        COALESCE(sd.execution_mode, 'build'), sd.env_snapshot,
		        sd.started_at, sd.built_at, sd.deployed_at, sd.finished_at,
		        sd.duration_ms, sd.failure_reason, sd.created_at
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
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDeploymentNotFound
		}
		return nil, fmt.Errorf("failed to get service deployment: %w", err)
	}

	if publicExposed && sd.HostPort != nil && *sd.HostPort > 0 {
		url := fmt.Sprintf("http://%s:%d", resolvePublicHost(), *sd.HostPort)
		sd.PreviewURL = &url
	}

	return sd, nil
}

// ListServiceDeploymentsByService retrieves all service deployments for a specific service ordered by deploy number.
func (s *DeploymentService) ListServiceDeploymentsByService(ctx context.Context, serviceID uuid.UUID) ([]*models.ServiceDeployment, error) {
	rows, err := s.db.Query(ctx,
		`SELECT sd.id, sd.deployment_id, sd.service_id, s.name, s.public_exposed, sd.deploy_number,
		        sd.status, sd.image_tag, sd.container_id, sd.host_port, sd.internal_port,
		        sd.build_strategy, sd.build_command, sd.start_command, sd.runtime_type,
		        COALESCE(sd.dockerfile_path, 'Dockerfile'), COALESCE(sd.build_context, '.'),
		        COALESCE(sd.health_strategy, 'auto'), sd.health_check_path,
		        sd.cpu_millicores, sd.memory_mb, sd.pids_limit, sd.ephemeral_storage_mb,
		        sd.image_digest, sd.source_revision, sd.env_config_hash,
		        COALESCE(sd.execution_mode, 'build'), sd.env_snapshot,
		        sd.started_at, sd.built_at, sd.deployed_at, sd.finished_at,
		        sd.duration_ms, sd.failure_reason, sd.created_at
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
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan service deployment: %w", err)
		}
		if publicExposed && sd.HostPort != nil && *sd.HostPort > 0 {
			url := fmt.Sprintf("http://%s:%d", publicHost, *sd.HostPort)
			sd.PreviewURL = &url
		}
		list = append(list, sd)
	}

	if list == nil {
		list = []*models.ServiceDeployment{}
	}

	return list, nil
}

// AddDeploymentLog adds a log entry for a deployment and returns the persisted record.
func (s *DeploymentService) AddDeploymentLog(ctx context.Context, deploymentID uuid.UUID, phase, stream, message string) (*models.DeploymentLog, error) {
	return s.AddDeploymentServiceLog(ctx, &deploymentID, nil, nil, phase, stream, message)
}

// AddDeploymentServiceLog adds a log entry scoped to a specific deployment, service deployment, and/or service.
func (s *DeploymentService) AddDeploymentServiceLog(
	ctx context.Context,
	deploymentID *uuid.UUID,
	serviceDeploymentID *uuid.UUID,
	serviceID *uuid.UUID,
	phase, stream, message string,
) (*models.DeploymentLog, error) {
	log := &models.DeploymentLog{
		DeploymentID:        deploymentID,
		ServiceDeploymentID: serviceDeploymentID,
		ServiceID:           serviceID,
		Phase:               phase,
		Stream:              stream,
		Message:             message,
	}
	err := s.db.QueryRow(ctx,
		`INSERT INTO deployment_logs (deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message)
		 VALUES ($1, $2, $3, NOW(), $4, $5, $6)
		 RETURNING id, timestamp`,
		deploymentID, serviceDeploymentID, serviceID, phase, stream, message,
	).Scan(&log.ID, &log.Timestamp)
	if err != nil {
		return nil, fmt.Errorf("failed to add deployment log: %w", err)
	}
	return log, nil
}

// GetDeploymentLogs retrieves logs for a release deployment.
func (s *DeploymentService) GetDeploymentLogs(ctx context.Context, deploymentID uuid.UUID) ([]*models.DeploymentLog, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
		 FROM deployment_logs WHERE deployment_id = $1 ORDER BY timestamp ASC, id ASC`,
		deploymentID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment logs: %w", err)
	}
	defer rows.Close()

	var logs []*models.DeploymentLog
	for rows.Next() {
		l := &models.DeploymentLog{}
		err := rows.Scan(&l.ID, &l.DeploymentID, &l.ServiceDeploymentID, &l.ServiceID, &l.Timestamp, &l.Phase, &l.Stream, &l.Message)
		if err != nil {
			return nil, fmt.Errorf("failed to scan deployment log: %w", err)
		}
		logs = append(logs, l)
	}

	if logs == nil {
		logs = []*models.DeploymentLog{}
	}

	return logs, nil
}

// GetServiceLogs retrieves logs scoped strictly to a specific release and service.
func (s *DeploymentService) GetServiceLogs(ctx context.Context, deploymentID, serviceID uuid.UUID) ([]*models.DeploymentLog, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
		 FROM deployment_logs 
		 WHERE deployment_id = $1 AND service_id = $2
		 ORDER BY timestamp ASC, id ASC`,
		deploymentID, serviceID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get service deployment logs: %w", err)
	}
	defer rows.Close()

	var logs []*models.DeploymentLog
	for rows.Next() {
		l := &models.DeploymentLog{}
		err := rows.Scan(&l.ID, &l.DeploymentID, &l.ServiceDeploymentID, &l.ServiceID, &l.Timestamp, &l.Phase, &l.Stream, &l.Message)
		if err != nil {
			return nil, fmt.Errorf("failed to scan service log: %w", err)
		}
		logs = append(logs, l)
	}

	if logs == nil {
		logs = []*models.DeploymentLog{}
	}

	return logs, nil
}

// GetServiceDeploymentLogs retrieves logs scoped strictly to an individual service deployment record.
func (s *DeploymentService) GetServiceDeploymentLogs(ctx context.Context, serviceDeploymentID uuid.UUID) ([]*models.DeploymentLog, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
		 FROM deployment_logs 
		 WHERE service_deployment_id = $1
		 ORDER BY timestamp ASC, id ASC`,
		serviceDeploymentID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get service deployment logs: %w", err)
	}
	defer rows.Close()

	var logs []*models.DeploymentLog
	for rows.Next() {
		l := &models.DeploymentLog{}
		err := rows.Scan(&l.ID, &l.DeploymentID, &l.ServiceDeploymentID, &l.ServiceID, &l.Timestamp, &l.Phase, &l.Stream, &l.Message)
		if err != nil {
			return nil, fmt.Errorf("failed to scan service deployment log: %w", err)
		}
		logs = append(logs, l)
	}

	if logs == nil {
		logs = []*models.DeploymentLog{}
	}

	return logs, nil
}

// GetDeploymentLogsAfter retrieves logs for a deployment created after a given log ID sequence.
func (s *DeploymentService) GetDeploymentLogsAfter(ctx context.Context, deploymentID uuid.UUID, serviceID *uuid.UUID, afterID int64, limit int) ([]*models.DeploymentLog, error) {
	if s.db == nil {
		return []*models.DeploymentLog{}, nil
	}
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}

	var (
		rows pgx.Rows
		err  error
	)
	if serviceID != nil {
		rows, err = s.db.Query(ctx,
			`SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
			 FROM deployment_logs 
			 WHERE deployment_id = $1 AND service_id = $2 AND id > $3
			 ORDER BY id ASC LIMIT $4`,
			deploymentID, *serviceID, afterID, limit,
		)
	} else {
		rows, err = s.db.Query(ctx,
			`SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
			 FROM deployment_logs 
			 WHERE deployment_id = $1 AND id > $2
			 ORDER BY id ASC LIMIT $3`,
			deploymentID, afterID, limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment logs after sequence: %w", err)
	}
	defer rows.Close()

	var logs []*models.DeploymentLog
	for rows.Next() {
		l := &models.DeploymentLog{}
		if err := rows.Scan(&l.ID, &l.DeploymentID, &l.ServiceDeploymentID, &l.ServiceID, &l.Timestamp, &l.Phase, &l.Stream, &l.Message); err != nil {
			return nil, fmt.Errorf("failed to scan log: %w", err)
		}
		logs = append(logs, l)
	}

	if logs == nil {
		logs = []*models.DeploymentLog{}
	}

	return logs, nil
}

// ListServiceDeployments retrieves all service deployment records for a release.
func (s *DeploymentService) ListServiceDeployments(ctx context.Context, deploymentID uuid.UUID) ([]*models.ServiceDeployment, error) {
	rows, err := s.db.Query(ctx,
		`SELECT sd.id, sd.deployment_id, sd.service_id, s.name, s.public_exposed, sd.deploy_number,
		        sd.status, sd.image_tag, sd.container_id, sd.host_port, sd.internal_port,
		        sd.build_strategy, sd.build_command, sd.start_command, sd.runtime_type,
		        COALESCE(sd.dockerfile_path, 'Dockerfile'), COALESCE(sd.build_context, '.'),
		        COALESCE(sd.health_strategy, 'auto'), sd.health_check_path,
		        sd.cpu_millicores, sd.memory_mb, sd.pids_limit, sd.ephemeral_storage_mb,
		        sd.image_digest, sd.source_revision, sd.env_config_hash,
		        COALESCE(sd.execution_mode, 'build'), sd.env_snapshot,
		        sd.started_at, sd.built_at, sd.deployed_at, sd.finished_at,
		        sd.duration_ms, sd.failure_reason, sd.created_at
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
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan service deployment: %w", err)
		}
		if publicExposed && sd.HostPort != nil && *sd.HostPort > 0 {
			url := fmt.Sprintf("http://%s:%d", publicHost, *sd.HostPort)
			sd.PreviewURL = &url
		}
		list = append(list, sd)
	}

	if list == nil {
		list = []*models.ServiceDeployment{}
	}

	return list, nil
}

// UpdateServiceDeploymentStatus updates status for an individual service deployment with state validation and atomic lock.
func (s *DeploymentService) UpdateServiceDeploymentStatus(ctx context.Context, serviceDeploymentID uuid.UUID, newStatus string, failureReason *string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var currentStatus string
	var startedAt *time.Time
	err = tx.QueryRow(ctx,
		"SELECT status, started_at FROM service_deployments WHERE id = $1 FOR UPDATE",
		serviceDeploymentID,
	).Scan(&currentStatus, &startedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("failed to fetch service deployment status: %w", err)
	}

	if currentStatus != "" {
		if err := models.ValidateStateTransition(currentStatus, newStatus); err != nil {
			slog.Warn("skipping invalid service deployment status transition",
				"service_deployment_id", serviceDeploymentID,
				"from", currentStatus,
				"to", newStatus,
				"error", err,
			)
			return err
		}
	}

	now := time.Now()
	var builtAt, deployedAt, finishedAt *time.Time
	var durationMs *int64

	switch newStatus {
	case models.DeployStatusStarting:
		builtAt = &now
	case models.DeployStatusHealthChecking:
		deployedAt = &now
	case models.DeployStatusRunning:
		deployedAt = &now
		finishedAt = &now
		if startedAt != nil {
			ms := now.Sub(*startedAt).Milliseconds()
			durationMs = &ms
		}
	case models.DeployStatusFailed, models.DeployStatusStopped, models.DeployStatusCrashed:
		finishedAt = &now
		if startedAt != nil {
			ms := now.Sub(*startedAt).Milliseconds()
			durationMs = &ms
		}
	}

	_, err = tx.Exec(ctx,
		`UPDATE service_deployments SET
		 status = $2,
		 built_at = COALESCE($3, built_at),
		 deployed_at = COALESCE($4, deployed_at),
		 finished_at = COALESCE($5, finished_at),
		 duration_ms = COALESCE($6, duration_ms),
		 failure_reason = COALESCE($7, failure_reason)
		 WHERE id = $1`,
		serviceDeploymentID, newStatus, builtAt, deployedAt, finishedAt, durationMs, failureReason,
	)
	if err != nil {
		return fmt.Errorf("failed to update service deployment status: %w", err)
	}

	return tx.Commit(ctx)
}

// UpdateServiceDeploymentStatusByDepAndSvc provides backward compatibility for release-level updates.
func (s *DeploymentService) UpdateServiceDeploymentStatusByDepAndSvc(ctx context.Context, deploymentID, serviceID uuid.UUID, newStatus string, failureReason *string) error {
	var sdID uuid.UUID
	err := s.db.QueryRow(ctx, "SELECT id FROM service_deployments WHERE deployment_id = $1 AND service_id = $2", deploymentID, serviceID).Scan(&sdID)
	if err != nil {
		return err
	}
	return s.UpdateServiceDeploymentStatus(ctx, sdID, newStatus, failureReason)
}

// UpdateServiceDeploymentContainer records container ID and allocated host port for a service deployment.
func (s *DeploymentService) UpdateServiceDeploymentContainer(ctx context.Context, serviceDeploymentID uuid.UUID, containerID string, hostPort *int) error {
	_, err := s.db.Exec(ctx,
		`UPDATE service_deployments SET
		 container_id = $2,
		 host_port = $3
		 WHERE id = $1`,
		serviceDeploymentID, containerID, hostPort,
	)
	return err
}

// UpdateServiceDeploymentContainerByDepAndSvc provides backward compatibility for release-level container updates.
func (s *DeploymentService) UpdateServiceDeploymentContainerByDepAndSvc(ctx context.Context, deploymentID, serviceID uuid.UUID, containerID string, hostPort *int) error {
	_, err := s.db.Exec(ctx,
		`UPDATE service_deployments SET
		 container_id = $3,
		 host_port = $4
		 WHERE deployment_id = $1 AND service_id = $2`,
		deploymentID, serviceID, containerID, hostPort,
	)
	return err
}

func (s *DeploymentService) listServicesTx(ctx context.Context, tx pgx.Tx, projectID uuid.UUID) ([]*models.Service, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, project_id, source_id, name, role, source_path, runtime_type, framework, package_manager,
		        build_strategy, build_command, start_command, dockerfile_path, build_context,
		        internal_port, host_port, public_exposed, health_strategy, health_check_path, health_check_enabled,
		        status, container_id, image_tag, current_service_deployment_id, created_at, updated_at
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
		err := rows.Scan(
			&svc.ID, &svc.ProjectID, &svc.SourceID, &svc.Name, &svc.Role, &svc.SourcePath, &svc.RuntimeType,
			&svc.Framework, &svc.PackageManager, &svc.BuildStrategy, &svc.BuildCommand,
			&svc.StartCommand, &svc.DockerfilePath, &svc.BuildContext, &svc.InternalPort, &svc.HostPort,
			&svc.PublicExposed, &svc.HealthStrategy, &svc.HealthCheckPath, &svc.HealthCheckEnabled,
			&svc.Status, &svc.ContainerID, &svc.ImageTag, &svc.CurrentServiceDeploymentID,
			&svc.CreatedAt, &svc.UpdatedAt,
		)
		if err == nil {
			list = append(list, svc)
		}
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

// GetServiceDeploymentLogsAfter retrieves logs for a service deployment after a given log ID for WS replay.
func (s *DeploymentService) GetServiceDeploymentLogsAfter(ctx context.Context, serviceDeploymentID uuid.UUID, afterID int64, limit int) ([]*models.DeploymentLog, error) {
	if s.db == nil {
		return []*models.DeploymentLog{}, nil
	}
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}

	rows, err := s.db.Query(ctx,
		`SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
		 FROM deployment_logs
		 WHERE service_deployment_id = $1 AND id > $2
		 ORDER BY id ASC LIMIT $3`,
		serviceDeploymentID, afterID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get service deployment logs after sequence: %w", err)
	}
	defer rows.Close()

	var logs []*models.DeploymentLog
	for rows.Next() {
		l := &models.DeploymentLog{}
		if err := rows.Scan(&l.ID, &l.DeploymentID, &l.ServiceDeploymentID, &l.ServiceID, &l.Timestamp, &l.Phase, &l.Stream, &l.Message); err != nil {
			return nil, fmt.Errorf("failed to scan log: %w", err)
		}
		logs = append(logs, l)
	}

	if logs == nil {
		logs = []*models.DeploymentLog{}
	}

	return logs, nil
}

// ReconcileOrphanedDeployments finds service deployments stuck in active states for too long
// and marks them as failed. This prevents permanently-queued records when Redis enqueue fails.
// DeploymentQueueReconciler abstracts queue queries and enqueueing for reconciliation.
type DeploymentQueueReconciler interface {
	IsJobEnqueuedOrActive(ctx context.Context, jobID uuid.UUID) (bool, error)
	EnqueueServiceDeployment(ctx context.Context, serviceDeploymentID uuid.UUID) error
	GetAttempts(ctx context.Context, jobID uuid.UUID) (int, error)
	IncrementAttempts(ctx context.Context, jobID uuid.UUID) (int64, error)
}

// ReconcileQueuedDeployments reconciles service deployments whose status is 'queued'.
// If their Redis job is missing:
// - If attempts < maxRetries, it re-enqueues the job.
// - If attempts >= maxRetries, it marks the deployment failed.
func (s *DeploymentService) ReconcileQueuedDeployments(ctx context.Context, q DeploymentQueueReconciler, minAge time.Duration, maxRetries int) (int, error) {
	if s.db == nil || q == nil {
		return 0, nil
	}

	rows, err := s.db.Query(ctx,
		`SELECT id, service_id, created_at
		 FROM service_deployments
		 WHERE status = $1
		   AND created_at < NOW() - $2::interval
		 ORDER BY created_at ASC`,
		models.DeployStatusQueued, minAge.String(),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to query queued service deployments: %w", err)
	}
	defer rows.Close()

	type queuedItem struct {
		id        uuid.UUID
		serviceID uuid.UUID
		createdAt time.Time
	}
	var items []queuedItem
	for rows.Next() {
		var it queuedItem
		if err := rows.Scan(&it.id, &it.serviceID, &it.createdAt); err == nil {
			items = append(items, it)
		}
	}

	reconciled := 0
	for _, item := range items {
		activeOrQueued, err := q.IsJobEnqueuedOrActive(ctx, item.id)
		if err != nil {
			slog.Warn("reconciliation: error checking job presence in queue", "service_deployment_id", item.id, "error", err)
			continue
		}
		if activeOrQueued {
			continue
		}

		// The Redis job is missing!
		attempts, _ := q.GetAttempts(ctx, item.id)
		if attempts < maxRetries {
			if err := q.EnqueueServiceDeployment(ctx, item.id); err != nil {
				slog.Error("reconciliation: failed to re-enqueue missing queued deployment", "id", item.id, "error", err)
				continue
			}
			_, _ = q.IncrementAttempts(ctx, item.id)
			slog.Info("re-enqueued missing queued service deployment via reconciliation",
				"service_deployment_id", item.id, "attempts", attempts+1)
			reconciled++
		} else {
			reason := fmt.Sprintf("Reconciliation: queued deployment was missing from queue and exceeded max recovery attempts (%d)", maxRetries)
			_ = s.FailServiceDeployment(ctx, item.id, reason)
			slog.Warn("marked missing queued deployment as failed after exceeding recovery attempts",
				"service_deployment_id", item.id, "attempts", attempts)
			reconciled++
		}
	}

	return reconciled, nil
}

// ReconcileOrphanedDeploymentsWithQueue checks active service deployments (cloning, building, starting, health_checking).
// Instead of blindly failing them after a timeout, it inspects whether a worker holds an active lease:
// - If an active worker lease exists, the deployment is actively processing and not orphaned.
// - If no active lease exists and it has been abandoned beyond staleDuration:
//     - If attempts < maxRetries, it resets status to 'queued' and re-enqueues for recovery.
//     - If attempts >= maxRetries, it marks the deployment as failed.
func (s *DeploymentService) ReconcileOrphanedDeploymentsWithQueue(ctx context.Context, q DeploymentQueueReconciler, staleDuration time.Duration, maxRetries int) (int, error) {
	if s.db == nil {
		return 0, nil
	}
	if q == nil {
		return s.ReconcileOrphanedDeployments(ctx, staleDuration)
	}

	rows, err := s.db.Query(ctx,
		`SELECT id, service_id, status, created_at, started_at
		 FROM service_deployments
		 WHERE status IN ($1, $2, $3, $4)
		   AND COALESCE(started_at, created_at) < NOW() - $5::interval
		 ORDER BY created_at ASC`,
		models.DeployStatusCloning, models.DeployStatusBuilding,
		models.DeployStatusStarting, models.DeployStatusHealthChecking,
		staleDuration.String(),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to query active service deployments: %w", err)
	}
	defer rows.Close()

	type activeItem struct {
		id        uuid.UUID
		serviceID uuid.UUID
		status    string
		createdAt time.Time
		startedAt *time.Time
	}
	var items []activeItem
	for rows.Next() {
		var it activeItem
		if err := rows.Scan(&it.id, &it.serviceID, &it.status, &it.createdAt, &it.startedAt); err == nil {
			items = append(items, it)
		}
	}

	reconciled := 0
	for _, item := range items {
		active, err := q.IsJobEnqueuedOrActive(ctx, item.id)
		if err != nil {
			slog.Warn("reconciliation: error checking active status in queue", "id", item.id, "error", err)
			continue
		}
		if active {
			// Worker is heartbeating or job is in queue
			continue
		}

		// Abandoned!
		attempts, _ := q.GetAttempts(ctx, item.id)
		if attempts < maxRetries {
			// Recover: reset status to queued and re-enqueue
			_, err = s.db.Exec(ctx,
				`UPDATE service_deployments SET status = $2, lease_worker_id = NULL WHERE id = $1`,
				item.id, models.DeployStatusQueued,
			)
			if err != nil {
				slog.Error("reconciliation: failed to reset abandoned deployment status", "id", item.id, "error", err)
				continue
			}
			if err := q.EnqueueServiceDeployment(ctx, item.id); err != nil {
				slog.Error("reconciliation: failed to re-enqueue abandoned deployment", "id", item.id, "error", err)
				continue
			}
			_, _ = q.IncrementAttempts(ctx, item.id)
			slog.Info("re-enqueued abandoned active deployment for recovery",
				"service_deployment_id", item.id, "previous_status", item.status, "attempts", attempts+1)
			reconciled++
		} else {
			reason := fmt.Sprintf("Reconciliation: abandoned deployment exceeded max recovery attempts (%d)", maxRetries)
			_ = s.FailServiceDeployment(ctx, item.id, reason)
			slog.Warn("marked abandoned deployment as failed after exceeding max attempts",
				"service_deployment_id", item.id, "attempts", attempts)
			reconciled++
		}
	}

	// Also reconcile missing queued deployments
	queuedRec, _ := s.ReconcileQueuedDeployments(ctx, q, 30*time.Second, maxRetries)
	return reconciled + queuedRec, nil
}

// ReconcileOrphanedDeployments finds service deployments stuck in active states for too long
// and marks them as failed. This fallback is used when no queue is available.
func (s *DeploymentService) ReconcileOrphanedDeployments(ctx context.Context, staleDuration time.Duration) (int, error) {
	if s.db == nil {
		return 0, nil
	}

	reason := "Reconciliation: deployment was stuck in an active state beyond the stale threshold"
	tag, err := s.db.Exec(ctx,
		`UPDATE service_deployments SET
		 status = $1,
		 failure_reason = $2,
		 finished_at = NOW(),
		 duration_ms = EXTRACT(EPOCH FROM (NOW() - COALESCE(started_at, created_at))) * 1000
		 WHERE status IN ($3, $4, $5, $6, $7)
		   AND created_at < NOW() - $8::interval`,
		models.DeployStatusFailed, reason,
		models.DeployStatusQueued, models.DeployStatusCloning, models.DeployStatusBuilding,
		models.DeployStatusStarting, models.DeployStatusHealthChecking,
		staleDuration.String(),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to reconcile orphaned deployments: %w", err)
	}

	count := int(tag.RowsAffected())
	if count > 0 {
		slog.Warn("reconciled orphaned service deployments", "count", count, "stale_threshold", staleDuration.String())
	}
	return count, nil
}

// UpdateServiceDeploymentImageDigest stores the immutable Docker image digest for rollback.
func (s *DeploymentService) UpdateServiceDeploymentImageDigest(ctx context.Context, serviceDeploymentID uuid.UUID, digest string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE service_deployments SET image_digest = $2 WHERE id = $1`,
		serviceDeploymentID, digest,
	)
	return err
}

// FailServiceDeployment marks a service deployment as failed and restores the service status.
// Used when enqueue fails or for cleanup of zombie records.
func (s *DeploymentService) FailServiceDeployment(ctx context.Context, serviceDeploymentID uuid.UUID, reason string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	now := time.Now()
	_, err = tx.Exec(ctx,
		`UPDATE service_deployments SET
		 status = $2, failure_reason = $3, finished_at = $4,
		 duration_ms = EXTRACT(EPOCH FROM ($4 - COALESCE(started_at, created_at))) * 1000
		 WHERE id = $1 AND status NOT IN ($5, $6, $7, $8)`,
		serviceDeploymentID, models.DeployStatusFailed, reason, now,
		models.DeployStatusRunning, models.DeployStatusStopped, models.DeployStatusCrashed, models.DeployStatusFailed,
	)
	if err != nil {
		return fmt.Errorf("failed to fail service deployment: %w", err)
	}

	// Restore the service status: if it was running before this deploy, restore to running.
	// Otherwise set to failed.
	var serviceID uuid.UUID
	_ = tx.QueryRow(ctx, "SELECT service_id FROM service_deployments WHERE id = $1", serviceDeploymentID).Scan(&serviceID)
	if serviceID != uuid.Nil {
		var prevRunningID *uuid.UUID
		_ = tx.QueryRow(ctx,
			`SELECT id FROM service_deployments WHERE service_id = $1 AND status = $2 AND id != $3 ORDER BY deploy_number DESC LIMIT 1`,
			serviceID, models.DeployStatusRunning, serviceDeploymentID,
		).Scan(&prevRunningID)

		if prevRunningID != nil {
			_, _ = tx.Exec(ctx, "UPDATE services SET status = $1, updated_at = $2 WHERE id = $3",
				models.ServiceStatusRunning, now, serviceID)
		} else {
			_, _ = tx.Exec(ctx, "UPDATE services SET status = $1, updated_at = $2 WHERE id = $3",
				models.ServiceStatusFailed, now, serviceID)
		}
	}

	return tx.Commit(ctx)
}

