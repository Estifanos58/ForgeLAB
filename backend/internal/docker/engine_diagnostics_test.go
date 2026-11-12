package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/system"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

func TestGetSystemDiagnostics_UnreachableDocker(t *testing.T) {
	ctx := context.Background()
	ownerID := uuid.New()

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectsListerForTest(func(ctx context.Context, oid uuid.UUID, limit, offset int) ([]*models.Project, error) {
		return []*models.Project{}, nil
	})
	svcSvc := services.NewServiceService(nil)
	projSvc.SetServiceService(svcSvc)

	eng := NewEngine(nil, projSvc, nil, nil, nil, nil, nil, nil, nil, t.TempDir())
	report, err := eng.GetSystemDiagnostics(ctx, ownerID, nil)
	require.NoError(t, err)
	require.NotNil(t, report)

	assert.Equal(t, "system", report.Scope)
	assert.Equal(t, "critical", report.OverallStatus)
	assert.False(t, report.Docker.Available)
	assert.False(t, report.Resources.DockerAvailable)
	require.NotEmpty(t, report.Issues)
	assert.Equal(t, "docker_connectivity", report.Issues[0].Category)
	assert.Equal(t, "critical", report.Issues[0].Severity)
	assert.NotEmpty(t, report.Issues[0].Remediation)
}

func TestGetProjectDiagnostics_AggregatesFailuresResourcesAndImports(t *testing.T) {
	ctx := context.Background()
	ownerID := uuid.New()
	projectID := uuid.New()
	svcID := uuid.New()
	cid := "cid-oom-service"

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/version"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Version":    "27.0.3",
				"Os":         "linux",
				"Arch":       "amd64",
				"ApiVersion": "1.41",
			})
		case strings.HasSuffix(r.URL.Path, "/info"):
			_ = json.NewEncoder(w).Encode(system.Info{
				Driver:          "overlay2",
				OperatingSystem: "Ubuntu 24.04",
				OSType:          "linux",
				NCPU:            8,
				MemTotal:        16 * 1024 * 1024 * 1024,
			})
		case strings.HasSuffix(r.URL.Path, "/system/df"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"LayersSize": 1000,
			})
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			_ = json.NewEncoder(w).Encode(container.InspectResponse{
				ID:           cid,
				RestartCount: 5,
				State: &container.State{
					Running:   true,
					OOMKilled: true,
					Status:    container.StateRunning,
					StartedAt: time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339Nano),
					Health: &container.Health{
						Status:        container.Unhealthy,
						FailingStreak: 2,
					},
				},
			})
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/stats"):
			_ = json.NewEncoder(w).Encode(container.StatsResponse{
				ID:     cid,
				OSType: "linux",
				CPUStats: container.CPUStats{
					CPUUsage:    container.CPUUsage{TotalUsage: 400_000_000},
					SystemUsage: 10_000_000_000,
					OnlineCPUs:  1,
				},
				PreCPUStats: container.CPUStats{
					CPUUsage:    container.CPUUsage{TotalUsage: 100_000_000},
					SystemUsage: 0,
					OnlineCPUs:  1,
				},
				MemoryStats: container.MemoryStats{
					Usage: 480 * 1024 * 1024, // 93.75% of 512MB
					Limit: 512 * 1024 * 1024,
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	cli, cleanup := newDirectTestDockerClient(t, handler)
	defer cleanup()

	svcModel := &models.Service{
		ID:             svcID,
		ProjectID:      projectID,
		Name:           "api",
		Status:         models.ServiceStatusDegraded,
		ContainerID:    &cid,
		ResourceConfig: models.ResourceConfig{MemoryMB: 512},
	}

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectByIDForTest(func(ctx context.Context, pID uuid.UUID) (*models.Project, error) {
		return &models.Project{
			ID:              projectID,
			OwnerID:         ownerID,
			Name:            "checkout-api",
			SourceType:      models.SourceTypeGitHub,
			SourceReference: "acme/checkout-api",
			Status:          models.ProjectStatusDegraded,
			Services:        []*models.Service{svcModel},
		}, nil
	})
	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListServicesForTest(func(ctx context.Context, pID uuid.UUID) ([]*models.Service, error) {
		return []*models.Service{svcModel}, nil
	})
	projSvc.SetServiceService(svcSvc)

	failReason := "Dockerfile RUN npm run build exited with code 1"
	deploySvc := services.NewDeploymentService(nil)
	deploySvc.SetListDeploymentsForTest(func(ctx context.Context, pID uuid.UUID, limit, offset int) ([]*models.Deployment, error) {
		return []*models.Deployment{
			{
				ID:            uuid.New(),
				ProjectID:     projectID,
				DeployNumber:  4,
				Status:        models.DeployStatusFailed,
				FailureReason: &failReason,
				CreatedAt:     time.Now().UTC().Add(-10 * time.Minute),
			},
		}, nil
	})
	deploySvc.SetListServiceDeploymentsByServiceForTest(func(ctx context.Context, sID uuid.UUID) ([]*models.ServiceDeployment, error) {
		return []*models.ServiceDeployment{}, nil
	})

	importSvc := services.NewRepositoryImportService(nil, t.TempDir(), nil)
	now := time.Now().UTC()
	importSvc.SeedImportForTest(&models.RepositoryImport{
		ID:                 uuid.New(),
		OwnerID:            ownerID,
		RepositoryFullName: "acme/checkout-api",
		Branch:             "main",
		Status:             models.ImportStatusDownloading,
		Stage:              "Downloading repository tarball",
		Progress:           45,
		UpdatedAt:          now.Add(-12 * time.Minute),
		CreatedAt:          now.Add(-13 * time.Minute),
		ExpiresAt:          now.Add(12 * time.Hour),
	})
	importSvc.SeedImportForTest(&models.RepositoryImport{
		ID:                 uuid.New(),
		OwnerID:            ownerID,
		RepositoryFullName: "acme/checkout-api",
		Branch:             "feat/retry",
		Status:             models.ImportStatusFailed,
		Stage:              "Failed to clone branch",
		ErrorCode:          "branch_not_found",
		ErrorMessage:       "Branch feat/retry not found",
		ProjectID:          &projectID,
		UpdatedAt:          now.Add(-2 * time.Minute),
		CreatedAt:          now.Add(-3 * time.Minute),
		ExpiresAt:          now.Add(12 * time.Hour),
	})

	eng := NewEngine(cli, projSvc, deploySvc, nil, nil, nil, nil, nil, nil, t.TempDir())
	eng.SetServiceService(svcSvc)

	report, err := eng.GetProjectDiagnostics(ctx, projectID, importSvc)
	require.NoError(t, err)
	require.NotNil(t, report)

	assert.Equal(t, "project", report.Scope)
	assert.Equal(t, "checkout-api", report.ProjectName)
	assert.Equal(t, "critical", report.OverallStatus)
	assert.True(t, report.Docker.Available)
	assert.Equal(t, "27.0.3", report.Docker.ServerVersion)
	assert.Equal(t, 8, report.Resources.HostCPUs)

	require.Len(t, report.DeploymentFailures, 1)
	assert.Equal(t, failReason, report.DeploymentFailures[0].FailureReason)

	require.Len(t, report.Resources.ExhaustedServices, 1)
	assert.True(t, report.Resources.ExhaustedServices[0].OOMKilled)
	assert.Greater(t, report.Resources.ExhaustedServices[0].MemoryPercent, 90.0)

	require.Len(t, report.Imports, 2)
	var foundStale, foundFailed bool
	for _, imp := range report.Imports {
		if imp.Stale {
			foundStale = true
		}
		if imp.Status == models.ImportStatusFailed {
			foundFailed = true
		}
	}
	assert.True(t, foundStale, "expected stalled import diagnostic")
	assert.True(t, foundFailed, "expected failed import diagnostic")

	// Critical OOM issue is sorted before warnings
	require.NotEmpty(t, report.Issues)
	assert.Equal(t, "critical", report.Issues[0].Severity)
	assert.Equal(t, "resource_exhaustion", report.Issues[0].Category)
}
