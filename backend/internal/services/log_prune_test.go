package services_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/services"
)

func TestDeploymentService_PruneLogs(t *testing.T) {
	pool, _ := connectTestDB(t)
	if pool == nil {
		t.Skip("PostgreSQL not available for prune test")
		return
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	deploymentService := services.NewDeploymentService(pool)

	userID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name)
		VALUES ($1, $2, 'hash123', 'Prune User')
	`, userID, fmt.Sprintf("prune-user-%s@test.local", userID.String()[:8]))
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	}()

	projID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO projects (id, owner_id, name, slug, status, created_at, updated_at)
		VALUES ($1, $2, 'Prune Proj', $3, 'running', NOW(), NOW())
	`, projID, userID, fmt.Sprintf("prune-%s", projID.String()[:8]))
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM projects WHERE id = $1", projID)
	}()

	deployID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO deployments (id, project_id, deploy_number, commit_sha, status, build_strategy, created_at)
		VALUES ($1, $2, 1, 'abc1234', 'running', 'auto', NOW())
	`, deployID, projID)
	require.NoError(t, err)

	// 1. Insert 5 logs that are 45 days old, and 5 logs that are fresh (now)
	oldTimestamp := time.Now().UTC().AddDate(0, 0, -45)
	for i := 0; i < 5; i++ {
		_, err := pool.Exec(ctx, `
			INSERT INTO deployment_logs (deployment_id, timestamp, phase, stream, message)
			VALUES ($1, $2, 'runtime', 'stdout', $3)
		`, deployID, oldTimestamp, fmt.Sprintf("old log line %d", i))
		require.NoError(t, err)
	}

	for i := 0; i < 5; i++ {
		_, err := pool.Exec(ctx, `
			INSERT INTO deployment_logs (deployment_id, timestamp, phase, stream, message)
			VALUES ($1, $2, 'runtime', 'stdout', $3)
		`, deployID, time.Now().UTC(), fmt.Sprintf("fresh log line %d", i))
		require.NoError(t, err)
	}

	// 2. Prune logs older than 30 days (maxLogsPerDeployment=0 -> no count limit)
	prunedCount, err := deploymentService.PruneLogs(ctx, 30, 0)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, prunedCount, int64(5), "must prune at least 5 expired logs")

	// Verify only 5 fresh logs remain for this deployment
	var remainingCount int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM deployment_logs WHERE deployment_id = $1", deployID).Scan(&remainingCount)
	require.NoError(t, err)
	assert.Equal(t, 5, remainingCount)

	// 3. Test per-deployment count limit
	// Insert 15 more fresh logs (total for this deployment now 20 logs)
	for i := 0; i < 15; i++ {
		_, err := pool.Exec(ctx, `
			INSERT INTO deployment_logs (deployment_id, timestamp, phase, stream, message)
			VALUES ($1, $2, 'runtime', 'stdout', $3)
		`, deployID, time.Now().UTC(), fmt.Sprintf("new log line %d", i))
		require.NoError(t, err)
	}

	// Prune to maximum 8 logs per deployment
	prunedCount, err = deploymentService.PruneLogs(ctx, 0, 8)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, prunedCount, int64(12), "must prune at least 12 excess logs")

	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM deployment_logs WHERE deployment_id = $1", deployID).Scan(&remainingCount)
	require.NoError(t, err)
	assert.Equal(t, 8, remainingCount, "must leave exactly 8 newest logs for this deployment")
}
