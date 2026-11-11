package services_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

func TestProjectService_ListProjects_PropagatesServiceQueryError(t *testing.T) {
	ctx := context.Background()
	ownerID := uuid.New()
	projID := uuid.New()

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectsListerForTest(func(ctx context.Context, owner uuid.UUID, limit, offset int) ([]*models.Project, error) {
		return []*models.Project{
			{
				ID:           projID,
				OwnerID:      ownerID,
				Name:         "test-app",
				Status:       models.ProjectStatusRunning,
				InternalPort: 3000,
			},
		}, nil
	})

	t.Run("returns error instead of synthesizing fallback service when batch service lookup fails", func(t *testing.T) {
		svcSvc := services.NewServiceService(nil)
		svcSvc.SetListServicesByProjectsForTest(func(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]*models.Service, error) {
			return nil, errors.New("simulated connection reset during batch service query")
		})
		projSvc.SetServiceService(svcSvc)

		projects, err := projSvc.ListProjects(ctx, ownerID)
		require.Error(t, err)
		assert.Nil(t, projects)
		assert.Contains(t, err.Error(), "failed to list services for projects")
		assert.Contains(t, err.Error(), "simulated connection reset")
	})

	t.Run("synthesizes legacy fallback service only when query succeeds with 0 services", func(t *testing.T) {
		svcSvc := services.NewServiceService(nil)
		svcSvc.SetListServicesByProjectsForTest(func(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]*models.Service, error) {
			return map[uuid.UUID][]*models.Service{}, nil
		})
		projSvc.SetServiceService(svcSvc)

		projects, err := projSvc.ListProjects(ctx, ownerID)
		require.NoError(t, err)
		require.Len(t, projects, 1)
		require.Len(t, projects[0].Services, 1)
		assert.Equal(t, projID, projects[0].Services[0].ID)
	})
}

func TestProjectService_GetProjectByIDWithoutOwnership_PropagatesServiceQueryError(t *testing.T) {
	ctx := context.Background()
	projID := uuid.New()

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectByIDForTest(func(ctx context.Context, id uuid.UUID) (*models.Project, error) {
		return &models.Project{
			ID:     projID,
			Name:   "single-proj",
			Status: models.ProjectStatusRunning,
		}, nil
	})

	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListServicesForTest(func(ctx context.Context, projectID uuid.UUID) ([]*models.Service, error) {
		return nil, errors.New("simulated database timeout listing services")
	})
	projSvc.SetServiceService(svcSvc)

	proj, err := projSvc.GetProjectByIDWithoutOwnership(ctx, projID)
	require.Error(t, err)
	assert.Nil(t, proj)
	assert.Contains(t, err.Error(), "failed to list services for project")
	assert.Contains(t, err.Error(), "simulated database timeout")
}
