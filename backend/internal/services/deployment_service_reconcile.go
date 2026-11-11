package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/forgelab/backend/internal/models"
)

// DeploymentQueueReconciler abstracts queue queries and enqueueing for reconciliation.
type DeploymentQueueReconciler interface {
	IsJobEnqueuedOrActive(ctx context.Context, jobID uuid.UUID) (bool, error)
	EnqueueServiceDeployment(ctx context.Context, serviceDeploymentID uuid.UUID) error
	EnqueueDeployment(ctx context.Context, deploymentID uuid.UUID) error
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
		   AND deployment_id IS NULL
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
		if err := rows.Scan(&it.id, &it.serviceID, &it.createdAt); err != nil {
			return 0, fmt.Errorf("failed to scan queued service deployment: %w", err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("error iterating queued service deployments: %w", err)
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

		attempts, err := q.GetAttempts(ctx, item.id)
		if err != nil {
			slog.Warn("reconciliation: error getting job attempts", "service_deployment_id", item.id, "error", err)
			continue
		}

		if attempts < maxRetries {
			_, _ = q.IncrementAttempts(ctx, item.id)
			if err := q.EnqueueServiceDeployment(ctx, item.id); err != nil {
				slog.Error("reconciliation: failed to re-enqueue service deployment", "service_deployment_id", item.id, "error", err)
				continue
			}
			slog.Info("reconciliation: re-enqueued orphan queued service deployment", "service_deployment_id", item.id, "attempt", attempts+1)
			reconciled++
		} else {
			reason := fmt.Sprintf("Deployment failed: job lost and exceeded max retries (%d)", maxRetries)
			if err := s.FailServiceDeployment(ctx, item.id, reason); err != nil {
				slog.Error("reconciliation: failed to mark dead job failed", "service_deployment_id", item.id, "error", err)
				continue
			}
			slog.Warn("reconciliation: marked dead queued deployment as failed", "service_deployment_id", item.id, "retries", attempts)
			reconciled++
		}
	}

	return reconciled, nil
}

// ReconcileOrphanedDeploymentsWithQueue finds service deployments stuck in active states for too long
// and marks them as failed. This prevents permanently-queued records when Redis enqueue fails.
func (s *DeploymentService) ReconcileOrphanedDeploymentsWithQueue(ctx context.Context, q DeploymentQueueReconciler, staleDuration time.Duration, maxRetries int) (int, error) {
	if s.db == nil {
		return 0, nil
	}

	rows, err := s.db.Query(ctx,
		`SELECT id, service_id, status, created_at, started_at
		 FROM service_deployments
		 WHERE status IN ($1, $2, $3, $4)
		   AND deployment_id IS NULL
		   AND COALESCE(started_at, created_at) < NOW() - $5::interval
		 ORDER BY created_at ASC`,
		models.DeployStatusCloning, models.DeployStatusBuilding,
		models.DeployStatusStarting, models.DeployStatusHealthChecking,
		staleDuration.String(),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to query stale service deployments: %w", err)
	}
	defer rows.Close()

	type staleItem struct {
		id        uuid.UUID
		serviceID uuid.UUID
		status    string
	}
	var items []staleItem
	for rows.Next() {
		var it staleItem
		var created, started *time.Time
		if err := rows.Scan(&it.id, &it.serviceID, &it.status, &created, &started); err != nil {
			return 0, fmt.Errorf("failed to scan stale service deployment: %w", err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("error iterating stale service deployments: %w", err)
	}

	reconciled := 0
	for _, item := range items {
		if q != nil {
			active, err := q.IsJobEnqueuedOrActive(ctx, item.id)
			if err != nil {
				slog.Warn("reconciliation: error checking job activity", "service_deployment_id", item.id, "error", err)
			} else if active {
				continue
			}
		}

		reason := fmt.Sprintf("Deployment timed out: stuck in %s for more than %s", item.status, staleDuration)
		if err := s.FailServiceDeployment(ctx, item.id, reason); err != nil {
			slog.Error("reconciliation: failed to fail stale service deployment", "service_deployment_id", item.id, "error", err)
			continue
		}
		slog.Warn("reconciliation: timed out stale service deployment", "service_deployment_id", item.id, "previous_status", item.status)
		reconciled++
	}

	return reconciled, nil
}

// IsServiceDeploymentSuperseded checks if a service deployment has been superseded by a newer deployment.
func (s *DeploymentService) IsServiceDeploymentSuperseded(ctx context.Context, serviceID uuid.UUID, deployNumber int) (bool, error) {
	if s.db == nil {
		return false, nil
	}
	var count int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM service_deployments
		 WHERE service_id = $1 AND deploy_number > $2`,
		serviceID, deployNumber,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// IsDeploymentSuperseded checks if a project deployment has been superseded by a newer deployment.
func (s *DeploymentService) IsDeploymentSuperseded(ctx context.Context, projectID uuid.UUID, deployNumber int) (bool, error) {
	if s.db == nil {
		return false, nil
	}
	var count int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM deployments
		 WHERE project_id = $1 AND deploy_number > $2`,
		projectID, deployNumber,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ReconcileOrphanedDeployments delegates to ReconcileOrphanedDeploymentsWithQueue with nil queue.
func (s *DeploymentService) ReconcileOrphanedDeployments(ctx context.Context, staleDuration time.Duration) (int, error) {
	return s.ReconcileOrphanedDeploymentsWithQueue(ctx, nil, staleDuration, 3)
}

// CancelServiceDeployment safely cancels a queued or in-progress service deployment.
// It verifies atomically that the service deployment belongs to the specified service and project.
// It fails if the deployment is already running (promoted) or already in a terminal state.
func (s *DeploymentService) CancelServiceDeployment(ctx context.Context, projectID, serviceID, serviceDeploymentID uuid.UUID) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var currentStatus string
	var verifiedServiceID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT sd.status, sd.service_id
		FROM service_deployments sd
		JOIN services s ON sd.service_id = s.id
		WHERE sd.id = $1 AND sd.service_id = $2 AND s.project_id = $3
		FOR UPDATE OF sd
	`, serviceDeploymentID, serviceID, projectID).Scan(&currentStatus, &verifiedServiceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDeploymentNotFound
		}
		return fmt.Errorf("failed to fetch service deployment: %w", err)
	}

	if currentStatus == models.DeployStatusRunning {
		return ErrCannotCancelRunningDeployment
	}

	if models.IsDeploymentTerminalStatus(currentStatus) {
		return ErrDeploymentAlreadyTerminal
	}

	now := time.Now()
	reason := "Deployment cancelled by user"
	_, err = tx.Exec(ctx,
		`UPDATE service_deployments SET
		 status = $2, failure_reason = $3, finished_at = $4,
		 duration_ms = EXTRACT(EPOCH FROM ($4 - COALESCE(started_at, created_at))) * 1000
		 WHERE id = $1`,
		serviceDeploymentID, models.DeployStatusFailed, reason, now,
	)
	if err != nil {
		return fmt.Errorf("failed to cancel service deployment: %w", err)
	}

	// Restore service status if appropriate
	var prevRunningID *uuid.UUID
	err = tx.QueryRow(ctx,
		`SELECT id FROM service_deployments WHERE service_id = $1 AND status = $2 AND id != $3 ORDER BY deploy_number DESC LIMIT 1`,
		serviceID, models.DeployStatusRunning, serviceDeploymentID,
	).Scan(&prevRunningID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("failed to check prior running service deployment: %w", err)
	}

	if prevRunningID != nil {
		if _, err := tx.Exec(ctx, "UPDATE services SET status = $1, updated_at = $2 WHERE id = $3",
			models.ServiceStatusRunning, now, serviceID); err != nil {
			return fmt.Errorf("failed to restore running service status: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, "UPDATE services SET status = $1, updated_at = $2 WHERE id = $3",
			models.ServiceStatusFailed, now, serviceID); err != nil {
			return fmt.Errorf("failed to mark service status failed: %w", err)
		}
	}

	// Log the cancellation
	if _, err := tx.Exec(ctx,
		`INSERT INTO deployment_logs (deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		nil, serviceDeploymentID, serviceID, now, models.LogPhaseStartup, models.LogStreamSystem, reason,
	); err != nil {
		return fmt.Errorf("failed to log service deployment cancellation: %w", err)
	}

	return tx.Commit(ctx)
}

// CancelDeployment safely cancels a queued or in-progress project deployment.
// It verifies atomically that the deployment belongs to the specified project.
// It fails if the deployment is already running (promoted) or already in a terminal state.
func (s *DeploymentService) CancelDeployment(ctx context.Context, projectID, deploymentID uuid.UUID) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var currentStatus string
	var verifiedProjectID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT status, project_id
		FROM deployments
		WHERE id = $1 AND project_id = $2
		FOR UPDATE
	`, deploymentID, projectID).Scan(&currentStatus, &verifiedProjectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDeploymentNotFound
		}
		return fmt.Errorf("failed to fetch deployment: %w", err)
	}

	if currentStatus == models.DeployStatusRunning {
		return ErrCannotCancelRunningDeployment
	}

	if models.IsDeploymentTerminalStatus(currentStatus) {
		return ErrDeploymentAlreadyTerminal
	}

	now := time.Now()
	reason := "Deployment cancelled by user"
	_, err = tx.Exec(ctx,
		`UPDATE deployments SET
		 status = $2, failure_reason = $3, finished_at = $4
		 WHERE id = $1`,
		deploymentID, models.DeployStatusFailed, reason, now,
	)
	if err != nil {
		return fmt.Errorf("failed to cancel deployment: %w", err)
	}

	// Also mark any child service_deployments as failed if active
	if _, err := tx.Exec(ctx,
		`UPDATE service_deployments SET
		 status = $2, failure_reason = $3, finished_at = $4,
		 duration_ms = EXTRACT(EPOCH FROM ($4 - COALESCE(started_at, created_at))) * 1000
		 WHERE deployment_id = $1 AND status NOT IN ($5, $6, $7, $8)`,
		deploymentID, models.DeployStatusFailed, reason, now,
		models.DeployStatusRunning, models.DeployStatusStopped, models.DeployStatusCrashed, models.DeployStatusFailed,
	); err != nil {
		return fmt.Errorf("failed to cancel child service deployments: %w", err)
	}

	// Update project status
	var prevRunningID *uuid.UUID
	err = tx.QueryRow(ctx,
		`SELECT id FROM deployments WHERE project_id = $1 AND status = $2 AND id != $3 ORDER BY deploy_number DESC LIMIT 1`,
		projectID, models.DeployStatusRunning, deploymentID,
	).Scan(&prevRunningID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("failed to check prior running deployment: %w", err)
	}

	if prevRunningID != nil {
		if _, err := tx.Exec(ctx, "UPDATE projects SET status = $1, updated_at = $2 WHERE id = $3",
			models.ProjectStatusRunning, now, projectID); err != nil {
			return fmt.Errorf("failed to restore running project status: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, "UPDATE projects SET status = $1, updated_at = $2 WHERE id = $3",
			models.ProjectStatusFailed, now, projectID); err != nil {
			return fmt.Errorf("failed to mark project status failed: %w", err)
		}
	}

	// Log the cancellation
	if _, err := tx.Exec(ctx,
		`INSERT INTO deployment_logs (deployment_id, service_id, timestamp, phase, stream, message)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		deploymentID, nil, now, models.LogPhaseStartup, models.LogStreamSystem, reason,
	); err != nil {
		return fmt.Errorf("failed to log deployment cancellation: %w", err)
	}

	return tx.Commit(ctx)
}

// CancelAllProjectDeployments cancels all queued or in-progress parent and service deployments for a project.
func (s *DeploymentService) CancelAllProjectDeployments(ctx context.Context, projectID uuid.UUID) error {
	now := time.Now()
	reason := "Project deletion in progress"

	// Mark queued/active project deployments as failed
	_, err := s.db.Exec(ctx,
		`UPDATE deployments SET
		 status = $1, failure_reason = $2, finished_at = $3,
		 duration_ms = EXTRACT(EPOCH FROM ($3 - COALESCE(started_at, created_at))) * 1000
		 WHERE project_id = $4 AND status IN ($5, $6, $7, $8, $9)`,
		models.DeployStatusFailed, reason, now, projectID,
		models.DeployStatusQueued, models.DeployStatusCloning, models.DeployStatusBuilding,
		models.DeployStatusStarting, models.DeployStatusHealthChecking,
	)
	if err != nil {
		return fmt.Errorf("failed to cancel project deployments: %w", err)
	}

	// Mark queued/active service deployments as failed
	_, err = s.db.Exec(ctx,
		`UPDATE service_deployments SET
		 status = $1, failure_reason = $2, finished_at = $3,
		 duration_ms = EXTRACT(EPOCH FROM ($3 - COALESCE(started_at, created_at))) * 1000
		 WHERE service_id IN (SELECT id FROM services WHERE project_id = $4)
		   AND status IN ($5, $6, $7, $8, $9)`,
		models.DeployStatusFailed, reason, now, projectID,
		models.DeployStatusQueued, models.DeployStatusCloning, models.DeployStatusBuilding,
		models.DeployStatusStarting, models.DeployStatusHealthChecking,
	)
	if err != nil {
		return fmt.Errorf("failed to cancel service deployments: %w", err)
	}

	return nil
}
