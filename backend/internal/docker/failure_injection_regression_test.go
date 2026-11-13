package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
	netpkg "github.com/forgelab/backend/internal/network"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
)

// TestFailureInjection_DockerDaemonCreateContainerError verifies that when the Docker daemon
// returns an unrecoverable failure (e.g. 500 Out of Memory or Internal Error) during container creation,
// the deployment transitions to FAILED, records the explicit error, and never claims running.
func TestFailureInjection_DockerDaemonCreateContainerError(t *testing.T) {
	ctx := context.Background()
	tmpRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpRoot, "Dockerfile"), []byte("FROM alpine:3.19\n"), 0644))

	projectID := uuid.New()
	deployID := uuid.New()
	ownerID := uuid.New()
	serviceID := uuid.New()
	sdID := uuid.New()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(image.InspectResponse{
				ID:          "sha256:immutable-daemon-fail-img",
				RepoDigests: []string{"alpine@sha256:immutable-daemon-fail-img"},
			})
		case strings.HasSuffix(r.URL.Path, "/networks"):
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode([]network.Summary{{Network: network.Network{ID: "net-1", Name: "forgelab-net-" + projectID.String()}}})
				return
			}
			w.WriteHeader(http.StatusCreated)
		case strings.Contains(r.URL.Path, "/networks/") && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(network.Inspect{Network: network.Network{ID: "net-1", Name: "forgelab-net-" + projectID.String()}})
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			// Inject Docker daemon failure: 500 Internal Server Error (simulating OOM or disk full)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"message": "daemon out of memory: failed to reserve container resources",
			})
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	cli, cleanup := newDirectTestDockerClient(t, handler)
	defer cleanup()

	digestRef := "alpine:3.19"
	dep := &models.Deployment{
		ID:           deployID,
		ProjectID:    projectID,
		DeployNumber: 1,
		Status:       models.DeployStatusQueued,
	}
	project := &models.Project{
		ID:             projectID,
		OwnerID:        ownerID,
		Name:           "docker-daemon-fail-app",
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: tmpRoot,
		Status:         models.ProjectStatusInactive,
		CreatedAt:      dep.CreatedAt,
	}
	service := &models.Service{
		ID:             serviceID,
		ProjectID:      projectID,
		Name:           "api",
		Role:           models.RoleBackend,
		Classification: models.ClassificationApplication,
		Status:         models.ServiceStatusInactive,
		InternalPort:   8080,
		HealthStrategy: models.HealthStrategyDocker,
		BuildStrategy:  models.BuildStrategyAuto,
	}
	sd := &models.ServiceDeployment{
		ID:             sdID,
		DeploymentID:   &deployID,
		ServiceID:      serviceID,
		ServiceName:    "api",
		DeployNumber:   1,
		Status:         models.DeployStatusQueued,
		ExecutionMode:  models.ExecutionModeReuseImage,
		ImageDigest:    &digestRef,
		ImageTag:       &digestRef,
		InternalPort:   8080,
		HealthStrategy: models.HealthStrategyDocker,
		BuildStrategy:  models.BuildStrategyAuto,
	}

	var mu sync.Mutex
	statusUpdates := []string{}
	var finalFailureReason string

	depSvc := services.NewDeploymentService(nil)
	depSvc.SetGetDeploymentForTest(func(ctx context.Context, dID uuid.UUID) (*models.Deployment, error) {
		cp := *dep
		return &cp, nil
	})
	depSvc.SetListServiceDeploymentsForTest(func(ctx context.Context, dID uuid.UUID) ([]*models.ServiceDeployment, error) {
		cp := *sd
		return []*models.ServiceDeployment{&cp}, nil
	})
	depSvc.SetGetServiceDeploymentForTest(func(ctx context.Context, sID uuid.UUID) (*models.ServiceDeployment, error) {
		cp := *sd
		return &cp, nil
	})
	depSvc.SetUpdateDeploymentStatusForTest(func(ctx context.Context, dID uuid.UUID, newStatus string, reason *string) error {
		mu.Lock()
		defer mu.Unlock()
		statusUpdates = append(statusUpdates, newStatus)
		dep.Status = newStatus
		if reason != nil {
			finalFailureReason = *reason
		}
		return nil
	})
	depSvc.SetUpdateServiceDeploymentStatusForTest(func(ctx context.Context, sID uuid.UUID, newStatus string, reason *string) error {
		mu.Lock()
		defer mu.Unlock()
		sd.Status = newStatus
		if reason != nil {
			sd.FailureReason = reason
		}
		return nil
	})
	depSvc.SetAddDeploymentServiceLogForTest(func(ctx context.Context, depID, sdID, sID *uuid.UUID, phase, stream, message string) (*models.DeploymentLog, error) {
		return &models.DeploymentLog{ID: 1, Timestamp: time.Now(), Phase: phase, Stream: stream, Message: message}, nil
	})

	svcSvc := services.NewServiceService(nil)
	svcSvc.SetGetServiceForTest(func(ctx context.Context, sID uuid.UUID) (*models.Service, error) {
		cp := *service
		return &cp, nil
	})
	svcSvc.SetListServicesForTest(func(ctx context.Context, pID uuid.UUID) ([]*models.Service, error) {
		cp := *service
		return []*models.Service{&cp}, nil
	})
	svcSvc.SetUpdateServiceStatusForTest(func(ctx context.Context, sID uuid.UUID, status string, containerID, imageTag *string, hostPort *int) error {
		service.Status = status
		return nil
	})

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetServiceService(svcSvc)
	projSvc.SetProjectByIDForTest(func(ctx context.Context, pID uuid.UUID) (*models.Project, error) {
		cp := *project
		return &cp, nil
	})
	projSvc.SetUpdateProjectStatusForTest(func(ctx context.Context, pID uuid.UUID, status string) error {
		project.Status = status
		return nil
	})

	pv := security.NewPathValidator([]string{tmpRoot})
	pm := netpkg.NewPortManager(18300, 18400)
	eng := NewEngine(cli, projSvc, depSvc, nil, nil, nil, pm, pv, nil, t.TempDir())
	eng.SetServiceService(svcSvc)
	defer eng.StopAllLogCollectors()

	err := eng.ExecuteDeployment(ctx, deployID)
	require.Error(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, statusUpdates, models.DeployStatusFailed)
	assert.Contains(t, finalFailureReason, "failed to reserve container resources")
	assert.Equal(t, models.DeployStatusFailed, sd.Status)
}

// TestFailureInjection_CancelledContext_AbortsPromptly verifies that when a deployment
// is cancelled mid-execution, the engine aborts promptly, marks the deployment as failed/aborted,
// and does not leave active locks or register as running.
func TestFailureInjection_CancelledContext_AbortsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel the context upfront to simulate immediate cancellation
	cancel()

	tmpRoot := t.TempDir()
	projectID := uuid.New()
	deployID := uuid.New()
	ownerID := uuid.New()
	serviceID := uuid.New()
	sdID := uuid.New()

	dep := &models.Deployment{
		ID:           deployID,
		ProjectID:    projectID,
		DeployNumber: 1,
		Status:       models.DeployStatusQueued,
	}
	project := &models.Project{
		ID:             projectID,
		OwnerID:        ownerID,
		Name:           "cancelled-deploy-app",
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: tmpRoot,
		Status:         models.ProjectStatusInactive,
		CreatedAt:      dep.CreatedAt,
	}
	service := &models.Service{
		ID:             serviceID,
		ProjectID:      projectID,
		Name:           "web",
		Status:         models.ServiceStatusInactive,
		InternalPort:   3000,
		HealthStrategy: models.HealthStrategyDocker,
		BuildStrategy:  models.BuildStrategyAuto,
	}
	sd := &models.ServiceDeployment{
		ID:             sdID,
		DeploymentID:   &deployID,
		ServiceID:      serviceID,
		ServiceName:    "web",
		DeployNumber:   1,
		Status:         models.DeployStatusQueued,
		InternalPort:   3000,
		HealthStrategy: models.HealthStrategyDocker,
		BuildStrategy:  models.BuildStrategyAuto,
	}

	var mu sync.Mutex
	statusUpdates := []string{}
	var finalFailureReason string

	depSvc := services.NewDeploymentService(nil)
	depSvc.SetGetDeploymentForTest(func(c context.Context, dID uuid.UUID) (*models.Deployment, error) {
		cp := *dep
		return &cp, nil
	})
	depSvc.SetListServiceDeploymentsForTest(func(c context.Context, dID uuid.UUID) ([]*models.ServiceDeployment, error) {
		cp := *sd
		return []*models.ServiceDeployment{&cp}, nil
	})
	depSvc.SetUpdateDeploymentStatusForTest(func(c context.Context, dID uuid.UUID, newStatus string, reason *string) error {
		mu.Lock()
		defer mu.Unlock()
		statusUpdates = append(statusUpdates, newStatus)
		dep.Status = newStatus
		if reason != nil {
			finalFailureReason = *reason
		}
		return nil
	})
	depSvc.SetAddDeploymentServiceLogForTest(func(c context.Context, depID, sdID, sID *uuid.UUID, phase, stream, message string) (*models.DeploymentLog, error) {
		return &models.DeploymentLog{ID: 1, Timestamp: time.Now(), Phase: phase, Stream: stream, Message: message}, nil
	})

	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListServicesForTest(func(c context.Context, pID uuid.UUID) ([]*models.Service, error) {
		cp := *service
		return []*models.Service{&cp}, nil
	})

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetServiceService(svcSvc)
	projSvc.SetProjectByIDForTest(func(c context.Context, pID uuid.UUID) (*models.Project, error) {
		cp := *project
		return &cp, nil
	})

	pv := security.NewPathValidator([]string{tmpRoot})
	pm := netpkg.NewPortManager(18400, 18500)
	eng := NewEngine(nil, projSvc, depSvc, nil, nil, nil, pm, pv, nil, t.TempDir())
	eng.SetServiceService(svcSvc)

	err := eng.ExecuteDeployment(ctx, deployID)
	require.Error(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, statusUpdates, models.DeployStatusFailed)
	assert.Contains(t, finalFailureReason, "worker context cancelled or lease lost")
	assert.False(t, eng.IsActiveExecution(deployID), "active execution registration must be cleaned up")
}
