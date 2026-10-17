package services_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

// TestLifecycle_ProjectStatusConstants verifies the new deleting status constant.
func TestLifecycle_ProjectStatusConstants(t *testing.T) {
	assert.Equal(t, "deleting", models.ProjectStatusDeleting)
}

// TestLifecycle_CalculateProjectStatus verifies that parent project status is accurately
// derived from individual coarse service statuses.
func TestLifecycle_CalculateProjectStatus(t *testing.T) {
	t.Run("empty services returns inactive", func(t *testing.T) {
		status := services.CalculateProjectStatus([]*models.Service{})
		assert.Equal(t, models.ProjectStatusInactive, status)
	})

	t.Run("all services running returns running", func(t *testing.T) {
		svcs := []*models.Service{
			{Status: models.ServiceStatusRunning},
			{Status: models.ServiceStatusRunning},
		}
		status := services.CalculateProjectStatus(svcs)
		assert.Equal(t, models.ProjectStatusRunning, status)
	})

	t.Run("any deploying service returns deploying", func(t *testing.T) {
		svcs := []*models.Service{
			{Status: models.ServiceStatusRunning},
			{Status: models.ServiceStatusDeploying},
		}
		status := services.CalculateProjectStatus(svcs)
		assert.Equal(t, models.ProjectStatusDeploying, status)
	})

	t.Run("all stopped services returns stopped", func(t *testing.T) {
		svcs := []*models.Service{
			{Status: models.ServiceStatusStopped},
			{Status: models.ServiceStatusStopped},
		}
		status := services.CalculateProjectStatus(svcs)
		assert.Equal(t, models.ProjectStatusStopped, status)
	})

	t.Run("mixed running and stopped returns partially_running", func(t *testing.T) {
		svcs := []*models.Service{
			{Status: models.ServiceStatusRunning},
			{Status: models.ServiceStatusStopped},
		}
		status := services.CalculateProjectStatus(svcs)
		assert.Equal(t, models.ProjectStatusPartiallyRunning, status)
	})

	t.Run("failed service with none running returns failed", func(t *testing.T) {
		svcs := []*models.Service{
			{Status: models.ServiceStatusStopped},
			{Status: models.ServiceStatusFailed},
		}
		status := services.CalculateProjectStatus(svcs)
		assert.Equal(t, models.ProjectStatusFailed, status)
	})

	t.Run("running services with completed jobs returns running", func(t *testing.T) {
		svcs := []*models.Service{
			{Status: models.ServiceStatusRunning, Classification: models.ClassificationApplication},
			{Status: models.ServiceStatusStopped, Classification: models.ClassificationJob},
		}
		status := services.CalculateProjectStatus(svcs)
		assert.Equal(t, models.ProjectStatusRunning, status)
	})
}

// TestLifecycle_PopulatePreviewURLRules verifies that preview URLs are strictly suppressed
// when services or projects are stopped, failed, or inactive.
func TestLifecycle_PopulatePreviewURLRules(t *testing.T) {
	projectSvc := services.NewProjectService(nil, nil, nil, nil)
	hostPort := 8080

	t.Run("running public service populates preview URL", func(t *testing.T) {
		svc := &models.Service{
			Status:        models.ServiceStatusRunning,
			PublicExposed: true,
			HostPort:      &hostPort,
		}
		proj := &models.Project{
			Status:   models.ServiceStatusRunning,
			Services: []*models.Service{svc},
		}
		projectSvc.PopulateProjectPreviewURL(proj)
		assert.NotEmpty(t, svc.PreviewURL, "running public service must have preview URL")
		assert.NotEmpty(t, proj.PreviewURL, "running project must have preview URL")
	})

	t.Run("stopped public service does NOT populate preview URL", func(t *testing.T) {
		svc := &models.Service{
			Status:        models.ServiceStatusStopped,
			PublicExposed: true,
			HostPort:      &hostPort,
		}
		proj := &models.Project{
			Status:   models.ServiceStatusStopped,
			Services: []*models.Service{svc},
		}
		projectSvc.PopulateProjectPreviewURL(proj)
		assert.Empty(t, svc.PreviewURL, "stopped service must NOT have preview URL")
		assert.Empty(t, proj.PreviewURL, "stopped project must NOT have preview URL")
	})

	t.Run("failed public service does NOT populate preview URL", func(t *testing.T) {
		svc := &models.Service{
			Status:        models.ServiceStatusFailed,
			PublicExposed: true,
			HostPort:      &hostPort,
		}
		proj := &models.Project{
			Status:   models.ServiceStatusFailed,
			Services: []*models.Service{svc},
		}
		projectSvc.PopulateProjectPreviewURL(proj)
		assert.Empty(t, svc.PreviewURL, "failed service must NOT have preview URL")
		assert.Empty(t, proj.PreviewURL, "failed project must NOT have preview URL")
	})

	t.Run("legacy single-container project stopped does NOT populate preview URL", func(t *testing.T) {
		proj := &models.Project{
			Status:   models.ServiceStatusStopped,
			Port:     &hostPort,
			Services: nil,
		}
		projectSvc.PopulateProjectPreviewURL(proj)
		assert.Empty(t, proj.PreviewURL, "stopped legacy project must NOT have preview URL")
	})

	t.Run("running frontend service populates preview URL even when public_exposed is unset", func(t *testing.T) {
		svc := &models.Service{
			Status:        models.ServiceStatusRunning,
			Role:          models.RoleFrontend,
			PublicExposed: false,
			HostPort:      &hostPort,
		}
		proj := &models.Project{
			Status:   models.ServiceStatusRunning,
			Services: []*models.Service{svc},
		}
		projectSvc.PopulateProjectPreviewURL(proj)
		assert.NotEmpty(t, svc.PreviewURL, "running frontend service must have preview URL")
		assert.NotEmpty(t, proj.PreviewURL, "running project must have preview URL")
	})

	t.Run("partially_running project with running frontend service populates preview URL", func(t *testing.T) {
		svc := &models.Service{
			Status:        models.ServiceStatusRunning,
			Role:          models.RoleFrontend,
			PublicExposed: true,
			HostPort:      &hostPort,
		}
		proj := &models.Project{
			Status:   models.ProjectStatusPartiallyRunning,
			Services: []*models.Service{svc},
		}
		projectSvc.PopulateProjectPreviewURL(proj)
		assert.NotEmpty(t, proj.PreviewURL, "partially_running project with running frontend must have preview URL")
	})
}

// TestLifecycle_DeleteProject_IdempotentErrorPath tests that DeleteProject does NOT delete DB records
// when cleanup fails, transitions status to failed, and succeeds when retried.
func TestLifecycle_DeleteProject_IdempotentErrorPath(t *testing.T) {
	pool, _ := connectTestDB(t)
	if pool == nil {
		return
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 1. Create test user
	userID := uuid.New()
	userEmail := fmt.Sprintf("test-delete-%s@forgelab.local", userID.String()[:8])
	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name)
		VALUES ($1, $2, 'hash123', 'Delete Test User')
	`, userID, userEmail)
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	}()

	// 2. Create test project in DB
	projectID := uuid.New()
	slug := fmt.Sprintf("test-del-%s", projectID.String()[:8])
	_, err = pool.Exec(ctx, `
		INSERT INTO projects (
			id, owner_id, name, slug, source_type, status, created_at, updated_at
		) VALUES (
			$1, $2, 'Delete Test', $3, 'local', 'running', NOW(), NOW()
		)
	`, projectID, userID, slug)
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM projects WHERE id = $1", projectID)
	}()

	projectSvc := services.NewProjectService(pool, nil, nil, nil)
	deploymentSvc := services.NewDeploymentService(pool)
	projectSvc.SetDeploymentService(deploymentSvc)

	// Step A: Cleanup fails
	cleanupErr := errors.New("simulated docker daemon timeout during cleanup")
	failedCleanupFunc := func(ctx context.Context, id uuid.UUID) error {
		return cleanupErr
	}

	err = projectSvc.DeleteProject(ctx, projectID, userID, failedCleanupFunc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "simulated docker daemon timeout")

	// Verify project still exists in DB and is marked as 'failed'
	var dbStatus string
	err = pool.QueryRow(ctx, "SELECT status FROM projects WHERE id = $1", projectID).Scan(&dbStatus)
	require.NoError(t, err, "project record must NOT be deleted when cleanup fails")
	assert.Equal(t, models.ProjectStatusFailed, dbStatus, "project status must transition to failed for retry")

	// Step B: Retry cleanup successfully
	successfulCleanupFunc := func(ctx context.Context, id uuid.UUID) error {
		return nil
	}

	err = projectSvc.DeleteProject(ctx, projectID, userID, successfulCleanupFunc)
	require.NoError(t, err, "retryable cleanup must succeed")

	// Verify project is now deleted from DB
	var count int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM projects WHERE id = $1", projectID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "project record must be permanently removed upon successful cleanup")
}

// TestLifecycle_DeploymentsBlockedDuringDeletion verifies that neither project nor service
// deployments can be created when project status is 'deleting'.
func TestLifecycle_DeploymentsBlockedDuringDeletion(t *testing.T) {
	pool, _ := connectTestDB(t)
	if pool == nil {
		return
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Create test user
	userID := uuid.New()
	userEmail := fmt.Sprintf("test-block-%s@forgelab.local", userID.String()[:8])
	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name)
		VALUES ($1, $2, 'hash123', 'Block Deploy User')
	`, userID, userEmail)
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	}()

	// Create test project with status = 'deleting'
	projectID := uuid.New()
	slug := fmt.Sprintf("test-blk-%s", projectID.String()[:8])
	_, err = pool.Exec(ctx, `
		INSERT INTO projects (
			id, owner_id, name, slug, source_type, status, created_at, updated_at
		) VALUES (
			$1, $2, 'Deleting Project', $3, 'local', 'deleting', NOW(), NOW()
		)
	`, projectID, userID, slug)
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM projects WHERE id = $1", projectID)
	}()

	// Create a test service under this project
	serviceID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO services (
			id, project_id, name, role, internal_port, status, created_at, updated_at
		) VALUES (
			$1, $2, 'web', 'frontend', 3000, 'running', NOW(), NOW()
		)
	`, serviceID, projectID)
	require.NoError(t, err)

	deploymentSvc := services.NewDeploymentService(pool)

	proj := &models.Project{
		ID:      projectID,
		OwnerID: userID,
		Status:  models.ProjectStatusDeleting,
	}

	// 1. Attempt project deployment
	_, err = deploymentSvc.CreateDeployment(ctx, proj)
	require.Error(t, err)
	assert.ErrorIs(t, err, services.ErrProjectDeleting, "CreateDeployment must be rejected with ErrProjectDeleting")

	// 2. Attempt service deployment
	svc := &models.Service{
		ID:        serviceID,
		ProjectID: projectID,
	}
	_, err = deploymentSvc.CreateServiceDeployment(ctx, proj, svc)
	require.Error(t, err)
	assert.ErrorIs(t, err, services.ErrProjectDeleting, "CreateServiceDeployment must be rejected with ErrProjectDeleting")
}
