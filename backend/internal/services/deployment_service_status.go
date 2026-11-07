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

// UpdateServiceDeploymentVolumes persists updated volume configurations (e.g. after security sanitization).
func (s *DeploymentService) UpdateServiceDeploymentVolumes(ctx context.Context, serviceDeploymentID uuid.UUID, volumes []models.VolumeMountConfig) error {
	volsJSON, err := json.Marshal(volumes)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx,
		`UPDATE service_deployments SET volumes = $2 WHERE id = $1`,
		serviceDeploymentID, volsJSON,
	)
	return err
}

// UpdateServiceDeploymentImageDigest stores the immutable Docker image digest for rollback.
func (s *DeploymentService) UpdateServiceDeploymentImageDigest(ctx context.Context, serviceDeploymentID uuid.UUID, digest string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE service_deployments SET image_digest = $2 WHERE id = $1`,
		serviceDeploymentID, digest,
	)
	return err
}

// UpdateDeploymentImageDigest stores the immutable Docker image digest for project rollback.
func (s *DeploymentService) UpdateDeploymentImageDigest(ctx context.Context, deploymentID uuid.UUID, digest string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE deployments SET image_digest = $2 WHERE id = $1`,
		deploymentID, digest,
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
