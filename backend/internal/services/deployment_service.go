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
	db *pgxpool.Pool
}

// NewDeploymentService creates a new DeploymentService.
func NewDeploymentService(db *pgxpool.Pool) *DeploymentService {
	return &DeploymentService{db: db}
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
		`SELECT id, name, role, build_strategy, build_command, start_command, runtime_type, internal_port 
		 FROM services WHERE project_id = $1`,
		project.ID,
	)
	var svcList []models.Service
	if err == nil {
		for rows.Next() {
			var s models.Service
			if err := rows.Scan(&s.ID, &s.Name, &s.Role, &s.BuildStrategy, &s.BuildCommand, &s.StartCommand, &s.RuntimeType, &s.InternalPort); err == nil {
				svcList = append(svcList, s)
			}
		}
		rows.Close()
	}

	for _, s := range svcList {
		svcDeployID := uuid.New()
		svcTag := fmt.Sprintf("forgelab/%s/%s:%d", project.ID, s.Name, deployNumber)
		_, _ = tx.Exec(ctx,
			`INSERT INTO service_deployments (
				id, deployment_id, service_id, status, image_tag, build_strategy,
				build_command, start_command, runtime_type, internal_port, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			svcDeployID, deployment.ID, s.ID, models.DeployStatusQueued, svcTag,
			s.BuildStrategy, s.BuildCommand, s.StartCommand, s.RuntimeType, s.InternalPort, now,
		)
		_, _ = tx.Exec(ctx,
			"UPDATE services SET status = $1, current_service_deployment_id = $2, updated_at = $3 WHERE id = $4",
			models.DeployStatusQueued, svcDeployID, now, s.ID,
		)
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

// CreateServiceDeployment creates a new deployment record targeting a single specific service.
func (s *DeploymentService) CreateServiceDeployment(ctx context.Context, project *models.Project, targetService *models.Service) (*models.Deployment, error) {
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
	imageTag := fmt.Sprintf("forgelab/%s/%s:%d", project.ID, targetService.Name, deployNumber)

	buildStrategy := targetService.BuildStrategy
	if buildStrategy == "" {
		buildStrategy = project.BuildStrategy
	}
	buildCommand := targetService.BuildCommand
	if buildCommand == "" {
		buildCommand = project.BuildCommand
	}
	startCommand := targetService.StartCommand
	if startCommand == "" {
		startCommand = project.StartCommand
	}
	runtimeType := targetService.RuntimeType
	if runtimeType == "" {
		runtimeType = project.RuntimeType
	}
	internalPort := targetService.InternalPort
	if internalPort <= 0 {
		internalPort = project.InternalPort
	}
	healthStrategy := targetService.HealthStrategy
	if healthStrategy == "" {
		healthStrategy = project.HealthStrategy
	}

	deployment := &models.Deployment{
		ID:             uuid.New(),
		ProjectID:      project.ID,
		DeployNumber:   deployNumber,
		Status:         models.DeployStatusQueued,
		Branch:         project.Branch,
		ImageTag:       &imageTag,
		BuildStrategy:  buildStrategy,
		BuildCommand:   buildCommand,
		StartCommand:   startCommand,
		RuntimeType:    runtimeType,
		InternalPort:   internalPort,
		HealthStrategy: healthStrategy,
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

	// Insert single service deployment for targeted service
	svcDeployID := uuid.New()
	_, err = tx.Exec(ctx,
		`INSERT INTO service_deployments (
			id, deployment_id, service_id, status, image_tag, build_strategy,
			build_command, start_command, runtime_type, internal_port, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		svcDeployID, deployment.ID, targetService.ID, models.DeployStatusQueued, imageTag,
		buildStrategy, buildCommand, startCommand, runtimeType, internalPort, now,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create service deployment: %w", err)
	}

	// Update targeted service status to queued/deploying
	_, err = tx.Exec(ctx,
		"UPDATE services SET status = $1, current_service_deployment_id = $2, updated_at = $3 WHERE id = $4",
		models.DeployStatusQueued, svcDeployID, now, targetService.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update target service status: %w", err)
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
		return nil, fmt.Errorf("failed to commit service deployment: %w", err)
	}

	deployment.ServiceDeployments = []*models.ServiceDeployment{
		{
			ID:            svcDeployID,
			DeploymentID:  deployment.ID,
			ServiceID:     targetService.ID,
			ServiceName:   targetService.Name,
			Status:        models.DeployStatusQueued,
			ImageTag:      &imageTag,
			InternalPort:  internalPort,
			BuildStrategy: buildStrategy,
			BuildCommand:  buildCommand,
			StartCommand:  startCommand,
			RuntimeType:   runtimeType,
			CreatedAt:     now,
		},
	}

	slog.Info("service deployment created",
		"deployment_id", deployment.ID,
		"project_id", project.ID,
		"service_id", targetService.ID,
		"service_name", targetService.Name,
		"deploy_number", deployNumber,
	)

	return deployment, nil
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

// AddDeploymentLog adds a log entry for a deployment and returns the persisted record.
func (s *DeploymentService) AddDeploymentLog(ctx context.Context, deploymentID uuid.UUID, phase, stream, message string) (*models.DeploymentLog, error) {
	return s.AddDeploymentServiceLog(ctx, deploymentID, nil, phase, stream, message)
}

// AddDeploymentServiceLog adds a log entry scoped to a specific service.
func (s *DeploymentService) AddDeploymentServiceLog(ctx context.Context, deploymentID uuid.UUID, serviceID *uuid.UUID, phase, stream, message string) (*models.DeploymentLog, error) {
	log := &models.DeploymentLog{
		DeploymentID: deploymentID,
		ServiceID:    serviceID,
		Phase:        phase,
		Stream:       stream,
		Message:      message,
	}
	err := s.db.QueryRow(ctx,
		`INSERT INTO deployment_logs (deployment_id, service_id, timestamp, phase, stream, message)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, timestamp`,
		deploymentID, serviceID, time.Now(), phase, stream, message,
	).Scan(&log.ID, &log.Timestamp)
	if err != nil {
		return nil, fmt.Errorf("failed to add deployment log: %w", err)
	}
	return log, nil
}

// GetDeploymentLogs retrieves logs for a deployment (global or all services).
func (s *DeploymentService) GetDeploymentLogs(ctx context.Context, deploymentID uuid.UUID) ([]*models.DeploymentLog, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, deployment_id, service_id, timestamp, phase, stream, message
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
		err := rows.Scan(&l.ID, &l.DeploymentID, &l.ServiceID, &l.Timestamp, &l.Phase, &l.Stream, &l.Message)
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

// GetServiceLogs retrieves logs scoped strictly to a specific service deployment.
func (s *DeploymentService) GetServiceLogs(ctx context.Context, deploymentID, serviceID uuid.UUID) ([]*models.DeploymentLog, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, deployment_id, service_id, timestamp, phase, stream, message
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
		err := rows.Scan(&l.ID, &l.DeploymentID, &l.ServiceID, &l.Timestamp, &l.Phase, &l.Stream, &l.Message)
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

// GetDeploymentLogsAfter retrieves logs for a deployment (optionally scoped to a service) created after a given log ID sequence.
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
			`SELECT id, deployment_id, service_id, timestamp, phase, stream, message
			 FROM deployment_logs 
			 WHERE deployment_id = $1 AND service_id = $2 AND id > $3
			 ORDER BY id ASC LIMIT $4`,
			deploymentID, *serviceID, afterID, limit,
		)
	} else {
		rows, err = s.db.Query(ctx,
			`SELECT id, deployment_id, service_id, timestamp, phase, stream, message
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
		if err := rows.Scan(&l.ID, &l.DeploymentID, &l.ServiceID, &l.Timestamp, &l.Phase, &l.Stream, &l.Message); err != nil {
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
		`SELECT sd.id, sd.deployment_id, sd.service_id, s.name, s.public_exposed, sd.status, sd.image_tag, sd.container_id,
		        sd.host_port, sd.internal_port, sd.build_strategy, sd.build_command, sd.start_command,
		        sd.runtime_type, sd.started_at, sd.built_at, sd.deployed_at, sd.finished_at,
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

	publicHost := os.Getenv("FORGELAB_PUBLIC_HOST")
	if publicHost == "" {
		publicHost = os.Getenv("PUBLIC_HOST")
	}
	if publicHost == "" {
		publicHost = "localhost"
	}

	var list []*models.ServiceDeployment
	for rows.Next() {
		sd := &models.ServiceDeployment{}
		var publicExposed bool
		err := rows.Scan(
			&sd.ID, &sd.DeploymentID, &sd.ServiceID, &sd.ServiceName, &publicExposed, &sd.Status, &sd.ImageTag,
			&sd.ContainerID, &sd.HostPort, &sd.InternalPort, &sd.BuildStrategy, &sd.BuildCommand,
			&sd.StartCommand, &sd.RuntimeType, &sd.StartedAt, &sd.BuiltAt, &sd.DeployedAt,
			&sd.FinishedAt, &sd.DurationMs, &sd.FailureReason, &sd.CreatedAt,
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
func (s *DeploymentService) UpdateServiceDeploymentStatus(ctx context.Context, deploymentID, serviceID uuid.UUID, newStatus string, failureReason *string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var currentStatus string
	err = tx.QueryRow(ctx,
		"SELECT status FROM service_deployments WHERE deployment_id = $1 AND service_id = $2 FOR UPDATE",
		deploymentID, serviceID,
	).Scan(&currentStatus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("failed to fetch service deployment status: %w", err)
	}

	if currentStatus != "" {
		if err := models.ValidateStateTransition(currentStatus, newStatus); err != nil {
			slog.Warn("skipping invalid service deployment status transition",
				"deployment_id", deploymentID,
				"service_id", serviceID,
				"from", currentStatus,
				"to", newStatus,
				"error", err,
			)
			return err
		}
	}

	now := time.Now()
	var deployedAt, finishedAt *time.Time
	if newStatus == models.DeployStatusRunning {
		deployedAt = &now
	}
	if newStatus == models.DeployStatusRunning || newStatus == models.DeployStatusFailed || newStatus == models.DeployStatusStopped {
		finishedAt = &now
	}

	_, err = tx.Exec(ctx,
		`UPDATE service_deployments SET
		 status = $3,
		 deployed_at = COALESCE($4, deployed_at),
		 finished_at = COALESCE($5, finished_at),
		 failure_reason = COALESCE($6, failure_reason)
		 WHERE deployment_id = $1 AND service_id = $2`,
		deploymentID, serviceID, newStatus, deployedAt, finishedAt, failureReason,
	)
	if err != nil {
		return fmt.Errorf("failed to update service deployment status: %w", err)
	}

	return tx.Commit(ctx)
}

// UpdateServiceDeploymentContainer records container ID and allocated host port for a service deployment.
func (s *DeploymentService) UpdateServiceDeploymentContainer(ctx context.Context, deploymentID, serviceID uuid.UUID, containerID string, hostPort *int) error {
	_, err := s.db.Exec(ctx,
		`UPDATE service_deployments SET
		 container_id = $3,
		 host_port = $4
		 WHERE deployment_id = $1 AND service_id = $2`,
		deploymentID, serviceID, containerID, hostPort,
	)
	return err
}
