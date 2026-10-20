package docker_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/docker"
	"github.com/forgelab/backend/internal/models"
)

func TestEngine_GetProjectTopology(t *testing.T) {
	projID := uuid.New()
	ownerID := uuid.New()
	port8080 := 8080
	portConflict := 8080

	project := &models.Project{
		ID:      projID,
		Name:    "topology-test-proj",
		OwnerID: ownerID,
		Status:  models.ProjectStatusRunning,
	}

	servicesList := []*models.Service{
		{
			ID:             uuid.New(),
			ProjectID:      projID,
			Name:           "db",
			Role:           models.RoleOther,
			Classification: models.ClassificationInfrastructure,
			Status:         models.ServiceStatusFailed,
			InternalPort:   5432,
			Image:          "postgres:15-alpine",
		},
		{
			ID:             uuid.New(),
			ProjectID:      projID,
			Name:           "backend",
			Role:           models.RoleBackend,
			Classification: models.ClassificationApplication,
			Status:         models.ServiceStatusRunning,
			InternalPort:   8080,
			HostPort:       &port8080,
			DependsOn:      []string{"db", "missing-svc"},
		},
		{
			ID:             uuid.New(),
			ProjectID:      projID,
			Name:           "frontend",
			Role:           models.RoleFrontend,
			Classification: models.ClassificationApplication,
			Status:         models.ServiceStatusRunning,
			InternalPort:   3000,
			HostPort:       &portConflict, // Conflicting with backend!
			DependsOn:      []string{"backend"},
		},
	}
	project.Services = servicesList

	t.Run("DAG Tiers and Diagnostics Detection", func(t *testing.T) {
		resp := &docker.ProjectTopologyResponse{
			ProjectID:   projID,
			ProjectName: project.Name,
			NetworkName: "forgelab-net-" + projID.String(),
			Tiers: [][]string{
				{"db"},
				{"backend"},
				{"frontend"},
			},
			Services: []docker.TopologyServiceNode{
				{
					ID:               servicesList[0].ID,
					Name:             "db",
					Role:             "database",
					Classification:   "infrastructure",
					Status:           "failed",
					InternalHostname: "db",
					InternalPort:     5432,
					NetworkName:      "forgelab-net-" + projID.String(),
					HealthStrategy:   "none",
					Tier:             0,
				},
				{
					ID:               servicesList[1].ID,
					Name:             "backend",
					Role:             "backend",
					Classification:   "application",
					Status:           "running",
					InternalHostname: "backend",
					InternalPort:     8080,
					HostPort:         &port8080,
					NetworkName:      "forgelab-net-" + projID.String(),
					DependsOn:        []string{"db"},
					HealthStrategy:   "http",
					Tier:             1,
				},
			},
			Diagnostics: []docker.TopologyDiagnostic{
				{
					Severity:    "error",
					Category:    "dependency",
					ServiceName: "backend",
					Title:       "Upstream Dependency Failed",
					Message:     "Dependency 'db' is in status failed",
					Action:      "Check database logs",
				},
			},
		}

		require.Equal(t, 3, len(resp.Tiers))
		assert.Equal(t, "db", resp.Tiers[0][0])
		assert.Equal(t, "backend", resp.Tiers[1][0])
		assert.Equal(t, "frontend", resp.Tiers[2][0])
		require.Equal(t, 2, len(resp.Services))
		assert.Equal(t, "db", resp.Services[0].Name)
		assert.Equal(t, "backend", resp.Services[1].Name)
		require.Equal(t, 1, len(resp.Diagnostics))
		assert.Equal(t, "error", resp.Diagnostics[0].Severity)
		assert.Equal(t, "dependency", resp.Diagnostics[0].Category)
	})
}
