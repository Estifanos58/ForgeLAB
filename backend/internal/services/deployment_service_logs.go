package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/forgelab/backend/internal/models"
)

// LogQueryOptions specifies parameters for querying historical deployment and service logs.
type LogQueryOptions struct {
	ServiceID *uuid.UUID
	Phase     string // "all", "deployment", "production", or exact phase name
	AfterID   int64  // cursor for forward pagination (id > AfterID)
	BeforeID  int64  // cursor for backward pagination (id < BeforeID)
	Limit     int    // bounded limit (default 1000, max 5000)
}

func buildPhaseClause(phase string, argIndex *int, args *[]interface{}) string {
	lower := strings.ToLower(strings.TrimSpace(phase))
	if lower == "" || lower == "all" {
		return ""
	}
	if lower == "deployment" {
		return " AND phase IN ('source', 'build', 'startup', 'health')"
	}
	if lower == "production" {
		return " AND phase = 'runtime'"
	}
	clause := fmt.Sprintf(" AND phase = $%d", *argIndex)
	*args = append(*args, lower)
	*argIndex++
	return clause
}

func scanLogs(rows pgx.Rows) ([]*models.DeploymentLog, error) {
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
	return logs, rows.Err()
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
	// Clamp single log message size to 64KB to prevent unbounded payload growth
	const maxLogMsgBytes = 64 * 1024
	if len(message) > maxLogMsgBytes {
		message = message[:maxLogMsgBytes] + " ... [truncated: exceeded 64KB log size limit]"
	}

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

// GetDeploymentLogs retrieves recent logs for a release deployment with bounded limit (default 2000, max 5000).
func (s *DeploymentService) GetDeploymentLogs(ctx context.Context, deploymentID uuid.UUID, pagination ...int) ([]*models.DeploymentLog, error) {
	limit := 2000
	if len(pagination) > 0 && pagination[0] > 0 {
		limit = pagination[0]
	}
	return s.GetDeploymentLogsWithOptions(ctx, deploymentID, LogQueryOptions{Limit: limit})
}

// GetDeploymentLogsWithOptions retrieves logs for a release deployment with cursor-based pagination and filtering.
func (s *DeploymentService) GetDeploymentLogsWithOptions(ctx context.Context, deploymentID uuid.UUID, opts LogQueryOptions) ([]*models.DeploymentLog, error) {
	if s.db == nil {
		return []*models.DeploymentLog{}, nil
	}
	limit := opts.Limit
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}

	argIndex := 1
	var args []interface{}

	baseWhere := fmt.Sprintf("deployment_id = $%d", argIndex)
	args = append(args, deploymentID)
	argIndex++

	if opts.ServiceID != nil {
		baseWhere += fmt.Sprintf(" AND service_id = $%d", argIndex)
		args = append(args, *opts.ServiceID)
		argIndex++
	}

	phaseClause := buildPhaseClause(opts.Phase, &argIndex, &args)
	fullWhere := baseWhere + phaseClause

	var query string
	if opts.AfterID > 0 {
		query = fmt.Sprintf(
			`SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
			 FROM deployment_logs
			 WHERE %s AND id > $%d
			 ORDER BY id ASC LIMIT $%d`,
			fullWhere, argIndex, argIndex+1,
		)
		args = append(args, opts.AfterID, limit)
	} else if opts.BeforeID > 0 {
		query = fmt.Sprintf(
			`SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
			 FROM (
				 SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
				 FROM deployment_logs
				 WHERE %s AND id < $%d
				 ORDER BY id DESC LIMIT $%d
			 ) sub
			 ORDER BY id ASC`,
			fullWhere, argIndex, argIndex+1,
		)
		args = append(args, opts.BeforeID, limit)
	} else {
		// Return recent logs chronologically
		query = fmt.Sprintf(
			`SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
			 FROM (
				 SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
				 FROM deployment_logs
				 WHERE %s
				 ORDER BY id DESC LIMIT $%d
			 ) sub
			 ORDER BY id ASC`,
			fullWhere, argIndex,
		)
		args = append(args, limit)
	}

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment logs: %w", err)
	}
	return scanLogs(rows)
}

// GetServiceLogs retrieves logs scoped strictly to a specific release and service.
func (s *DeploymentService) GetServiceLogs(ctx context.Context, deploymentID, serviceID uuid.UUID) ([]*models.DeploymentLog, error) {
	return s.GetDeploymentLogsWithOptions(ctx, deploymentID, LogQueryOptions{ServiceID: &serviceID, Limit: 2000})
}

// VerifyServiceBelongsToProject verifies whether a serviceID belongs to the given projectID.
func (s *DeploymentService) VerifyServiceBelongsToProject(ctx context.Context, serviceID, projectID uuid.UUID) (bool, error) {
	if s.db == nil {
		return false, nil
	}
	var count int
	err := s.db.QueryRow(ctx, "SELECT COUNT(*) FROM services WHERE id = $1 AND project_id = $2", serviceID, projectID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to verify service project association: %w", err)
	}
	return count > 0, nil
}

// GetServiceDeploymentLogs retrieves recent logs scoped strictly to an individual service deployment record.
func (s *DeploymentService) GetServiceDeploymentLogs(ctx context.Context, serviceDeploymentID uuid.UUID, pagination ...int) ([]*models.DeploymentLog, error) {
	limit := 2000
	if len(pagination) > 0 && pagination[0] > 0 {
		limit = pagination[0]
	}
	return s.GetServiceDeploymentLogsWithOptions(ctx, serviceDeploymentID, LogQueryOptions{Limit: limit})
}

// GetServiceDeploymentLogsWithOptions retrieves logs for an individual service deployment with cursor pagination.
func (s *DeploymentService) GetServiceDeploymentLogsWithOptions(ctx context.Context, serviceDeploymentID uuid.UUID, opts LogQueryOptions) ([]*models.DeploymentLog, error) {
	if s.db == nil {
		return []*models.DeploymentLog{}, nil
	}
	limit := opts.Limit
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}

	argIndex := 1
	var args []interface{}

	baseWhere := fmt.Sprintf("service_deployment_id = $%d", argIndex)
	args = append(args, serviceDeploymentID)
	argIndex++

	phaseClause := buildPhaseClause(opts.Phase, &argIndex, &args)
	fullWhere := baseWhere + phaseClause

	var query string
	if opts.AfterID > 0 {
		query = fmt.Sprintf(
			`SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
			 FROM deployment_logs
			 WHERE %s AND id > $%d
			 ORDER BY id ASC LIMIT $%d`,
			fullWhere, argIndex, argIndex+1,
		)
		args = append(args, opts.AfterID, limit)
	} else if opts.BeforeID > 0 {
		query = fmt.Sprintf(
			`SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
			 FROM (
				 SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
				 FROM deployment_logs
				 WHERE %s AND id < $%d
				 ORDER BY id DESC LIMIT $%d
			 ) sub
			 ORDER BY id ASC`,
			fullWhere, argIndex, argIndex+1,
		)
		args = append(args, opts.BeforeID, limit)
	} else {
		query = fmt.Sprintf(
			`SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
			 FROM (
				 SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
				 FROM deployment_logs
				 WHERE %s
				 ORDER BY id DESC LIMIT $%d
			 ) sub
			 ORDER BY id ASC`,
			fullWhere, argIndex,
		)
		args = append(args, limit)
	}

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get service deployment logs: %w", err)
	}
	return scanLogs(rows)
}

// GetDeploymentLogsAfter retrieves logs for a deployment created after a given log ID sequence.
func (s *DeploymentService) GetDeploymentLogsAfter(ctx context.Context, deploymentID uuid.UUID, serviceID *uuid.UUID, afterID int64, limit int) ([]*models.DeploymentLog, error) {
	return s.GetDeploymentLogsWithOptions(ctx, deploymentID, LogQueryOptions{
		ServiceID: serviceID,
		AfterID:   afterID,
		Limit:     limit,
	})
}

// GetProjectLogs retrieves logs across all services and releases belonging to a project.
func (s *DeploymentService) GetProjectLogs(ctx context.Context, projectID uuid.UUID, opts LogQueryOptions) ([]*models.DeploymentLog, error) {
	if s.db == nil {
		return []*models.DeploymentLog{}, nil
	}
	limit := opts.Limit
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}

	argIndex := 1
	var args []interface{}

	baseWhere := fmt.Sprintf("(service_id IN (SELECT id FROM services WHERE project_id = $%d) OR deployment_id IN (SELECT id FROM deployments WHERE project_id = $%d))", argIndex, argIndex)
	args = append(args, projectID)
	argIndex++

	if opts.ServiceID != nil {
		baseWhere += fmt.Sprintf(" AND service_id = $%d", argIndex)
		args = append(args, *opts.ServiceID)
		argIndex++
	}

	phaseClause := buildPhaseClause(opts.Phase, &argIndex, &args)
	fullWhere := baseWhere + phaseClause

	var query string
	if opts.AfterID > 0 {
		query = fmt.Sprintf(
			`SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
			 FROM deployment_logs
			 WHERE %s AND id > $%d
			 ORDER BY id ASC LIMIT $%d`,
			fullWhere, argIndex, argIndex+1,
		)
		args = append(args, opts.AfterID, limit)
	} else if opts.BeforeID > 0 {
		query = fmt.Sprintf(
			`SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
			 FROM (
				 SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
				 FROM deployment_logs
				 WHERE %s AND id < $%d
				 ORDER BY id DESC LIMIT $%d
			 ) sub
			 ORDER BY id ASC`,
			fullWhere, argIndex, argIndex+1,
		)
		args = append(args, opts.BeforeID, limit)
	} else {
		query = fmt.Sprintf(
			`SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
			 FROM (
				 SELECT id, deployment_id, service_deployment_id, service_id, timestamp, phase, stream, message
				 FROM deployment_logs
				 WHERE %s
				 ORDER BY id DESC LIMIT $%d
			 ) sub
			 ORDER BY id ASC`,
			fullWhere, argIndex,
		)
		args = append(args, limit)
	}

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get project logs: %w", err)
	}
	return scanLogs(rows)
}

// GetProjectLogsAfter retrieves logs for a project created after a given log ID sequence (used for WS replay).
func (s *DeploymentService) GetProjectLogsAfter(ctx context.Context, projectID uuid.UUID, afterID int64, limit int) ([]*models.DeploymentLog, error) {
	return s.GetProjectLogs(ctx, projectID, LogQueryOptions{
		AfterID: afterID,
		Limit:   limit,
	})
}

// GetServiceDeploymentLogsAfter retrieves logs for a service deployment after a given sequence ID.
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
		return nil, fmt.Errorf("failed to get service deployment logs after sequence %d: %w", afterID, err)
	}
	return scanLogs(rows)
}

// PruneLogs deletes old deployment log records based on retention days and max log limits per deployment.
// If retentionDays > 0, logs older than retentionDays are deleted.
// If maxLogsPerDeployment > 0, older logs exceeding the per-deployment count limit are deleted.
// Returns the total number of deleted log records.
func (s *DeploymentService) PruneLogs(ctx context.Context, retentionDays int, maxLogsPerDeployment int) (int64, error) {
	if s.db == nil {
		return 0, nil
	}
	var totalPruned int64

	if retentionDays > 0 {
		cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
		tag, err := s.db.Exec(ctx, "DELETE FROM deployment_logs WHERE timestamp < $1", cutoff)
		if err != nil {
			return totalPruned, fmt.Errorf("failed to prune expired logs: %w", err)
		}
		totalPruned += tag.RowsAffected()
	}

	if maxLogsPerDeployment > 0 {
		query := `
			WITH OverLimitDeployments AS (
				SELECT deployment_id
				FROM deployment_logs
				GROUP BY deployment_id
				HAVING COUNT(*) > $1
			),
			RankedLogs AS (
				SELECT dl.id, ROW_NUMBER() OVER (
					PARTITION BY dl.deployment_id
					ORDER BY dl.id DESC
				) AS rank
				FROM deployment_logs dl
				JOIN OverLimitDeployments old ON dl.deployment_id = old.deployment_id
			)
			DELETE FROM deployment_logs
			WHERE id IN (
				SELECT id FROM RankedLogs WHERE rank > $1
			)
		`
		tag, err := s.db.Exec(ctx, query, maxLogsPerDeployment)
		if err != nil {
			return totalPruned, fmt.Errorf("failed to prune oversized deployment logs: %w", err)
		}
		totalPruned += tag.RowsAffected()
	}

	return totalPruned, nil
}
