package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
	netpkg "github.com/forgelab/backend/internal/network"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
)

func TestExecuteDeployment_FailsExplicitlyOnServiceQueryError(t *testing.T) {
	ctx := context.Background()
	projectID := uuid.New()
	deployID := uuid.New()
	ownerID := uuid.New()

	dep := &models.Deployment{
		ID:           deployID,
		ProjectID:    projectID,
		DeployNumber: 3,
		Status:       models.DeployStatusQueued,
	}
	project := &models.Project{
		ID:        projectID,
		OwnerID:   ownerID,
		Name:      "service-query-err-project",
		Status:    models.ProjectStatusRunning,
		CreatedAt: dep.CreatedAt,
	}

	var statusUpdates []string
	var failureReasons []string

	depSvc := services.NewDeploymentService(nil)
	depSvc.SetGetDeploymentForTest(func(ctx context.Context, dID uuid.UUID) (*models.Deployment, error) {
		cp := *dep
		return &cp, nil
	})
	depSvc.SetUpdateDeploymentStatusForTest(func(ctx context.Context, dID uuid.UUID, newStatus string, reason *string) error {
		statusUpdates = append(statusUpdates, newStatus)
		if reason != nil {
			failureReasons = append(failureReasons, *reason)
		}
		dep.Status = newStatus
		return nil
	})

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectByIDForTest(func(ctx context.Context, pID uuid.UUID) (*models.Project, error) {
		cp := *project
		return &cp, nil
	})

	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListServicesForTest(func(ctx context.Context, pID uuid.UUID) ([]*models.Service, error) {
		return nil, errors.New("postgres read timeout")
	})

	eng := NewEngine(nil, projSvc, depSvc, nil, nil, nil, nil, nil, nil, t.TempDir())
	eng.SetServiceService(svcSvc)

	err := eng.ExecuteDeployment(ctx, deployID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "postgres read timeout")
	assert.Contains(t, statusUpdates, models.DeployStatusFailed)
	require.NotEmpty(t, failureReasons)
	assert.Contains(t, failureReasons[0], "postgres read timeout")
}

func TestExecuteServiceDeployment_FailureRecoveryPreservesHealthyRelease(t *testing.T) {
	ctx := context.Background()
	tmpRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpRoot, "Dockerfile"), []byte("FROM alpine:3.19\n"), 0644))

	ownerID := uuid.New()
	projectID := uuid.New()
	serviceID := uuid.New()
	prevSDID := uuid.New()
	newSDID := uuid.New()
	parentDeployID := uuid.New()

	prevCID := "prev-healthy-container-id"
	newCID := "new-failing-container-id"
	oldPort := 18080

	var mu sync.Mutex
	removedContainers := []string{}
	startedContainers := []string{}
	stoppedContainers := []string{}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(image.InspectResponse{
				ID:          "sha256:immutable-good-image",
				RepoDigests: []string{"alpine@sha256:immutable-good-image"},
			})
		case strings.HasSuffix(r.URL.Path, "/images/alpine:3.19/tag") || strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/tag"):
			w.WriteHeader(http.StatusCreated)
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
		case r.URL.Path == "/v1.47/containers/create" || strings.HasSuffix(r.URL.Path, "/containers/create"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(container.CreateResponse{ID: newCID})
		case strings.HasSuffix(r.URL.Path, "/start"):
			id := extractContainerID(r.URL.Path, "/start")
			mu.Lock()
			startedContainers = append(startedContainers, id)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/stop"):
			id := extractContainerID(r.URL.Path, "/stop")
			mu.Lock()
			stoppedContainers = append(stoppedContainers, id)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/"):
			id := strings.TrimPrefix(r.URL.Path[strings.LastIndex(r.URL.Path, "/containers/")+len("/containers/"):], "/")
			mu.Lock()
			removedContainers = append(removedContainers, id)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			id := extractContainerID(r.URL.Path, "/json")
			w.Header().Set("Content-Type", "application/json")
			if id == prevCID {
				_ = json.NewEncoder(w).Encode(container.InspectResponse{
					ID:    prevCID,
					State: &container.State{Running: true, Status: container.StateRunning},
				})
				return
			}
			// Simulate newly started container immediately unhealthy/exited during health verification
			_ = json.NewEncoder(w).Encode(container.InspectResponse{
				ID: newCID,
				State: &container.State{
					Running:  false,
					Status:   container.StateExited,
					ExitCode: 137,
				},
			})
		case strings.HasSuffix(r.URL.Path, "/logs"):
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	cli, cleanup := newDirectTestDockerClient(t, handler)
	defer cleanup()

	project := &models.Project{
		ID:             projectID,
		OwnerID:        ownerID,
		Name:           "safety-invariant-app",
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: tmpRoot,
		Status:         models.ProjectStatusRunning,
	}
	service := &models.Service{
		ID:                         serviceID,
		ProjectID:                  projectID,
		Name:                       "api",
		Role:                       models.RoleBackend,
		Classification:             models.ClassificationApplication,
		Status:                     models.ServiceStatusRunning,
		ContainerID:                strPtr(prevCID),
		HostPort:                   &oldPort,
		InternalPort:               8080,
		PublicExposed:              false,
		HealthStrategy:             models.HealthStrategyDocker,
		BuildStrategy:              models.BuildStrategyAuto,
		CurrentServiceDeploymentID: &prevSDID,
	}

	interruptedReason := "Deployment interrupted: backend restarted while deployment was in progress"
	digestRef := "alpine:3.19"
	sd := &models.ServiceDeployment{
		ID:             newSDID,
		DeploymentID:   &parentDeployID,
		ServiceID:      serviceID,
		ServiceName:    "api",
		DeployNumber:   2,
		Status:         models.DeployStatusFailed,
		FailureReason:  &interruptedReason,
		ExecutionMode:  models.ExecutionModeReuseImage,
		ImageDigest:    &digestRef,
		ImageTag:       &digestRef,
		InternalPort:   8080,
		HealthStrategy: models.HealthStrategyDocker,
		BuildStrategy:  models.BuildStrategyAuto,
	}
	parentDeploy := &models.Deployment{
		ID:           parentDeployID,
		ProjectID:    projectID,
		DeployNumber: 2,
		Status:       models.DeployStatusBuilding,
	}

	resetCalled := false
	promotedCalled := false
	sdStatusHistory := []string{}

	depSvc := services.NewDeploymentService(nil)
	depSvc.SetGetDeploymentForTest(func(ctx context.Context, dID uuid.UUID) (*models.Deployment, error) {
		cp := *parentDeploy
		return &cp, nil
	})
	depSvc.SetGetServiceDeploymentForTest(func(ctx context.Context, sdID uuid.UUID) (*models.ServiceDeployment, error) {
		cp := *sd
		return &cp, nil
	})
	depSvc.SetResetInterruptedServiceDeploymentForTest(func(ctx context.Context, sdID uuid.UUID) error {
		resetCalled = true
		sd.Status = models.DeployStatusQueued
		sd.FailureReason = nil
		return nil
	})
	depSvc.SetUpdateServiceDeploymentStatusForTest(func(ctx context.Context, sdID uuid.UUID, newStatus string, failureReason *string) error {
		sdStatusHistory = append(sdStatusHistory, newStatus)
		sd.Status = newStatus
		sd.FailureReason = failureReason
		return nil
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
	svcSvc.SetPromoteServiceDeploymentForTest(func(ctx context.Context, sID, sdID uuid.UUID, cID, img string, hp *int) error {
		promotedCalled = true
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
	pm := netpkg.NewPortManager(18200, 18300)
	eng := NewEngine(cli, projSvc, depSvc, nil, nil, nil, pm, pv, nil, t.TempDir())
	eng.SetServiceService(svcSvc)
	defer eng.StopAllLogCollectors()

	// Simulate active release execution in memory so interrupt recovery is authorized
	eng.registerActiveExec(parentDeployID, projectID, func() {})
	defer eng.unregisterActiveExec(parentDeployID)

	err := eng.ExecuteServiceDeployment(ctx, newSDID)
	require.Error(t, err)

	assert.True(t, resetCalled, "interrupted deployment under active release must use ResetInterruptedServiceDeployment")
	assert.False(t, promotedCalled, "failed health verification must NEVER promote the failing service deployment")
	assert.Equal(t, models.DeployStatusFailed, sd.Status)
	assert.Equal(t, prevSDID, *service.CurrentServiceDeploymentID, "previous healthy CurrentServiceDeploymentID must stay intact")
	assert.Equal(t, prevCID, *service.ContainerID, "previous healthy container ID must stay intact")
	assert.Equal(t, models.ServiceStatusRunning, service.Status, "service coarse status must stay running while previous healthy container serves")
	assert.Equal(t, []string{newCID}, startedContainers)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, stoppedContainers, newCID, "failing container should be stopped")
	assert.Contains(t, removedContainers, newCID, "failing container should be cleaned up")
	assert.NotContains(t, removedContainers, prevCID, "previous healthy container must never be removed")
	assert.Contains(t, sdStatusHistory, models.DeployStatusHealthChecking)
	assert.Contains(t, sdStatusHistory, models.DeployStatusFailed)
}

func TestReconcileDaemonContainers_SelfHealsPromotedDeploymentStuckInHealthChecking(t *testing.T) {
	ctx := context.Background()
	projectID := uuid.New()
	serviceID := uuid.New()
	sdID := uuid.New()
	liveCID := "live-running-promoted-container"
	port := 18090

	// Mock Docker client: returns running container
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]container.Summary{
				{
					ID:    liveCID,
					State: container.StateRunning,
					Ports: []container.PortSummary{
						{PublicPort: uint16(port), PrivatePort: 8080, Type: "tcp"},
					},
					Labels: map[string]string{
						"forgelab.project_id":            projectID.String(),
						"forgelab.service_id":            serviceID.String(),
						"forgelab.service_deployment_id": sdID.String(),
					},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/networks"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]network.Summary{})
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	cli, cleanup := newDirectTestDockerClient(t, handler)
	defer cleanup()

	// Service database state: already promoted
	service := &models.Service{
		ID:                         serviceID,
		ProjectID:                  projectID,
		Name:                       "web",
		Status:                     models.ServiceStatusRunning,
		ContainerID:                &liveCID,
		HostPort:                   &port,
		CurrentServiceDeploymentID: &sdID,
	}

	// Deployment database state: stuck in health_checking
	sd := &models.ServiceDeployment{
		ID:           sdID,
		ServiceID:    serviceID,
		Status:       models.DeployStatusHealthChecking,
		ContainerID:  &liveCID,
		HostPort:     &port,
		InternalPort: 8080,
	}

	var updatedStatuses []string
	var failedSDID *uuid.UUID

	depSvc := services.NewDeploymentService(nil)
	depSvc.SetListActiveServiceDeploymentsForTest(func(ctx context.Context) ([]*models.ServiceDeployment, error) {
		cp := *sd
		return []*models.ServiceDeployment{&cp}, nil
	})
	depSvc.SetUpdateServiceDeploymentStatusForTest(func(ctx context.Context, id uuid.UUID, newStatus string, failureReason *string) error {
		updatedStatuses = append(updatedStatuses, newStatus)
		sd.Status = newStatus
		return nil
	})
	depSvc.SetFailServiceDeploymentForTest(func(ctx context.Context, id uuid.UUID, reason string) error {
		failedSDID = &id
		return nil
	})

	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListActiveServicesForTest(func(ctx context.Context) ([]*models.Service, error) {
		cp := *service
		return []*models.Service{&cp}, nil
	})

	projSvc := services.NewProjectService(nil, nil, nil, nil)

	pm := netpkg.NewPortManager(18000, 18500)
	eng := NewEngine(cli, projSvc, depSvc, nil, nil, nil, pm, nil, nil, t.TempDir())
	eng.SetServiceService(svcSvc)

	err := eng.ReconcileDaemonContainers(ctx)
	require.NoError(t, err)

	// Invariant: Promoted deployment whose container is healthy and running must be self-healed to RUNNING, NOT failed!
	assert.Nil(t, failedSDID, "promoted running deployment must not be marked failed during reconciliation")
	assert.Contains(t, updatedStatuses, models.DeployStatusRunning, "deployment status must be self-healed to RUNNING")
	assert.Equal(t, models.DeployStatusRunning, sd.Status)
}

func TestReconcileDaemonContainers_CleansUpUnpromotedInterruptedContainer(t *testing.T) {
	ctx := context.Background()
	projectID := uuid.New()
	serviceID := uuid.New()
	sdID := uuid.New()
	orphanedCID := "orphaned-interrupted-container"
	port := 18095

	var mu sync.Mutex
	stoppedContainers := []string{}
	removedContainers := []string{}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]container.Summary{
				{
					ID:    orphanedCID,
					State: container.StateRunning,
					Ports: []container.PortSummary{
						{PublicPort: uint16(port), PrivatePort: 8080, Type: "tcp"},
					},
					Labels: map[string]string{
						"forgelab.project_id":            projectID.String(),
						"forgelab.service_id":            serviceID.String(),
						"forgelab.service_deployment_id": sdID.String(),
					},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/stop"):
			id := extractContainerID(r.URL.Path, "/stop")
			mu.Lock()
			stoppedContainers = append(stoppedContainers, id)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/"):
			id := strings.TrimPrefix(r.URL.Path[strings.LastIndex(r.URL.Path, "/containers/")+len("/containers/"):], "/")
			mu.Lock()
			removedContainers = append(removedContainers, id)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/networks"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]network.Summary{})
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	cli, cleanup := newDirectTestDockerClient(t, handler)
	defer cleanup()

	// Service database state: NOT promoted to sdID (has no deployment or an older one)
	olderSDID := uuid.New()
	service := &models.Service{
		ID:                         serviceID,
		ProjectID:                  projectID,
		Name:                       "worker",
		Status:                     models.ServiceStatusStopped,
		CurrentServiceDeploymentID: &olderSDID,
	}

	// Unpromoted in-flight deployment left in starting state
	sd := &models.ServiceDeployment{
		ID:           sdID,
		ServiceID:    serviceID,
		Status:       models.DeployStatusStarting,
		ContainerID:  &orphanedCID,
		HostPort:     &port,
		InternalPort: 8080,
	}

	var failedSDID *uuid.UUID

	depSvc := services.NewDeploymentService(nil)
	depSvc.SetListActiveServiceDeploymentsForTest(func(ctx context.Context) ([]*models.ServiceDeployment, error) {
		cp := *sd
		return []*models.ServiceDeployment{&cp}, nil
	})
	depSvc.SetFailServiceDeploymentForTest(func(ctx context.Context, id uuid.UUID, reason string) error {
		failedSDID = &id
		sd.Status = models.DeployStatusFailed
		return nil
	})

	svcSvc := services.NewServiceService(nil)
	svcSvc.SetListActiveServicesForTest(func(ctx context.Context) ([]*models.Service, error) {
		cp := *service
		return []*models.Service{&cp}, nil
	})

	projSvc := services.NewProjectService(nil, nil, nil, nil)

	pm := netpkg.NewPortManager(18000, 18500)
	eng := NewEngine(cli, projSvc, depSvc, nil, nil, nil, pm, nil, nil, t.TempDir())
	eng.SetServiceService(svcSvc)

	err := eng.ReconcileDaemonContainers(ctx)
	require.NoError(t, err)

	// Invariant: Unpromoted interrupted deployment is marked failed, and its orphaned container stopped & removed!
	assert.NotNil(t, failedSDID)
	assert.Equal(t, sdID, *failedSDID)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, stoppedContainers, orphanedCID, "orphaned unpromoted container should be stopped")
	assert.Contains(t, removedContainers, orphanedCID, "orphaned unpromoted container should be removed")
}

func TestExecuteServiceDeployment_RollbackOnPromotionPersistenceFailure(t *testing.T) {
	ctx := context.Background()
	tmpRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpRoot, "Dockerfile"), []byte("FROM alpine:3.19\n"), 0644))

	ownerID := uuid.New()
	projectID := uuid.New()
	serviceID := uuid.New()
	prevSDID := uuid.New()
	newSDID := uuid.New()

	prevCID := "prev-healthy-container-active"
	newCID := "new-promoted-failing-db-container"
	oldPort := 18080

	var mu sync.Mutex
	removedContainers := []string{}
	startedContainers := []string{}
	stoppedContainers := []string{}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(image.InspectResponse{
				ID:          "sha256:immutable-good-image",
				RepoDigests: []string{"alpine@sha256:immutable-good-image"},
			})
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/tag"):
			w.WriteHeader(http.StatusCreated)
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
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(container.CreateResponse{ID: newCID})
		case strings.HasSuffix(r.URL.Path, "/start"):
			id := extractContainerID(r.URL.Path, "/start")
			mu.Lock()
			startedContainers = append(startedContainers, id)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/stop"):
			id := extractContainerID(r.URL.Path, "/stop")
			mu.Lock()
			stoppedContainers = append(stoppedContainers, id)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/"):
			id := strings.TrimPrefix(r.URL.Path[strings.LastIndex(r.URL.Path, "/containers/")+len("/containers/"):], "/")
			mu.Lock()
			removedContainers = append(removedContainers, id)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(container.InspectResponse{
				ID:    extractContainerID(r.URL.Path, "/json"),
				State: &container.State{Running: true, Status: container.StateRunning, Health: &container.Health{Status: "healthy"}},
			})
		case strings.HasSuffix(r.URL.Path, "/logs"):
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	cli, cleanup := newDirectTestDockerClient(t, handler)
	defer cleanup()

	project := &models.Project{
		ID:             projectID,
		OwnerID:        ownerID,
		Name:           "rollback-project",
		Status:         models.ProjectStatusRunning,
		DeploymentMode: models.DeploymentModeStandard,
		SourceType:     models.SourceTypeLocalUpload,
	}

	service := &models.Service{
		ID:                         serviceID,
		ProjectID:                  projectID,
		Name:                       "web",
		Role:                       models.RoleFrontend,
		Status:                     models.ServiceStatusRunning,
		ContainerID:                &prevCID,
		HostPort:                   &oldPort,
		InternalPort:               8080,
		HealthStrategy:             models.HealthStrategyNone,
		CurrentServiceDeploymentID: &prevSDID,
	}

	digestRef := "alpine:3.19"
	sd := &models.ServiceDeployment{
		ID:             newSDID,
		ServiceID:      serviceID,
		ServiceName:    "web",
		DeployNumber:   2,
		Status:         models.DeployStatusQueued,
		ExecutionMode:  models.ExecutionModeReuseImage,
		ImageDigest:    &digestRef,
		ImageTag:       &digestRef,
		InternalPort:   8080,
		HealthStrategy: models.HealthStrategyNone,
	}

	depSvc := services.NewDeploymentService(nil)
	depSvc.SetGetServiceDeploymentForTest(func(ctx context.Context, id uuid.UUID) (*models.ServiceDeployment, error) {
		cp := *sd
		return &cp, nil
	})
	depSvc.SetUpdateServiceDeploymentStatusForTest(func(ctx context.Context, id uuid.UUID, newStatus string, failureReason *string) error {
		sd.Status = newStatus
		sd.FailureReason = failureReason
		return nil
	})
	depSvc.SetUpdateServiceDeploymentContainerForTest(func(ctx context.Context, id uuid.UUID, cID string, hp *int) error {
		sd.ContainerID = &cID
		sd.HostPort = hp
		return nil
	})

	svcSvc := services.NewServiceService(nil)
	svcSvc.SetGetServiceForTest(func(ctx context.Context, id uuid.UUID) (*models.Service, error) {
		cp := *service
		return &cp, nil
	})
	svcSvc.SetListServicesForTest(func(ctx context.Context, id uuid.UUID) ([]*models.Service, error) {
		cp := *service
		return []*models.Service{&cp}, nil
	})
	svcSvc.SetUpdateServiceStatusForTest(func(ctx context.Context, id uuid.UUID, status string, cID, img *string, hp *int) error {
		service.Status = status
		return nil
	})

	// Simulate database failure during promotion
	svcSvc.SetPromoteServiceDeploymentForTest(func(ctx context.Context, sID, sdID uuid.UUID, cID, img string, hp *int) error {
		return errors.New("simulated database transaction deadlock during promotion")
	})

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetServiceService(svcSvc)
	projSvc.SetProjectByIDForTest(func(ctx context.Context, id uuid.UUID) (*models.Project, error) {
		cp := *project
		return &cp, nil
	})
	projSvc.SetUpdateProjectStatusForTest(func(ctx context.Context, id uuid.UUID, status string) error {
		project.Status = status
		return nil
	})

	pv := security.NewPathValidator([]string{tmpRoot})
	pm := netpkg.NewPortManager(18300, 18400)
	eng := NewEngine(cli, projSvc, depSvc, nil, nil, nil, pm, pv, nil, t.TempDir())
	eng.SetServiceService(svcSvc)
	defer eng.StopAllLogCollectors()

	err := eng.ExecuteServiceDeployment(ctx, newSDID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "simulated database transaction deadlock during promotion")

	// Safety invariant: new broken container must be stopped and destroyed, leaving previous healthy untouched!
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, stoppedContainers, newCID, "new container must be stopped upon promotion failure")
	assert.Contains(t, removedContainers, newCID, "new container must be removed upon promotion failure")
	assert.NotContains(t, removedContainers, prevCID, "previous healthy container must NEVER be removed")

	assert.Equal(t, models.DeployStatusFailed, sd.Status)
	assert.Equal(t, prevSDID, *service.CurrentServiceDeploymentID, "service current deployment must remain prior healthy")
	assert.Equal(t, prevCID, *service.ContainerID, "service container ID must remain prior healthy")
}
