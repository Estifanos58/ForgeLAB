package docker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
	"github.com/forgelab/backend/internal/proxy"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
)

func TestExecuteServiceDeployment_BlueGreen_HealthyCutover(t *testing.T) {
	ctx := context.Background()
	tmpRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpRoot, "Dockerfile"), []byte("FROM alpine:3.19\n"), 0644))

	blueUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("blue-v1"))
	}))
	defer blueUpstream.Close()

	u, err := url.Parse(blueUpstream.URL)
	require.NoError(t, err)
	bluePort, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	ownerID := uuid.New()
	projectID := uuid.New()
	serviceID := uuid.New()
	prevSDID := uuid.New()
	newSDID := uuid.New()

	blueCID := "blue-container-id-0001"
	greenCID := "green-container-id-0002"

	var mu sync.Mutex
	var events []string
	var stoppedContainers []string
	var removedContainers []string
	var activeRouter *proxy.TrafficRouter
	var slotDuringHealthCheck string
	var slotAtPromotion string

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(image.InspectResponse{
				ID:          "sha256:green-image-digest",
				RepoDigests: []string{"alpine@sha256:green-image-digest"},
			})
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/tag"):
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(r.URL.Path, "/networks/create"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(network.CreateResponse{ID: "net-bg"})
		case strings.HasSuffix(r.URL.Path, "/networks"):
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode([]network.Summary{{Network: network.Network{ID: "net-bg", Name: "forgelab-net-" + projectID.String()}}})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(network.CreateResponse{ID: "net-bg"})
		case strings.Contains(r.URL.Path, "/networks/") && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(network.Inspect{Network: network.Network{ID: "net-bg", Name: "forgelab-net-" + projectID.String()}})
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(container.CreateResponse{ID: greenCID})
		case strings.HasSuffix(r.URL.Path, "/start"):
			id := extractContainerID(r.URL.Path, "/start")
			mu.Lock()
			events = append(events, "start:"+id)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/stop"):
			id := extractContainerID(r.URL.Path, "/stop")
			mu.Lock()
			stoppedContainers = append(stoppedContainers, id)
			events = append(events, "stop:"+id)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/"):
			id := strings.TrimPrefix(r.URL.Path[strings.LastIndex(r.URL.Path, "/containers/")+len("/containers/"):], "/")
			mu.Lock()
			removedContainers = append(removedContainers, id)
			events = append(events, "remove:"+id)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			id := extractContainerID(r.URL.Path, "/json")
			if activeRouter != nil {
				if rt, ok := activeRouter.GetRoute(serviceID); ok {
					mu.Lock()
					if slotDuringHealthCheck == "" {
						slotDuringHealthCheck = rt.ActiveSlot
					}
					mu.Unlock()
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(container.InspectResponse{
				ID: id,
				State: &container.State{
					Running: true,
					Status:  container.StateRunning,
					Health:  &container.Health{Status: container.Healthy},
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
		Name:           "bg-project",
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: tmpRoot,
		Status:         models.ProjectStatusRunning,
		DeploymentMode: models.DeploymentModeBlueGreen,
	}
	service := &models.Service{
		ID:                         serviceID,
		ProjectID:                  projectID,
		Name:                       "web",
		Role:                       models.RoleFrontend,
		Classification:             models.ClassificationApplication,
		DeploymentMode:             models.DeploymentModeBlueGreen,
		Status:                     models.ServiceStatusRunning,
		ContainerID:                strPtr(blueCID),
		HostPort:                   &bluePort,
		InternalPort:               8080,
		PublicExposed:              true,
		HealthStrategy:             models.HealthStrategyDocker,
		BuildStrategy:              models.BuildStrategyAuto,
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
		HealthStrategy: models.HealthStrategyDocker,
		BuildStrategy:  models.BuildStrategyAuto,
	}

	var logMessages []string
	var logMu sync.Mutex

	depSvc := services.NewDeploymentService(nil)
	depSvc.SetGetServiceDeploymentForTest(func(ctx context.Context, sdID uuid.UUID) (*models.ServiceDeployment, error) {
		cp := *sd
		return &cp, nil
	})
	depSvc.SetUpdateServiceDeploymentStatusForTest(func(ctx context.Context, sdID uuid.UUID, newStatus string, reason *string) error {
		sd.Status = newStatus
		sd.FailureReason = reason
		return nil
	})
	depSvc.SetUpdateServiceDeploymentContainerForTest(func(ctx context.Context, sdID uuid.UUID, cid string, hp *int) error {
		sd.ContainerID = &cid
		sd.HostPort = hp
		return nil
	})
	depSvc.SetAddDeploymentServiceLogForTest(func(ctx context.Context, dID, sdID, sID *uuid.UUID, phase, stream, message string) (*models.DeploymentLog, error) {
		logMu.Lock()
		logMessages = append(logMessages, message)
		logMu.Unlock()
		return &models.DeploymentLog{ID: int64(len(logMessages)), Phase: phase, Stream: stream, Message: message}, nil
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
	svcSvc.SetUpdateServiceStatusForTest(func(ctx context.Context, sID uuid.UUID, status string, cid, tag *string, hp *int) error {
		service.Status = status
		return nil
	})
	svcSvc.SetPromoteServiceDeploymentForTest(func(ctx context.Context, sID, sdID uuid.UUID, cID, img string, hp *int) error {
		if activeRouter != nil {
			if rt, ok := activeRouter.GetRoute(serviceID); ok {
				slotAtPromotion = rt.ActiveSlot
			}
		}
		service.CurrentServiceDeploymentID = &sdID
		service.ContainerID = &cID
		service.HostPort = hp
		service.Status = models.ServiceStatusRunning
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
	pm := netpkg.NewPortManager(18400, 18500)
	router := proxy.NewTrafficRouter()
	activeRouter = router

	eng := NewEngine(cli, projSvc, depSvc, nil, nil, nil, pm, pv, nil, t.TempDir())
	eng.SetServiceService(svcSvc)
	eng.SetTrafficRouter(router)
	defer eng.StopAllLogCollectors()

	require.NoError(t, eng.ExecuteServiceDeployment(ctx, newSDID))

	// During health check, proxy MUST still point to Blue while Green initializes
	assert.Equal(t, proxy.SlotBlue, slotDuringHealthCheck)
	// At promotion time, TrafficRouter has switched to Green while Blue was still alive
	assert.Equal(t, proxy.SlotGreen, slotAtPromotion)

	route, ok := router.GetRoute(serviceID)
	require.True(t, ok)
	assert.Equal(t, proxy.SlotGreen, route.ActiveSlot)
	assert.Equal(t, greenCID, route.ContainerID)
	require.NotNil(t, sd.HostPort)
	assert.Equal(t, *sd.HostPort, route.HostPort)

	// Blue must be stopped/removed AFTER Green started
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, stoppedContainers, blueCID)
	assert.Contains(t, removedContainers, blueCID)
	startGreenIdx := -1
	stopBlueIdx := -1
	for i, ev := range events {
		if ev == "start:"+greenCID && startGreenIdx == -1 {
			startGreenIdx = i
		}
		if ev == "stop:"+blueCID && stopBlueIdx == -1 {
			stopBlueIdx = i
		}
	}
	assert.NotEqual(t, -1, startGreenIdx)
	assert.NotEqual(t, -1, stopBlueIdx)
	assert.Less(t, startGreenIdx, stopBlueIdx, "Blue container must only be stopped after Green started & passed health check")

	logMu.Lock()
	joinedLogs := strings.Join(logMessages, "\n")
	logMu.Unlock()
	assert.Contains(t, joinedLogs, "BLUE-GREEN:")
	assert.Contains(t, joinedLogs, "BLUE-GREEN CUTOVER:")
}

func TestExecuteServiceDeployment_BlueGreen_FailedHealthKeepsBlueServing(t *testing.T) {
	ctx := context.Background()
	tmpRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpRoot, "Dockerfile"), []byte("FROM alpine:3.19\n"), 0644))

	blueUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("blue-still-alive"))
	}))
	defer blueUpstream.Close()

	u, err := url.Parse(blueUpstream.URL)
	require.NoError(t, err)
	bluePort, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	ownerID := uuid.New()
	projectID := uuid.New()
	serviceID := uuid.New()
	prevSDID := uuid.New()
	newSDID := uuid.New()

	blueCID := "blue-live-container-01"
	greenCID := "green-broken-cont-02"

	var mu sync.Mutex
	var stoppedContainers []string
	var removedContainers []string

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(image.InspectResponse{ID: "sha256:broken-img"})
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/tag"):
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(r.URL.Path, "/networks/create"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(network.CreateResponse{ID: "net-bg"})
		case strings.HasSuffix(r.URL.Path, "/networks"):
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode([]network.Summary{{Network: network.Network{ID: "net-bg", Name: "forgelab-net-" + projectID.String()}}})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(network.CreateResponse{ID: "net-bg"})
		case strings.Contains(r.URL.Path, "/networks/") && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(network.Inspect{Network: network.Network{ID: "net-bg", Name: "forgelab-net-" + projectID.String()}})
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(container.CreateResponse{ID: greenCID})
		case strings.HasSuffix(r.URL.Path, "/start"):
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
				ID: greenCID,
				State: &container.State{
					Running:  false,
					Status:   container.StateExited,
					ExitCode: 1,
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
		Name:           "bg-rollback-app",
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: tmpRoot,
		Status:         models.ProjectStatusRunning,
		DeploymentMode: models.DeploymentModeBlueGreen,
	}
	service := &models.Service{
		ID:                         serviceID,
		ProjectID:                  projectID,
		Name:                       "web",
		Role:                       models.RoleFrontend,
		Classification:             models.ClassificationApplication,
		DeploymentMode:             models.DeploymentModeBlueGreen,
		Status:                     models.ServiceStatusRunning,
		ContainerID:                strPtr(blueCID),
		HostPort:                   &bluePort,
		InternalPort:               8080,
		PublicExposed:              true,
		HealthStrategy:             models.HealthStrategyDocker,
		BuildStrategy:              models.BuildStrategyAuto,
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
		HealthStrategy: models.HealthStrategyDocker,
		BuildStrategy:  models.BuildStrategyAuto,
	}

	depSvc := services.NewDeploymentService(nil)
	depSvc.SetGetServiceDeploymentForTest(func(ctx context.Context, sdID uuid.UUID) (*models.ServiceDeployment, error) {
		cp := *sd
		return &cp, nil
	})
	depSvc.SetUpdateServiceDeploymentStatusForTest(func(ctx context.Context, sdID uuid.UUID, newStatus string, reason *string) error {
		sd.Status = newStatus
		sd.FailureReason = reason
		return nil
	})

	promotedCalled := false
	svcSvc := services.NewServiceService(nil)
	svcSvc.SetGetServiceForTest(func(ctx context.Context, sID uuid.UUID) (*models.Service, error) {
		cp := *service
		return &cp, nil
	})
	svcSvc.SetListServicesForTest(func(ctx context.Context, pID uuid.UUID) ([]*models.Service, error) {
		cp := *service
		return []*models.Service{&cp}, nil
	})
	svcSvc.SetUpdateServiceStatusForTest(func(ctx context.Context, sID uuid.UUID, status string, cid, tag *string, hp *int) error {
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

	router := proxy.NewTrafficRouter()
	_, err = router.SwitchTraffic(serviceID, projectID, "web", u.Hostname(), bluePort, blueCID)
	require.NoError(t, err)

	pv := security.NewPathValidator([]string{tmpRoot})
	pm := netpkg.NewPortManager(18510, 18590)
	eng := NewEngine(cli, projSvc, depSvc, nil, nil, nil, pm, pv, nil, t.TempDir())
	eng.SetServiceService(svcSvc)
	eng.SetTrafficRouter(router)
	defer eng.StopAllLogCollectors()

	err = eng.ExecuteServiceDeployment(ctx, newSDID)
	require.Error(t, err)
	assert.False(t, promotedCalled)
	assert.Equal(t, models.DeployStatusFailed, sd.Status)
	assert.Equal(t, blueCID, *service.ContainerID)
	assert.Equal(t, prevSDID, *service.CurrentServiceDeploymentID)

	mu.Lock()
	assert.Contains(t, stoppedContainers, greenCID)
	assert.Contains(t, removedContainers, greenCID)
	assert.NotContains(t, stoppedContainers, blueCID)
	assert.NotContains(t, removedContainers, blueCID)
	mu.Unlock()

	// Verify TrafficRouter still routes requests live to Blue upstream!
	route, ok := router.GetRoute(serviceID)
	require.True(t, ok)
	assert.Equal(t, proxy.SlotBlue, route.ActiveSlot)
	assert.Equal(t, blueCID, route.ContainerID)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	router.ServeServiceHTTP(serviceID, rec, req)
	respBody, _ := io.ReadAll(rec.Result().Body)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "blue-still-alive", string(respBody))
	assert.Equal(t, proxy.SlotBlue, rec.Header().Get("X-ForgeLAB-Slot"))
}

func TestExecuteServiceDeployment_BlueGreen_StatefulFallback(t *testing.T) {
	ctx := context.Background()
	tmpRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpRoot, "Dockerfile"), []byte("FROM alpine:3.19\n"), 0644))

	ownerID := uuid.New()
	projectID := uuid.New()
	serviceID := uuid.New()
	prevSDID := uuid.New()
	newSDID := uuid.New()

	prevCID := "stateful-prev-cont-01"
	newCID := "stateful-new-cont-02"
	prevPort := 18610

	var mu sync.Mutex
	var events []string

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(image.InspectResponse{ID: "sha256:stateful-img"})
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/tag"):
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(r.URL.Path, "/volumes/create"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"Name": "data"})
		case strings.HasSuffix(r.URL.Path, "/networks/create"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(network.CreateResponse{ID: "net-s"})
		case strings.HasSuffix(r.URL.Path, "/networks"):
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode([]network.Summary{{Network: network.Network{ID: "net-s", Name: "forgelab-net-" + projectID.String()}}})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(network.CreateResponse{ID: "net-s"})
		case strings.Contains(r.URL.Path, "/networks/") && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(network.Inspect{Network: network.Network{ID: "net-s", Name: "forgelab-net-" + projectID.String()}})
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(container.CreateResponse{ID: newCID})
		case strings.HasSuffix(r.URL.Path, "/start"):
			id := extractContainerID(r.URL.Path, "/start")
			mu.Lock()
			events = append(events, "start:"+id)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/stop"):
			id := extractContainerID(r.URL.Path, "/stop")
			mu.Lock()
			events = append(events, "stop:"+id)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			id := extractContainerID(r.URL.Path, "/json")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(container.InspectResponse{
				ID: id,
				State: &container.State{
					Running: true,
					Status:  container.StateRunning,
					Health:  &container.Health{Status: container.Healthy},
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

	volCfg := []models.VolumeMountConfig{{Source: "data-vol", Target: "/data", Type: models.VolumeTypeNamed, ReadOnly: false}}
	project := &models.Project{
		ID:             projectID,
		OwnerID:        ownerID,
		Name:           "stateful-bg-app",
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: tmpRoot,
		Status:         models.ProjectStatusRunning,
		DeploymentMode: models.DeploymentModeBlueGreen,
	}
	service := &models.Service{
		ID:                         serviceID,
		ProjectID:                  projectID,
		Name:                       "app",
		Role:                       models.RoleFrontend,
		Classification:             models.ClassificationApplication,
		DeploymentMode:             models.DeploymentModeBlueGreen,
		Volumes:                    volCfg,
		Status:                     models.ServiceStatusRunning,
		ContainerID:                strPtr(prevCID),
		HostPort:                   &prevPort,
		InternalPort:               8080,
		PublicExposed:              true,
		HealthStrategy:             models.HealthStrategyDocker,
		BuildStrategy:              models.BuildStrategyAuto,
		CurrentServiceDeploymentID: &prevSDID,
	}

	digestRef := "alpine:3.19"
	sd := &models.ServiceDeployment{
		ID:             newSDID,
		ServiceID:      serviceID,
		ServiceName:    "app",
		DeployNumber:   2,
		Status:         models.DeployStatusQueued,
		ExecutionMode:  models.ExecutionModeReuseImage,
		ImageDigest:    &digestRef,
		ImageTag:       &digestRef,
		Volumes:        volCfg,
		InternalPort:   8080,
		HealthStrategy: models.HealthStrategyDocker,
		BuildStrategy:  models.BuildStrategyAuto,
	}

	var logMessages []string
	var logMu sync.Mutex

	depSvc := services.NewDeploymentService(nil)
	depSvc.SetGetServiceDeploymentForTest(func(ctx context.Context, sdID uuid.UUID) (*models.ServiceDeployment, error) {
		cp := *sd
		return &cp, nil
	})
	depSvc.SetUpdateServiceDeploymentStatusForTest(func(ctx context.Context, sdID uuid.UUID, newStatus string, reason *string) error {
		sd.Status = newStatus
		return nil
	})
	depSvc.SetAddDeploymentServiceLogForTest(func(ctx context.Context, dID, sdID, sID *uuid.UUID, phase, stream, message string) (*models.DeploymentLog, error) {
		logMu.Lock()
		logMessages = append(logMessages, message)
		logMu.Unlock()
		return &models.DeploymentLog{ID: int64(len(logMessages)), Phase: phase, Stream: stream, Message: message}, nil
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
	svcSvc.SetUpdateServiceStatusForTest(func(ctx context.Context, sID uuid.UUID, status string, cid, tag *string, hp *int) error {
		return nil
	})
	svcSvc.SetPromoteServiceDeploymentForTest(func(ctx context.Context, sID, sdID uuid.UUID, cID, img string, hp *int) error {
		return nil
	})

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetServiceService(svcSvc)
	projSvc.SetProjectByIDForTest(func(ctx context.Context, pID uuid.UUID) (*models.Project, error) {
		cp := *project
		return &cp, nil
	})
	projSvc.SetUpdateProjectStatusForTest(func(ctx context.Context, pID uuid.UUID, status string) error {
		return nil
	})

	pv := security.NewPathValidator([]string{tmpRoot})
	pm := netpkg.NewPortManager(18620, 18690)
	eng := NewEngine(cli, projSvc, depSvc, nil, nil, nil, pm, pv, nil, t.TempDir())
	eng.SetServiceService(svcSvc)
	eng.SetTrafficRouter(proxy.NewTrafficRouter())
	defer eng.StopAllLogCollectors()

	require.NoError(t, eng.ExecuteServiceDeployment(ctx, newSDID))

	mu.Lock()
	stopPrevIdx := -1
	startNewIdx := -1
	for i, ev := range events {
		if ev == "stop:"+prevCID && stopPrevIdx == -1 {
			stopPrevIdx = i
		}
		if ev == "start:"+newCID && startNewIdx == -1 {
			startNewIdx = i
		}
	}
	mu.Unlock()

	assert.NotEqual(t, -1, stopPrevIdx)
	assert.NotEqual(t, -1, startNewIdx)
	assert.Less(t, stopPrevIdx, startNewIdx, "stateful service must stop previous container before starting replacement")

	logMu.Lock()
	joinedLogs := strings.Join(logMessages, "\n")
	logMu.Unlock()
	assert.Contains(t, joinedLogs, "stop-before-start fallback")
}
