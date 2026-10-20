package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"unicode"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dockerimage "github.com/docker/docker/api/types/image"
	dockernetwork "github.com/docker/docker/api/types/network"
	dockervolume "github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/agent"
	"github.com/forgelab/backend/internal/detector"
	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/logging"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/network"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
	"github.com/forgelab/backend/internal/tararchive"
	ws "github.com/forgelab/backend/internal/websocket"
)

type sharedBuildPromise = SharedBuildPromise

type activeExecEntry struct {
	id        uuid.UUID
	projectID uuid.UUID
	cancel    context.CancelFunc
	done      chan struct{}
}

// ReconciliationState represents the operational reconciliation status between Docker daemon and database.
type ReconciliationState struct {
	Status         string     `json:"status"` // "pending", "completed", "failed", "unreachable"
	LastReconciled *time.Time `json:"last_reconciled,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	Authoritative  bool       `json:"authoritative"`
}

type Engine struct {
	clientMu             sync.Mutex // synchronizes streaming transport wrapping and client updates
	dockerClient         *client.Client
	projectService       *services.ProjectService
	deploymentService    *services.DeploymentService
	secretService        *services.SecretService
	sourceService        *services.SourceService
	githubService        *services.GitHubService
	serviceService       *services.ServiceService
	portManager          *network.PortManager
	pathValidator        *security.PathValidator
	wsHub                *ws.Hub
	workDir              string
	localBuildMode       string
	maxConcurrentBuilds  int
	buildSem             *BuildSemaphore
	semMu                sync.Mutex
	buildMaintenanceMu   sync.RWMutex          // synchronizes Docker image builds vs image pruning/maintenance
	pruneFilterLabel     string                // optional label filter for scoped image pruning
	activeLogCollectors  sync.Map              // map[string]context.CancelFunc — tracks running log collector goroutines
	collectorRegistry    *LogCollectorRegistry // race-safe registry and operational diagnostics for runtime log collectors
	activeCancels        sync.Map              // map[uuid.UUID]context.CancelFunc — tracks active deployment execution cancel funcs
	activeExecMu         sync.Mutex            // protects activeExecs map
	activeExecs          map[uuid.UUID]*activeExecEntry
	activeContainers     sync.Map                // map[string]time.Time — containerID -> creation timestamp, tracks in-flight containers during deployment
	sharedBuildArtifacts sync.Map                // map[string]*sharedBuildPromise — deduplicates and coordinates builds for identical context+dockerfile
	sharedCoordMu        sync.Mutex              // protects initialization and mutation of sharedCoordinator
	sharedCoordinator    *SharedBuildCoordinator // Coordinates shared builds across services with completion guarantees
	cachedBuilderVer     atomic.Value            // stores types.BuilderVersion advertised by Docker daemon
	startTime            time.Time               // instance boot timestamp for tracking interrupted deployments
	publicHost           string                  // host name or IP used for browser-facing URLs
	reconcileMu          sync.RWMutex
	reconcileState       ReconciliationState
}

func (e *Engine) trackActiveContainer(id string) {
	if id == "" {
		return
	}
	now := time.Now()
	e.activeContainers.Store(id, now)
	if len(id) > 12 {
		e.activeContainers.Store(id[:12], now)
	}
}

func (e *Engine) untrackActiveContainer(id string) {
	if id == "" {
		return
	}
	e.activeContainers.Delete(id)
	if len(id) > 12 {
		e.activeContainers.Delete(id[:12])
	}
}

func (e *Engine) isContainerTracked(id string) bool {
	if id == "" {
		return false
	}
	if _, ok := e.activeContainers.Load(id); ok {
		return true
	}
	if len(id) > 12 {
		if _, ok := e.activeContainers.Load(id[:12]); ok {
			return true
		}
	}
	return false
}

func NewEngine(
	dockerClient *client.Client,
	projectService *services.ProjectService,
	deploymentService *services.DeploymentService,
	secretService *services.SecretService,
	sourceService *services.SourceService,
	githubService *services.GitHubService,
	portManager *network.PortManager,
	pathValidator *security.PathValidator,
	wsHub *ws.Hub,
	workDir string,
) *Engine {
	if workDir == "" {
		workDir = "./data/builds"
	}
	_ = os.MkdirAll(workDir, 0755)

	return &Engine{
		dockerClient:        dockerClient,
		projectService:      projectService,
		deploymentService:   deploymentService,
		secretService:       secretService,
		sourceService:       sourceService,
		githubService:       githubService,
		serviceService:      services.NewServiceService(nil),
		portManager:         portManager,
		pathValidator:       pathValidator,
		wsHub:               wsHub,
		workDir:             workDir,
		localBuildMode:      "snapshot",
		maxConcurrentBuilds: DefaultMaxConcurrentBuilds(),
		buildSem:            NewBuildSemaphore(DefaultMaxConcurrentBuilds()),
		sharedCoordinator:   NewSharedBuildCoordinator(),
		activeExecs:         make(map[uuid.UUID]*activeExecEntry),
		collectorRegistry:   NewLogCollectorRegistry(),
		startTime:           time.Now(),
		reconcileState: ReconciliationState{
			Status:        "pending",
			Authoritative: false,
		},
	}
}

// GetReconciliationState returns the current reconciliation status and authoritativeness.
func (e *Engine) GetReconciliationState() ReconciliationState {
	e.reconcileMu.RLock()
	defer e.reconcileMu.RUnlock()
	return e.reconcileState
}

// IsReconciliationAuthoritative returns whether Docker engine and container state
// have been successfully reconciled and are authoritative.
func (e *Engine) IsReconciliationAuthoritative() bool {
	e.reconcileMu.RLock()
	defer e.reconcileMu.RUnlock()
	return e.reconcileState.Authoritative
}

func (e *Engine) getCollectorRegistry() *LogCollectorRegistry {
	if e.collectorRegistry == nil {
		e.semMu.Lock()
		if e.collectorRegistry == nil {
			e.collectorRegistry = NewLogCollectorRegistry()
		}
		e.semMu.Unlock()
	}
	return e.collectorRegistry
}

// GetLogCollectorDiagnostics returns operational observability metrics for all runtime log collectors.
func (e *Engine) GetLogCollectorDiagnostics() LogCollectorDiagnostics {
	return e.getCollectorRegistry().GetDiagnostics()
}

func (e *Engine) getBuildSemaphore() *BuildSemaphore {
	e.semMu.Lock()
	defer e.semMu.Unlock()
	if e.buildSem == nil {
		lim := e.maxConcurrentBuilds
		if lim <= 0 {
			lim = DefaultMaxConcurrentBuilds()
		}
		e.buildSem = NewBuildSemaphore(lim)
	}
	return e.buildSem
}

// SetMaxConcurrentBuilds configures the global Docker build concurrency limit.
func (e *Engine) SetMaxConcurrentBuilds(n int) {
	if n > 0 {
		e.maxConcurrentBuilds = n
		e.getBuildSemaphore().SetLimit(n)
	}
}

// GetMaxConcurrentBuilds returns the current global Docker build concurrency limit.
func (e *Engine) GetMaxConcurrentBuilds() int {
	return e.maxConcurrentBuilds
}

func (e *Engine) getSharedCoordinator() *SharedBuildCoordinator {
	e.sharedCoordMu.Lock()
	defer e.sharedCoordMu.Unlock()
	if e.sharedCoordinator == nil {
		e.sharedCoordinator = NewSharedBuildCoordinator()
	}
	return e.sharedCoordinator
}

func (e *Engine) getOrInitSharedBuild(key string) (*sharedBuildPromise, bool) {
	coord := e.getSharedCoordinator()
	p, loaded := coord.GetOrInit(key, "")
	e.sharedBuildArtifacts.Store(key, p)
	return p, loaded
}

func isSyntheticAutoStartCommand(cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	switch trimmed {
	case "/app/server", "java -jar /app/app.jar", "./target/release/server":
		return true
	}
	return false
}

func parseCommandToArgs(cmdStr string) []string {
	cmdStr = strings.TrimSpace(cmdStr)
	if cmdStr == "" {
		return nil
	}
	if strings.HasPrefix(cmdStr, "[") && strings.HasSuffix(cmdStr, "]") {
		var parts []string
		if err := json.Unmarshal([]byte(cmdStr), &parts); err == nil && len(parts) > 0 {
			return parts
		}
	}
	var parts []string
	var current strings.Builder
	inQuotes := false
	quoteChar := rune(0)
	for _, r := range cmdStr {
		if inQuotes {
			if r == quoteChar {
				inQuotes = false
			} else {
				current.WriteRune(r)
			}
		} else {
			if r == '"' || r == '\'' {
				inQuotes = true
				quoteChar = r
			} else if unicode.IsSpace(r) {
				if current.Len() > 0 {
					parts = append(parts, current.String())
					current.Reset()
				}
			} else {
				current.WriteRune(r)
			}
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// SetPruneFilterLabel configures an optional label filter for scoped image pruning.
func (e *Engine) SetPruneFilterLabel(label string) {
	e.pruneFilterLabel = label
}

// GetPruneFilterLabel returns the configured label filter for scoped image pruning.
func (e *Engine) GetPruneFilterLabel() string {
	return e.pruneFilterLabel
}

// GetBuildWaitersCount returns the number of builds waiting on the BuildSemaphore.
func (e *Engine) GetBuildWaitersCount() int {
	return e.getBuildSemaphore().GetWaitersCount()
}

// GetBuildDiagnostics returns a structured snapshot of current build slot capacity, active slots, and waiting builds.
func (e *Engine) GetBuildDiagnostics() SemaphoreDiagnostics {
	return e.getBuildSemaphore().GetDiagnostics()
}

// GetSharedBuildDiagnostics returns a diagnostic snapshot of in-flight and completed shared build artifacts.
func (e *Engine) GetSharedBuildDiagnostics() SharedBuildDiagnostics {
	return e.getSharedCoordinator().GetDiagnostics()
}

// CleanupDeploymentSharedBuilds cleans up completed shared build entries for a project.
func (e *Engine) CleanupDeploymentSharedBuilds(projectID string) int {
	return e.getSharedCoordinator().CleanupDeployment(projectID)
}

func (e *Engine) registerActiveExec(id, projectID uuid.UUID, cancel context.CancelFunc) chan struct{} {
	e.activeExecMu.Lock()
	defer e.activeExecMu.Unlock()
	done := make(chan struct{})
	e.activeExecs[id] = &activeExecEntry{
		id:        id,
		projectID: projectID,
		cancel:    cancel,
		done:      done,
	}
	e.activeCancels.Store(id, cancel)
	return done
}

func (e *Engine) unregisterActiveExec(id uuid.UUID) {
	e.activeExecMu.Lock()
	if entry, ok := e.activeExecs[id]; ok {
		delete(e.activeExecs, id)
		close(entry.done)
	}
	e.activeExecMu.Unlock()
	e.activeCancels.Delete(id)
}

// IsActiveExecution reports whether a release deployment or service deployment is currently actively executing in memory.
func (e *Engine) IsActiveExecution(id uuid.UUID) bool {
	e.activeExecMu.Lock()
	defer e.activeExecMu.Unlock()
	_, ok := e.activeExecs[id]
	return ok
}

// RegisterActiveCancel registers a cancellation function for an active deployment execution.
func (e *Engine) RegisterActiveCancel(deploymentID uuid.UUID, cancel context.CancelFunc) {
	e.activeCancels.Store(deploymentID, cancel)
}

// UnregisterActiveCancel removes the cancellation function for a deployment.
func (e *Engine) UnregisterActiveCancel(deploymentID uuid.UUID) {
	e.activeCancels.Delete(deploymentID)
}

// CancelActiveDeployment cancels the context of an actively executing deployment if present.
func (e *Engine) CancelActiveDeployment(deploymentID uuid.UUID) bool {
	e.activeExecMu.Lock()
	entry, ok := e.activeExecs[deploymentID]
	e.activeExecMu.Unlock()
	if ok && entry.cancel != nil {
		entry.cancel()
		return true
	}
	if val, ok := e.activeCancels.Load(deploymentID); ok {
		if cancel, isFunc := val.(context.CancelFunc); isFunc {
			cancel()
			return true
		}
	}
	return false
}

// CancelAndDrainProjectDeployments cancels all active deployment contexts for the given project
// and synchronously waits for their worker goroutines to exit before returning.
func (e *Engine) CancelAndDrainProjectDeployments(ctx context.Context, projectID uuid.UUID) error {
	e.activeExecMu.Lock()
	var toDrain []chan struct{}
	for _, entry := range e.activeExecs {
		if entry.projectID == projectID {
			if entry.cancel != nil {
				entry.cancel()
			}
			toDrain = append(toDrain, entry.done)
		}
	}
	e.activeExecMu.Unlock()

	for _, done := range toDrain {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

type buildSlotContextKeyType struct{}

var buildSlotContextKey = buildSlotContextKeyType{}

func getDockerBuildTimeout() time.Duration {
	if raw := os.Getenv("FORGELAB_DOCKER_BUILD_TIMEOUT"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
		if mins, err := strconv.Atoi(raw); err == nil && mins > 0 {
			return time.Duration(mins) * time.Minute
		}
	}
	return 45 * time.Minute
}

// SetServiceService configures the service management service.
func (e *Engine) SetServiceService(ss *services.ServiceService) {
	e.serviceService = ss
}

// recalculateAndUpdateProjectStatus derives the project status from its services and persists it.
func (e *Engine) recalculateAndUpdateProjectStatus(ctx context.Context, projectID uuid.UUID) (string, error) {
	if e.serviceService == nil {
		return models.ProjectStatusInactive, nil
	}
	svcs, err := e.serviceService.ListServices(ctx, projectID)
	if err != nil {
		return "", err
	}
	if len(svcs) == 0 {
		return models.ProjectStatusInactive, nil
	}
	newStatus := services.CalculateProjectStatus(svcs)
	if e.projectService != nil {
		_ = e.projectService.UpdateProjectStatus(ctx, projectID, newStatus)
	}
	if e.wsHub != nil {
		projectChannel := fmt.Sprintf("project:%s", projectID.String())
		_ = e.wsHub.PublishEvent(projectChannel, &ws.EventMessage{
			Type:    "project_status",
			Channel: projectChannel,
			Data: map[string]interface{}{
				"project_id": projectID.String(),
				"status":     newStatus,
			},
		})
	}
	return newStatus, nil
}

// toCoarseServiceStatus maps a detailed deployment phase to a coarse service-level status.
// Service.status must remain coarse only: inactive, deploying, running, stopped, failed.
// If the deployment fails or crashes, but a previous healthy container is still running,
// the service status remains running.
func toCoarseServiceStatus(deployStatus string, hasPreviousHealthy bool) string {
	switch deployStatus {
	case models.DeployStatusQueued,
		models.DeployStatusCloning,
		models.DeployStatusBuilding,
		models.DeployStatusStarting,
		models.DeployStatusHealthChecking:
		return models.ServiceStatusDeploying
	case models.DeployStatusRunning:
		return models.ServiceStatusRunning
	case models.DeployStatusStopped:
		return models.ServiceStatusStopped
	case models.DeployStatusFailed, models.DeployStatusCrashed:
		if hasPreviousHealthy {
			return models.ServiceStatusRunning
		}
		return models.ServiceStatusFailed
	default:
		return models.ServiceStatusDeploying
	}
}

// ExecuteServiceDeployment runs the deployment pipeline for a single service deployment unit:
// source -> build -> container -> start -> health check -> promote service -> cleanup old service.
// It enforces the safety invariant: a failed deployment never replaces the previous healthy service container.
func (e *Engine) ExecuteServiceDeployment(ctx context.Context, serviceDeploymentID uuid.UUID) error {
	serviceDeploy, err := e.deploymentService.GetServiceDeployment(ctx, serviceDeploymentID)
	if err != nil {
		return fmt.Errorf("failed to get service deployment %s: %w", serviceDeploymentID, err)
	}

	// Stale / superseded deployment check: if already terminal (e.g. superseded while in queue or cancelled), do not run!
	if models.IsDeploymentTerminalStatus(serviceDeploy.Status) {
		// If service deployment was prematurely marked failed by backend restart reconciliation,
		// but its parent release deployment is actively running right now, recover it to queued!
		if serviceDeploy.Status == models.DeployStatusFailed &&
			serviceDeploy.FailureReason != nil &&
			strings.Contains(*serviceDeploy.FailureReason, "interrupted: backend restarted") &&
			serviceDeploy.DeploymentID != nil &&
			e.IsActiveExecution(*serviceDeploy.DeploymentID) {
			slog.Info("recovering service deployment prematurely marked interrupted by backend restart", "service_deployment_id", serviceDeploymentID)
			_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, serviceDeploymentID, models.DeployStatusQueued, nil)
			serviceDeploy.Status = models.DeployStatusQueued
		} else {
			slog.Info("service deployment already in terminal state; skipping execution", "service_deployment_id", serviceDeploymentID, "status", serviceDeploy.Status)
			if serviceDeploy.Status == models.DeployStatusRunning {
				return nil
			}
			return fmt.Errorf("service deployment %s is already in terminal state '%s'", serviceDeploymentID, serviceDeploy.Status)
		}
	}
	if e.deploymentService != nil {
		if superseded, err := e.deploymentService.IsServiceDeploymentSuperseded(ctx, serviceDeploy.ServiceID, serviceDeploy.DeployNumber); err == nil && superseded {
			slog.Info("service deployment superseded by newer deployment; skipping execution", "service_deployment_id", serviceDeploymentID, "deploy_number", serviceDeploy.DeployNumber)
			return fmt.Errorf("service deployment %s superseded by newer deployment", serviceDeploymentID)
		}
	}

	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()

	service, err := e.serviceService.GetService(execCtx, serviceDeploy.ServiceID)
	if err != nil {
		return fmt.Errorf("failed to get service %s: %w", serviceDeploy.ServiceID, err)
	}

	project, err := e.getProjectByID(execCtx, service.ProjectID)
	if err != nil {
		return fmt.Errorf("failed to get project %s: %w", service.ProjectID, err)
	}

	if service.ProjectID != project.ID {
		return fmt.Errorf("service %s does not belong to project %s", service.ID, project.ID)
	}

	if serviceDeploy.DeploymentID != nil {
		parentDeploy, err := e.deploymentService.GetDeployment(ctx, *serviceDeploy.DeploymentID)
		if err != nil {
			return fmt.Errorf("failed to get parent deployment: %w", err)
		}
		if parentDeploy.ProjectID != project.ID {
			return fmt.Errorf("parent deployment %s does not belong to project %s", parentDeploy.ID, project.ID)
		}
	}

	e.registerActiveExec(serviceDeploymentID, project.ID, execCancel)
	defer e.unregisterActiveExec(serviceDeploymentID)

	if project.Status == models.ProjectStatusDeleting {
		return errors.New("deployment aborted: project deletion is in progress")
	}

	// Capture previous service state for safe rollback if health checks or promotion fail
	previousServiceStatus := service.Status
	previousContainerID := service.ContainerID
	previousDeploymentID := service.CurrentServiceDeploymentID
	hasPreviousHealthy := (previousServiceStatus == models.ServiceStatusRunning && previousContainerID != nil && *previousContainerID != "")

	redactor := logging.NewLogRedactor()
	var runtimeEnvMap map[string]string
	var normalBuildArgs map[string]string
	var secretBuildVars map[string]string

	if e.secretService != nil {
		if len(serviceDeploy.EnvSnapshot) > 0 {
			runtimeEnvMap, _, _ = e.secretService.GetEnvMapFromSnapshot(serviceDeploy.EnvSnapshot, models.EnvScopeRuntime)
		} else {
			runtimeEnvMap, _, _ = e.secretService.GetDecryptedEnvMap(execCtx, project.ID, &service.ID, models.EnvScopeRuntime)
		}
		normalArgs, secretVars, allSecrets, err := e.secretService.GetBuildVariables(execCtx, project.ID, &service.ID, serviceDeploy.EnvSnapshot)
		if err == nil {
			normalBuildArgs = normalArgs
			secretBuildVars = secretVars
			redactor.SetSecrets(allSecrets)
		}
	}

	emitLog := func(phase, stream, message string) {
		redactedMsg := redactor.Redact(message)
		persistedLog, err := e.deploymentService.AddDeploymentServiceLog(ctx, serviceDeploy.DeploymentID, &serviceDeploy.ID, &service.ID, phase, stream, redactedMsg)
		if err != nil {
			slog.Error("failed to persist service deployment log", "service_deployment_id", serviceDeploy.ID, "phase", phase, "error", err)
			return
		}
		if e.wsHub != nil {
			data := map[string]interface{}{
				"id":                    persistedLog.ID,
				"sequence":              persistedLog.ID,
				"service_deployment_id": serviceDeploy.ID.String(),
				"service_id":            service.ID.String(),
				"service_name":          service.Name,
				"timestamp":             persistedLog.Timestamp.Format(time.RFC3339Nano),
				"phase":                 persistedLog.Phase,
				"stream":                persistedLog.Stream,
				"message":               persistedLog.Message,
			}

			// ALWAYS publish to the canonical service-deployment channel (works for independent AND release deploys)
			sdChannel := fmt.Sprintf("service-deployment:%s", serviceDeploy.ID.String())
			_ = e.wsHub.PublishEvent(sdChannel, &ws.EventMessage{
				Type:     "log",
				Channel:  sdChannel,
				Sequence: persistedLog.ID,
				Data:     data,
			})

			// If part of a release, also publish to the release-scoped channels
			if serviceDeploy.DeploymentID != nil {
				data["deployment_id"] = serviceDeploy.DeploymentID.String()
				serviceChannel := fmt.Sprintf("deployment:%s:service:%s", serviceDeploy.DeploymentID.String(), service.ID.String())
				_ = e.wsHub.PublishEvent(serviceChannel, &ws.EventMessage{
					Type:     "log",
					Channel:  serviceChannel,
					Sequence: persistedLog.ID,
					Data:     data,
				})
				channel := "deployment:" + serviceDeploy.DeploymentID.String()
				_ = e.wsHub.PublishEvent(channel, &ws.EventMessage{
					Type:     "log",
					Channel:  channel,
					Sequence: persistedLog.ID,
					Data:     data,
				})
			}
			projectChannel := fmt.Sprintf("project:%s", project.ID.String())
			_ = e.wsHub.PublishEvent(projectChannel, &ws.EventMessage{
				Type:     "service_deployment_log",
				Channel:  projectChannel,
				Sequence: persistedLog.ID,
				Data:     data,
			})
		}
	}

	emitStatus := func(newStatus string, hostPort *int, failureReason *string) {
		if err := e.deploymentService.UpdateServiceDeploymentStatus(ctx, serviceDeploy.ID, newStatus, failureReason); err != nil {
			slog.Warn("failed to update service deployment status in db", "service_deployment_id", serviceDeploy.ID, "status", newStatus, "error", err)
			return
		}
		coarseStatus := toCoarseServiceStatus(newStatus, hasPreviousHealthy)
		_ = e.serviceService.UpdateServiceStatus(ctx, service.ID, coarseStatus, nil, nil, nil)
		newProjStatus, _ := e.recalculateAndUpdateProjectStatus(ctx, project.ID)

		if e.wsHub != nil {
			data := map[string]interface{}{
				"service_deployment_id": serviceDeploy.ID.String(),
				"service_id":            service.ID.String(),
				"service_name":          service.Name,
				"deploy_number":         serviceDeploy.DeployNumber,
				"new_status":            newStatus,
				"service_status":        coarseStatus,
				"timestamp":             time.Now().Format(time.RFC3339),
			}
			if hostPort != nil {
				data["host_port"] = *hostPort
			}
			if failureReason != nil {
				data["failure_reason"] = *failureReason
			}

			// Broadcast to the canonical service-deployment channel
			sdChannel := fmt.Sprintf("service-deployment:%s", serviceDeploy.ID.String())
			_ = e.wsHub.PublishEvent(sdChannel, &ws.EventMessage{
				Type:    "status_change",
				Channel: sdChannel,
				Data:    data,
			})

			if serviceDeploy.DeploymentID != nil {
				data["deployment_id"] = serviceDeploy.DeploymentID.String()
				serviceChannel := fmt.Sprintf("deployment:%s:service:%s", serviceDeploy.DeploymentID.String(), service.ID.String())
				_ = e.wsHub.PublishEvent(serviceChannel, &ws.EventMessage{
					Type:    "status_change",
					Channel: serviceChannel,
					Data:    data,
				})
			}
			projectChannel := fmt.Sprintf("project:%s", project.ID.String())
			_ = e.wsHub.PublishEvent(projectChannel, &ws.EventMessage{
				Type:    "service_status_change",
				Channel: projectChannel,
				Data:    data,
			})
			_ = e.wsHub.PublishEvent(projectChannel, &ws.EventMessage{
				Type:    "project_status",
				Channel: projectChannel,
				Data: map[string]interface{}{
					"project_id": project.ID.String(),
					"status":     newProjStatus,
				},
			})
		}
	}

	emitLog(models.LogPhaseSource, models.LogStreamSystem, fmt.Sprintf("Starting service deployment #%d for '%s' (role: %s, runtime: %s)...", serviceDeploy.DeployNumber, service.Name, service.Role, service.RuntimeType))
	emitLog(models.LogPhaseSource, models.LogStreamSystem, fmt.Sprintf("Diagnostic context: source_type='%s', source_ref='%s', root_dir='%s', build_strategy='%s', internal_port=%d", project.SourceType, project.SourceReference, project.BuildContext, service.BuildStrategy, service.InternalPort))
	slog.Info("executing service deployment",
		"service_deployment_id", serviceDeploy.ID,
		"service_id", service.ID,
		"project_id", project.ID,
		"source_type", project.SourceType,
		"source_reference", project.SourceReference,
		"root_dir", project.BuildContext,
		"build_strategy", service.BuildStrategy,
	)

	allProjectServices, err := e.serviceService.ListServices(execCtx, project.ID)
	if err != nil {
		allProjectServices = []*models.Service{service}
	}
	servicesMap := make(map[string]*models.Service, len(allProjectServices))
	for _, s := range allProjectServices {
		if sVols, sStripped := security.SanitizeServiceVolumes(s.Volumes); sStripped {
			s.Volumes = sVols
			if e.serviceService != nil {
				_ = e.serviceService.UpdateServiceVolumes(execCtx, s.ID, sVols)
			}
		}
		servicesMap[s.Name] = s
	}

	// Sanitize tenant volumes: strip forbidden host Docker socket mounts before preflight validation
	safeVols, stripped := security.SanitizeServiceVolumes(serviceDeploy.Volumes)
	if stripped {
		serviceDeploy.Volumes = safeVols
		emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("[security] Notice: stripped host Docker socket mount from service '%s' (tenant containers are sandboxed and cannot access host Docker engine)", serviceDeploy.ServiceName))
		if e.deploymentService != nil {
			_ = e.deploymentService.UpdateServiceDeploymentVolumes(execCtx, serviceDeploy.ID, safeVols)
		}
	}

	// Preflight validation for single service deployment: verifies service config and that dependencies exist in project
	if err := ValidateDeploymentPreflight(execCtx, PreflightOptions{
		Project:             project,
		Deployments:         []*models.ServiceDeployment{serviceDeploy},
		ServicesMap:         servicesMap,
		PathValidator:       e.pathValidator,
		SourceService:       e.sourceService,
		DockerClient:        e.dockerClient,
		IsServiceDeployment: true,
	}); err != nil {
		reason := fmt.Sprintf("Preflight validation failed: %v", err)
		emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
		emitStatus(models.DeployStatusFailed, nil, &reason)
		return errors.New(reason)
	}

	svcTag := fmt.Sprintf("forgelab/%s/%s:%d", project.ID, service.Name, serviceDeploy.DeployNumber)
	runImage := svcTag
	reusingExistingImage := false

	// Check if this deployment is configured to reuse an existing immutable image (e.g. rollback)
	if serviceDeploy.ExecutionMode == models.ExecutionModeReuseImage || (serviceDeploy.ImageDigest != nil && *serviceDeploy.ImageDigest != "") {
		candidateImage := ""
		if serviceDeploy.ImageDigest != nil && *serviceDeploy.ImageDigest != "" {
			candidateImage = *serviceDeploy.ImageDigest
		} else if serviceDeploy.ImageTag != nil && *serviceDeploy.ImageTag != "" {
			candidateImage = *serviceDeploy.ImageTag
		}

		if candidateImage != "" && e.dockerClient != nil {
			if inspect, _, err := e.dockerClient.ImageInspectWithRaw(execCtx, candidateImage); err == nil {
				reusingExistingImage = true
				runImage = candidateImage
				idShort := inspect.ID
				if len(idShort) > 12 {
					idShort = idShort[:12]
				}
				emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Execution path: reusing existing immutable image '%s' (image ID: %s). Skipping source cloning and image build.", candidateImage, idShort))
				_ = e.dockerClient.ImageTag(execCtx, candidateImage, svcTag)
			} else {
				// If explicitly in reuse_image mode (rollback), fail closed; do not silently rebuild from current source!
				if serviceDeploy.ExecutionMode == models.ExecutionModeReuseImage {
					reason := fmt.Sprintf("Rollback failed closed: immutable image '%s' is unavailable in Docker daemon (%v); will not rebuild from current source", candidateImage, err)
					emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
					emitStatus(models.DeployStatusFailed, nil, &reason)
					return errors.New(reason)
				}
				emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Stored immutable image '%s' not cached in Docker daemon (%v). Falling back to source build pipeline.", candidateImage, err))
			}
		} else if serviceDeploy.ExecutionMode == models.ExecutionModeReuseImage {
			reason := "Rollback failed closed: no immutable image digest or tag is available for reuse"
			if candidateImage != "" && e.dockerClient == nil {
				reason = fmt.Sprintf("Rollback failed closed: Docker daemon client unavailable to verify image '%s'", candidateImage)
			}
			emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return errors.New(reason)
		}
	}

	// Detect if this service shares an image with another project service that defines a build configuration
	// (e.g. migrate shares forgelab-backend:latest with backend)
	if service.Image != "" && (service.DockerfilePath == "" || service.BuildContext == "" || service.BuildStrategy == models.BuildStrategyImage || serviceDeploy.BuildStrategy == models.BuildStrategyImage) && e.serviceService != nil {
		if allSvcs, err := e.serviceService.ListServices(execCtx, project.ID); err == nil {
			for _, other := range allSvcs {
				if other.ID != service.ID && strings.TrimSpace(other.Image) == strings.TrimSpace(service.Image) && (other.DockerfilePath != "" || other.BuildContext != "" || other.BuildStrategy == models.BuildStrategyDockerfile) {
					service.BuildStrategy = models.BuildStrategyDockerfile
					serviceDeploy.BuildStrategy = models.BuildStrategyDockerfile
					if service.BuildContext == "" || service.BuildContext == "." {
						service.BuildContext = other.BuildContext
						serviceDeploy.BuildContext = other.BuildContext
					}
					if service.DockerfilePath == "" {
						service.DockerfilePath = other.DockerfilePath
						serviceDeploy.DockerfilePath = other.DockerfilePath
					}
					if service.SourcePath == "" || service.SourcePath == "." {
						service.SourcePath = other.SourcePath
					}
					break
				}
			}
		}
	}

	// Strict image build-strategy enforcement: image strategy NEVER attempts source or Dockerfile build
	isImageStrategy := service.Classification == models.ClassificationInfrastructure ||
		serviceDeploy.Classification == models.ClassificationInfrastructure ||
		serviceDeploy.BuildStrategy == models.BuildStrategyImage ||
		service.BuildStrategy == models.BuildStrategyImage ||
		(service.Image != "" && (service.DockerfilePath == "" || service.BuildStrategy == models.BuildStrategyImage))

	if !reusingExistingImage && isImageStrategy {
		targetImage := strings.TrimSpace(service.Image)
		if targetImage == "" {
			targetImage = strings.TrimSpace(serviceDeploy.Image)
		}
		if targetImage == "" {
			reason := fmt.Sprintf("Service '%s' specifies image strategy or infrastructure classification but has no image configured", service.Name)
			emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return errors.New(reason)
		}

		emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Infrastructure / pre-built image '%s' detected for service '%s'. Pulling/verifying image...", targetImage, service.Name))
		runImage = targetImage
		if e.dockerClient != nil {
			reader, pullErr := e.dockerClient.ImagePull(execCtx, targetImage, dockerimage.PullOptions{})
			if pullErr == nil && reader != nil {
				_, _ = io.Copy(io.Discard, reader)
				_ = reader.Close()
				emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Image '%s' ready. Skipping source build.", targetImage))
			} else {
				// Image pull failed: check if image is available in local cache
				_, _, inspectErr := e.dockerClient.ImageInspectWithRaw(execCtx, targetImage)
				if inspectErr != nil {
					reason := fmt.Sprintf("Infrastructure image '%s' could not be pulled and is not available in local cache: %v", targetImage, pullErr)
					emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
					emitStatus(models.DeployStatusFailed, nil, &reason)
					return errors.New(reason)
				}
				emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Image '%s' found in local image cache. Skipping source build.", targetImage))
			}
		}
		reusingExistingImage = true
	}

	var sharedPromise *sharedBuildPromise
	var sharedKey string

	if !reusingExistingImage {
		srcRev := ""
		if serviceDeploy.SourceRevision != nil {
			srcRev = *serviceDeploy.SourceRevision
		}
		rawCtx := strings.TrimSpace(service.BuildContext)
		if rawCtx == "" {
			rawCtx = strings.TrimSpace(serviceDeploy.BuildContext)
		}
		cleanCtx := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rawCtx)))
		if cleanCtx == "" {
			cleanCtx = "."
		}
		rawDF := strings.TrimSpace(service.DockerfilePath)
		if rawDF == "" {
			rawDF = strings.TrimSpace(serviceDeploy.DockerfilePath)
		}
		cleanDF := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rawDF)))
		if cleanDF == "" || cleanDF == "." {
			cleanDF = "Dockerfile"
		}

		// Detect if this service can share a build artifact with another service
		isSharedCandidate := (service.BuildStrategy == models.BuildStrategyDockerfile || serviceDeploy.BuildStrategy == models.BuildStrategyDockerfile)
		if isSharedCandidate {
			buildArgsMap := make(map[string]*string)
			for k, v := range normalBuildArgs {
				val := v
				buildArgsMap[k] = &val
			}
			strat := service.BuildStrategy
			if strat == "" {
				strat = serviceDeploy.BuildStrategy
			}
			sharedKey = ComputeSharedBuildKey(
				project.ID.String(),
				srcRev,
				cleanCtx,
				cleanDF,
				strat,
				"",
				buildArgsMap,
				service.RuntimeType,
				service.InternalPort,
				service.StartCommand,
				service.PackageManager,
			)
			p, loaded := e.getOrInitSharedBuild(sharedKey)
			sharedPromise = p
			if loaded {
				emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Service '%s' shares build context and Dockerfile ('%s', '%s') with another service. Waiting for shared build artifact...", service.Name, cleanCtx, cleanDF))
				select {
				case <-sharedPromise.Done():
					tag, dig, sErr := sharedPromise.Result()
					if sErr != nil {
						reason := fmt.Sprintf("Shared image build failed: %v", sErr)
						emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
						emitStatus(models.DeployStatusFailed, nil, &reason)
						return errors.New(reason)
					}
					if tag != "" {
						runImage = tag
						if e.dockerClient != nil {
							_ = e.dockerClient.ImageTag(execCtx, tag, svcTag)
							runImage = svcTag
							if service.Image != "" {
								_ = e.dockerClient.ImageTag(execCtx, tag, service.Image)
							}
						}
						if dig != "" && e.deploymentService != nil {
							_ = e.deploymentService.UpdateServiceDeploymentImageDigest(ctx, serviceDeploy.ID, dig)
						}
						reusingExistingImage = true
						emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Reusing shared build artifact '%s' (build context: '%s', dockerfile: '%s'). Skipping Docker build.", tag, cleanCtx, cleanDF))
					}
				case <-execCtx.Done():
					reason := "Context cancelled while waiting for shared image build"
					emitStatus(models.DeployStatusFailed, nil, &reason)
					return errors.New(reason)
				}
			}
		}
	}

	if !reusingExistingImage {
		if sharedPromise != nil {
			defer func() {
				if !sharedPromise.IsDone() {
					failErr := execCtx.Err()
					if failErr == nil {
						failErr = errors.New("shared image build aborted or cancelled before completion")
					}
					sharedPromise.Fail(failErr)
					e.getSharedCoordinator().Remove(sharedKey)
					e.sharedBuildArtifacts.Delete(sharedKey)
				}
			}()
		}

		// 1. Source acquisition & resolution
		emitStatus(models.DeployStatusCloning, nil, nil)
		srcRev := ""
		if serviceDeploy.SourceRevision != nil {
			srcRev = *serviceDeploy.SourceRevision
		}
		buildSourceDir, cleanupDir, err := e.resolveSourceDirectory(execCtx, project, serviceDeploy.ID.String(), emitLog, srcRev)
		if cleanupDir != "" {
			defer func() {
				_ = os.RemoveAll(cleanupDir)
			}()
		}
		if err != nil {
			reason := fmt.Sprintf("Failed to resolve source: %v", err)
			emitLog(models.LogPhaseSource, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return errors.New(reason)
		}

		// 2. Building Docker Image
		emitStatus(models.DeployStatusBuilding, nil, nil)
		if err := e.verifyDockerDaemon(execCtx); err != nil {
			reason := fmt.Sprintf("Docker daemon check failed before build: %v. Please ensure Docker Desktop is running.", err)
			emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return errors.New(reason)
		}
		emitLog(models.LogPhaseBuild, models.LogStreamSystem, "Docker daemon available")
		if bv, ok := e.getCachedBuilderVersion(); ok && bv == types.BuilderBuildKit {
			emitLog(models.LogPhaseBuild, models.LogStreamSystem, "Builder version: 2 / BuildKit")
		} else if bv, ok := e.getCachedBuilderVersion(); ok && bv == types.BuilderV1 {
			emitLog(models.LogPhaseBuild, models.LogStreamSystem, "Builder version: 1 / legacy")
		} else if bv, ok := e.getCachedBuilderVersion(); ok && bv != "" {
			emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Builder version: %s", bv))
		} else {
			emitLog(models.LogPhaseBuild, models.LogStreamSystem, "Builder version: 1 / legacy")
		}
		relDockerPath := "Dockerfile"
		var tarArchive io.ReadCloser
		var dfContent []byte

		var releaseSlot func()
		var slotReleased bool
		var slotRelMu sync.Mutex
		doReleaseSlot := func() {
			slotRelMu.Lock()
			defer slotRelMu.Unlock()
			if !slotReleased && releaseSlot != nil {
				slotReleased = true
				releaseSlot()
			}
		}
		defer doReleaseSlot()

		if project.SourceType == models.SourceTypeLocalAgent {
			effPath := service.SourcePath
			if project.BuildContext != "" && project.BuildContext != "." && !strings.HasPrefix(service.SourcePath, project.BuildContext) {
				effPath = filepath.ToSlash(filepath.Clean(filepath.Join(filepath.FromSlash(project.BuildContext), filepath.FromSlash(service.SourcePath))))
			}
			cleanRelPath, pathErr := e.pathValidator.ValidateRelativeServicePath(effPath)
			if pathErr != nil {
				reason := fmt.Sprintf("Path isolation security violation: invalid service path: %v", pathErr)
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
				emitStatus(models.DeployStatusFailed, nil, &reason)
				return errors.New(reason)
			}

			baseURL := resolveAgentBaseURL()
			agentToken := ""
			expectedAgentID := ""
			if e.sourceService != nil {
				if srcUUID, err := uuid.Parse(project.SourceReference); err == nil {
					if src, sErr := e.sourceService.GetSource(execCtx, srcUUID, project.OwnerID); sErr == nil && src != nil {
						expectedAgentID = src.AgentID
					}
					tok, err := e.sourceService.GetDecryptedAgentToken(execCtx, project.OwnerID, srcUUID)
					if err != nil {
						reason := fmt.Sprintf("Local agent authorization failed: %v", err)
						emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
						emitStatus(models.DeployStatusFailed, nil, &reason)
						return errors.New(reason)
					}
					agentToken = tok
				}
			}
			if agentToken == "" {
				reason := "Local agent session credential missing or expired; please re-select project folder"
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
				emitStatus(models.DeployStatusFailed, nil, &reason)
				return errors.New(reason)
			}

			agentQuery := url.Values{}
			agentQuery.Set("service_path", cleanRelPath)
			if service.BuildStrategy == models.BuildStrategyDockerfile {
				dfPath := service.DockerfilePath
				if dfPath == "" {
					dfPath = "Dockerfile"
				}
				agentQuery.Set("dockerfile", dfPath)
			} else {
				agentQuery.Set("runtime", service.RuntimeType)
				agentQuery.Set("port", strconv.Itoa(service.InternalPort))
				if service.StartCommand != "" {
					agentQuery.Set("start_cmd", service.StartCommand)
				}
				pkgManager := service.PackageManager
				if pkgManager == "" && service.RuntimeType == "nodejs" {
					pkgManager = "npm"
				}
				if pkgManager != "" {
					agentQuery.Set("package_manager", pkgManager)
				}
			}

			verifiedBase, verifyErr := agent.ResolveAndVerifyAgentBaseURL(execCtx, baseURL, expectedAgentID)
			if verifyErr != nil {
				reason := fmt.Sprintf("Local agent identity verification failed: %v", verifyErr)
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
				emitStatus(models.DeployStatusFailed, nil, &reason)
				return errors.New(reason)
			}

			agentStreamClient := &http.Client{
				Transport: &http.Transport{
					DialContext: (&net.Dialer{
						Timeout:   2 * time.Second,
						KeepAlive: 30 * time.Second,
					}).DialContext,
					TLSHandshakeTimeout:   5 * time.Second,
					ResponseHeaderTimeout: 15 * time.Second,
				},
			}

			agentURL := fmt.Sprintf("%s/api/agent/sources/%s/stream-context?%s",
				verifiedBase,
				project.SourceReference,
				agentQuery.Encode(),
			)
			req, reqErr := http.NewRequestWithContext(execCtx, http.MethodGet, agentURL, nil)
			if reqErr != nil {
				reason := fmt.Sprintf("Failed to create agent stream request: %v", reqErr)
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
				emitStatus(models.DeployStatusFailed, nil, &reason)
				return errors.New(reason)
			}
			req.Header.Set("Authorization", "Bearer "+agentToken)
			req.Header.Set("X-Agent-Session-Token", agentToken)
			resp, httpErr := agentStreamClient.Do(req)

			if httpErr != nil || resp == nil || resp.StatusCode != http.StatusOK {
				reason := "Failed to stream source from local agent"
				if httpErr != nil {
					reason = httpErr.Error()
				} else if resp != nil {
					bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
					resp.Body.Close()

					var errData struct {
						Error  string `json:"error"`
						Reason string `json:"reason"`
					}
					_ = json.Unmarshal(bodyBytes, &errData)

					diagReason := errData.Reason
					if diagReason == "" {
						diagReason = errData.Error
					}
					if diagReason == "" && len(bodyBytes) > 0 {
						diagReason = strings.TrimSpace(string(bodyBytes))
					}

					slog.Warn("local agent stream context request rejected",
						"status", resp.StatusCode,
						"reason", diagReason,
						"source_id", project.SourceReference,
					)

					if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
						if diagReason != "" {
							reason = fmt.Sprintf("Local agent session authorization rejected (%d: %s). Please re-select the project folder.", resp.StatusCode, diagReason)
						} else {
							reason = fmt.Sprintf("Local agent session authorization rejected (%d). Please re-select the project folder.", resp.StatusCode)
						}
					} else if resp.StatusCode == http.StatusNotFound {
						if diagReason != "" {
							reason = fmt.Sprintf("Local agent source session not found (%s). Please re-select the project folder.", diagReason)
						} else {
							reason = "Local agent source session expired or agent restarted. Please re-select the project folder."
						}
					} else {
						if diagReason != "" {
							reason = fmt.Sprintf("agent returned status %d: %s", resp.StatusCode, diagReason)
						} else {
							reason = fmt.Sprintf("agent returned status %d", resp.StatusCode)
						}
					}
				}
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
				emitStatus(models.DeployStatusFailed, nil, &reason)
				return errors.New(reason)
			}
			tarArchive = resp.Body
			if service.BuildStrategy == models.BuildStrategyAuto {
				relDockerPath = "Dockerfile.forgelab"
			} else {
				if service.DockerfilePath != "" {
					relDockerPath = service.DockerfilePath
				} else {
					relDockerPath = "Dockerfile"
				}
			}
		} else {
			// For non-local-agent sources: local_directory, local_upload, github
			// Determine effective source root using user-selected root_dir (project.BuildContext) if configured
			effectiveSourceRoot := buildSourceDir
			if project.BuildContext != "" && project.BuildContext != "." {
				cleanProjRoot := filepath.Clean(filepath.FromSlash(project.BuildContext))
				if !strings.HasPrefix(cleanProjRoot, "..") && !filepath.IsAbs(cleanProjRoot) {
					subDir := filepath.Join(buildSourceDir, cleanProjRoot)
					if fi, err := os.Stat(subDir); err == nil && fi.IsDir() {
						effectiveSourceRoot = subDir
					}
				}
			}

			isAuto := service.BuildStrategy == models.BuildStrategyAuto
			svcContextDir, resolvedDockerPath, valErr := e.pathValidator.ValidateServiceBuildPaths(
				effectiveSourceRoot,
				service.SourcePath,
				service.BuildContext,
				service.DockerfilePath,
				isAuto,
			)
			if valErr != nil && effectiveSourceRoot != buildSourceDir {
				// Fallback to repository root if the build context was declared relative to repo root (e.g. Compose)
				svcContextDir, resolvedDockerPath, valErr = e.pathValidator.ValidateServiceBuildPaths(
					buildSourceDir,
					service.SourcePath,
					service.BuildContext,
					service.DockerfilePath,
					isAuto,
				)
			}
			if valErr != nil {
				reason := fmt.Sprintf("Path isolation security violation: %v", valErr)
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
				emitStatus(models.DeployStatusFailed, nil, &reason)
				return errors.New(reason)
			}
			relDockerPath = resolvedDockerPath

			var virtualFiles map[string][]byte
			if isAuto {
				intPort := service.InternalPort
				if intPort <= 0 {
					intPort = 8080
				}
				pkgManager := service.PackageManager
				if pkgManager == "" && svcContextDir != "" {
					if _, err := os.Stat(filepath.Join(svcContextDir, "pnpm-lock.yaml")); err == nil {
						pkgManager = "pnpm"
					} else if _, err := os.Stat(filepath.Join(svcContextDir, "yarn.lock")); err == nil {
						pkgManager = "yarn"
					} else if _, err := os.Stat(filepath.Join(svcContextDir, "bun.lockb")); err == nil {
						pkgManager = "bun"
					} else if _, err := os.Stat(filepath.Join(svcContextDir, "bun.lock")); err == nil {
						pkgManager = "bun"
					} else if _, err := os.Stat(filepath.Join(svcContextDir, "package-lock.json")); err == nil {
						pkgManager = "npm"
					}
				}
				generatedContent := detector.GenerateDockerfile(service.RuntimeType, intPort, service.StartCommand, pkgManager)
				virtualFiles = map[string][]byte{
					"Dockerfile.forgelab": []byte(generatedContent),
				}
			}

			// Pre-read Dockerfile content to resolve builder before streaming
			if isAuto {
				dfContent = virtualFiles["Dockerfile.forgelab"]
			} else if svcContextDir != "" && relDockerPath != "" {
				if b, err := os.ReadFile(filepath.Join(svcContextDir, relDockerPath)); err == nil {
					dfContent = b
				}
			}

			matcher, _ := LoadDockerignore(svcContextDir)
			if matcher == nil {
				matcher = NewDockerignoreMatcher(DefaultIgnorePatterns)
			}

			archFile, archErr := tararchive.CreateCompletedArchiveFile(execCtx, e.workDir, tararchive.BuildContextOptions{
				BuildContextDir: svcContextDir,
				Matcher:         matcher,
				VirtualFiles:    virtualFiles,
				EmitLog: func(phase, stream, msg string) {
					emitLog(phase, stream, msg)
				},
			}, relDockerPath)
			if archErr != nil {
				reason := fmt.Sprintf("Build context generation failed: %v", archErr)
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
				emitStatus(models.DeployStatusFailed, nil, &reason)
				return errors.New(reason)
			}
			defer func() {
				_ = archFile.Close()
				_ = os.Remove(archFile.Name())
			}()
			tarArchive = archFile
		}

		buildArgs := make(map[string]*string)
		for k, v := range normalBuildArgs {
			val := v
			buildArgs[k] = &val
		}
		if len(buildArgs) > 0 {
			emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Injected %d non-secret build arguments (build secrets isolated from BuildArgs/image history).", len(buildArgs)))
		}
		if len(secretBuildVars) > 0 {
			emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Isolated %d build secrets from Docker BuildArgs and image history.", len(secretBuildVars)))
		}

		buildLabels := map[string]string{
			"forgelab.managed":               "true",
			"forgelab.project_id":            project.ID.String(),
			"forgelab.service_id":            service.ID.String(),
			"forgelab.service_name":          service.Name,
			"forgelab.service_deployment_id": serviceDeploy.ID.String(),
		}
		if serviceDeploy.DeploymentID != nil {
			buildLabels["forgelab.deployment_id"] = serviceDeploy.DeploymentID.String()
		}

		buildOpts := types.ImageBuildOptions{
			Tags:       []string{svcTag},
			Dockerfile: relDockerPath,
			BuildArgs:  buildArgs,
			Remove:     true,
			Labels:     buildLabels,
		}

		// Pre-resolve builder capability before starting build
		if resolvedOpts, err := e.resolveBuilder(execCtx, dfContent, buildOpts, nil); err == nil {
			buildOpts = resolvedOpts
		}

		buildCtx := WithBuildServiceName(execCtx, service.Name)
		if err := e.buildImage(buildCtx, tarArchive, buildOpts, func(msg string) {
			emitLog(models.LogPhaseBuild, models.LogStreamStdout, msg)
		}); err != nil {
			if sharedPromise != nil {
				sharedPromise.Fail(err)
				e.getSharedCoordinator().Remove(sharedKey)
				e.sharedBuildArtifacts.Delete(sharedKey)
			}
			reason := err.Error()
			cat := ClassifyDockerBuildError(err)
			diag := e.formatDockerDiagnostic(ctx, err, cat)
			emitLog(models.LogPhaseBuild, models.LogStreamStderr, diag)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return errors.New(reason)
		}
		emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Docker image '%s' built successfully.", svcTag))

		// Inspect image to record immutable digest for rollback / reproducibility
		var digest string
		if e.dockerClient != nil {
			if inspect, _, err := e.dockerClient.ImageInspectWithRaw(ctx, svcTag); err == nil {
				if len(inspect.RepoDigests) > 0 {
					digest = inspect.RepoDigests[0]
				} else if inspect.ID != "" {
					digest = inspect.ID
				}
				if digest != "" && e.deploymentService != nil {
					_ = e.deploymentService.UpdateServiceDeploymentImageDigest(ctx, serviceDeploy.ID, digest)
				}
			}
		}
		if sharedPromise != nil {
			sharedPromise.Complete(svcTag, digest)
		}
		if service.Image != "" && e.dockerClient != nil {
			_ = e.dockerClient.ImageTag(ctx, svcTag, service.Image)
		}
		runImage = svcTag
		doReleaseSlot() // Release Docker build slot immediately after image build completes
	}

	// 3. Container Startup
	emitStatus(models.DeployStatusStarting, nil, nil)
	emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Creating container for service '%s' using image '%s'...", service.Name, runImage))

	intPort := service.InternalPort
	if intPort <= 0 {
		intPort = 8080
	}

	var hostPort *int
	var portBindings nat.PortMap
	targetPortStr := fmt.Sprintf("%d/tcp", intPort)

	allProjectServices, _ = e.serviceService.ListServices(ctx, project.ID)
	// Internal infrastructure, workers, and non-public services do not allocate host ports when part of multi-service project
	isInternalService := service.Classification == models.ClassificationInfrastructure ||
		service.Classification == models.ClassificationWorker ||
		service.Classification == models.ClassificationJob ||
		serviceDeploy.Classification == models.ClassificationInfrastructure ||
		serviceDeploy.Classification == models.ClassificationWorker ||
		serviceDeploy.Classification == models.ClassificationJob ||
		discovery.ClassifyService(service.Name, service.Image, nil, "") == discovery.ClassificationInfrastructure ||
		discovery.ClassifyService(service.Name, service.Image, nil, "") == discovery.ClassificationWorker ||
		discovery.ClassifyService(service.Name, service.Image, nil, "") == discovery.ClassificationJob
	shouldExposePort := !isInternalService && (service.PublicExposed || service.Role == models.RoleFrontend || len(allProjectServices) <= 1)

	if shouldExposePort {
		allocated, err := e.portManager.AllocatePort()
		if err != nil {
			reason := fmt.Sprintf("Failed to allocate host port for exposed service '%s': %v", service.Name, err)
			emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return errors.New(reason)
		}
		hostPort = &allocated
		portBindings = nat.PortMap{
			nat.Port(targetPortStr): []nat.PortBinding{
				{
					HostIP:   "0.0.0.0",
					HostPort: fmt.Sprintf("%d", allocated),
				},
			},
		}
	}

	// Auto-heal missing environment variables for local agent projects:
	if len(runtimeEnvMap) == 0 && e.projectService != nil && project.SourceType == models.SourceTypeLocalAgent {
		if importedCount, err := e.projectService.ImportProjectEnvironment(execCtx, project, project.OwnerID); err == nil && importedCount > 0 {
			if e.secretService != nil {
				if reloaded, _, err := e.secretService.GetDecryptedEnvMap(execCtx, project.ID, &service.ID, models.EnvScopeRuntime); err == nil && len(reloaded) > 0 {
					runtimeEnvMap = reloaded
				}
			}
		}
	}

	// Supply necessary container initialization variables for infrastructure services if missing
	svcNameLower := strings.ToLower(service.Name)
	svcImageLower := strings.ToLower(service.Image)
	isCurrentPostgres := strings.Contains(svcNameLower, "postgres") || strings.Contains(svcImageLower, "postgres")
	if isCurrentPostgres {
		if runtimeEnvMap == nil {
			runtimeEnvMap = make(map[string]string)
		}
		if _, hasPass := runtimeEnvMap["POSTGRES_PASSWORD"]; !hasPass {
			if _, hasAuth := runtimeEnvMap["POSTGRES_HOST_AUTH_METHOD"]; !hasAuth {
				runtimeEnvMap["POSTGRES_PASSWORD"] = "forgelab_dev_password"
			}
		}
		if _, hasUser := runtimeEnvMap["POSTGRES_USER"]; !hasUser {
			runtimeEnvMap["POSTGRES_USER"] = "forgelab"
		}
		if _, hasDB := runtimeEnvMap["POSTGRES_DB"]; !hasDB {
			runtimeEnvMap["POSTGRES_DB"] = "forgelab"
		}
	}

	isCurrentMySQL := strings.Contains(svcNameLower, "mysql") || strings.Contains(svcNameLower, "mariadb") || strings.Contains(svcImageLower, "mysql") || strings.Contains(svcImageLower, "mariadb")
	if isCurrentMySQL {
		if runtimeEnvMap == nil {
			runtimeEnvMap = make(map[string]string)
		}
		if _, hasPass := runtimeEnvMap["MYSQL_ROOT_PASSWORD"]; !hasPass {
			if _, hasEmpty := runtimeEnvMap["MYSQL_ALLOW_EMPTY_PASSWORD"]; !hasEmpty {
				runtimeEnvMap["MYSQL_ROOT_PASSWORD"] = "root"
			}
		}
		if _, hasDB := runtimeEnvMap["MYSQL_DATABASE"]; !hasDB {
			runtimeEnvMap["MYSQL_DATABASE"] = "forgelab"
		}
	}

	svcEnv := make([]string, 0, len(runtimeEnvMap)+len(allProjectServices)*4+4)
	for k, v := range runtimeEnvMap {
		trimmedV := strings.TrimSpace(v)
		if (k == "DATABASE_URL" || k == "REDIS_URL" || strings.HasSuffix(k, "_URL")) && (trimmedV == "[REDACTED]" || !strings.Contains(trimmedV, "://")) {
			continue
		}
		svcEnv = append(svcEnv, fmt.Sprintf("%s=%s", k, v))
	}
	svcEnv = append(svcEnv, fmt.Sprintf("PORT=%d", intPort))
	svcEnv = append(svcEnv, fmt.Sprintf("SERVICE_NAME=%s", service.Name))

	var hasExplicitDatabaseURL bool
	var hasExplicitRedisURL bool
	if v, ok := runtimeEnvMap["DATABASE_URL"]; ok && strings.TrimSpace(v) != "" && strings.TrimSpace(v) != "[REDACTED]" && strings.Contains(v, "://") {
		hasExplicitDatabaseURL = true
	}
	if v, ok := runtimeEnvMap["REDIS_URL"]; ok && strings.TrimSpace(v) != "" && strings.TrimSpace(v) != "[REDACTED]" && strings.Contains(v, "://") {
		hasExplicitRedisURL = true
	}

	interSvcEnv := e.resolveInterServiceConnectionEnv(execCtx, project, service, allProjectServices, runtimeEnvMap, redactor, hasExplicitDatabaseURL, hasExplicitRedisURL)
	svcEnv = append(svcEnv, interSvcEnv...)

	// Parse deployment plan to obtain declared network configurations and aliases
	var plan discovery.DeploymentPlan
	hasPlan := false
	if len(project.DeploymentPlan) > 0 && string(project.DeploymentPlan) != "{}" {
		if err := json.Unmarshal(project.DeploymentPlan, &plan); err == nil {
			hasPlan = true
		}
	}

	netConfigMap := make(map[string]discovery.NetworkConfig)
	if hasPlan {
		for _, nc := range plan.NetworkConfigs {
			netConfigMap[nc.Name] = nc
		}
	}

	planAliasesMap := make(map[string]map[string][]string)
	if hasPlan {
		for _, ps := range plan.Services {
			if len(ps.NetworkAliases) > 0 {
				planAliasesMap[ps.Name] = ps.NetworkAliases
			}
		}
	}

	// Preserve service-level network membership and create isolated project networks
	var serviceNetworks []string
	if len(serviceDeploy.Networks) > 0 {
		serviceNetworks = serviceDeploy.Networks
	} else if len(service.Networks) > 0 {
		serviceNetworks = service.Networks
	}

	var primaryNetworkName string
	var secondaryNetworkNames []string
	var primaryNetworkComposeKey string
	secondaryNetworkComposeKeys := make(map[string]string)

	if len(serviceNetworks) > 0 {
		for i, net := range serviceNetworks {
			nc, isDeclared := netConfigMap[net]
			var dockerNetName string
			opts := NetworkEnsureOptions{
				Driver:   "bridge",
				Internal: false,
				External: false,
				Labels: map[string]string{
					"forgelab.project_id": project.ID.String(),
					"forgelab.network":    net,
				},
			}
			if isDeclared {
				if nc.Driver != "" {
					opts.Driver = nc.Driver
				}
				opts.Internal = nc.Internal
				opts.External = nc.External
				if nc.External {
					dockerNetName = net
				} else {
					dockerNetName = fmt.Sprintf("forgelab-net-%s-%s", project.ID.String()[:8], net)
				}
			} else {
				dockerNetName = fmt.Sprintf("forgelab-net-%s-%s", project.ID.String()[:8], net)
			}

			if err := e.EnsureNetworkWithOptions(execCtx, dockerNetName, opts); err != nil {
				reason := fmt.Sprintf("Failed to initialize service network '%s': %v", dockerNetName, err)
				emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
				emitStatus(models.DeployStatusFailed, nil, &reason)
				return errors.New(reason)
			}
			if i == 0 {
				primaryNetworkName = dockerNetName
				primaryNetworkComposeKey = net
			} else {
				secondaryNetworkNames = append(secondaryNetworkNames, dockerNetName)
				secondaryNetworkComposeKeys[dockerNetName] = net
			}
		}
	} else {
		// When no explicit service network definitions exist, use the isolated project network
		commonProjectNet := fmt.Sprintf("forgelab-net-%s", project.ID.String())
		if err := e.EnsureNetworkWithOptions(execCtx, commonProjectNet, NetworkEnsureOptions{
			Driver: "bridge",
			Labels: map[string]string{
				"forgelab.project_id": project.ID.String(),
			},
		}); err != nil {
			reason := fmt.Sprintf("Failed to initialize common project network '%s': %v", commonProjectNet, err)
			emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return errors.New(reason)
		}
		primaryNetworkName = commonProjectNet
	}

	containerName := fmt.Sprintf("forgelab-app-%s-%s", serviceDeploy.ID.String()[:8], service.Name)
	containerConfig := &container.Config{
		Image: runImage,
		Env:   svcEnv,
		ExposedPorts: nat.PortSet{
			nat.Port(targetPortStr): struct{}{},
		},
		Labels: map[string]string{
			"forgelab.project_id":            project.ID.String(),
			"forgelab.service_id":            service.ID.String(),
			"forgelab.service_deployment_id": serviceDeploy.ID.String(),
			"forgelab.service_name":          service.Name,
		},
	}
	if serviceDeploy.DeploymentID != nil {
		containerConfig.Labels["forgelab.deployment_id"] = serviceDeploy.DeploymentID.String()
	}

	var cmdParts []string
	startCmd := strings.TrimSpace(serviceDeploy.StartCommand)
	if startCmd == "" {
		startCmd = strings.TrimSpace(service.StartCommand)
	}

	// For Dockerfile builds, ignore synthetic auto-detected commands (like "/app/server") so the Dockerfile's native CMD / ENTRYPOINT executes
	isDockerfileBuild := (service.BuildStrategy == models.BuildStrategyDockerfile ||
		serviceDeploy.BuildStrategy == models.BuildStrategyDockerfile ||
		service.DockerfilePath != "" ||
		serviceDeploy.DockerfilePath != "")
	if isDockerfileBuild && isSyntheticAutoStartCommand(startCmd) {
		startCmd = ""
	}

	if startCmd != "" {
		cmdParts = parseCommandToArgs(startCmd)
	}
	if len(cmdParts) > 0 {
		containerConfig.Cmd = cmdParts
	}

	// Attach Compose healthcheck if configured
	hcConfig := serviceDeploy.HealthCheckConfig
	if hcConfig == nil {
		hcConfig = service.HealthCheckConfig
	}
	if hcConfig != nil && len(hcConfig.Test) > 0 {
		if hcConfig.Strategy == models.HealthStrategyNone ||
			(len(hcConfig.Test) == 1 && strings.EqualFold(hcConfig.Test[0], "none")) {
			containerConfig.Healthcheck = &container.HealthConfig{
				Test: []string{"NONE"},
			}
		} else {
			containerConfig.Healthcheck = &container.HealthConfig{
				Test: hcConfig.Test,
			}
			if hcConfig.IntervalSeconds > 0 {
				containerConfig.Healthcheck.Interval = time.Duration(hcConfig.IntervalSeconds) * time.Second
			}
			if hcConfig.TimeoutSeconds > 0 {
				containerConfig.Healthcheck.Timeout = time.Duration(hcConfig.TimeoutSeconds) * time.Second
			}
			if hcConfig.Retries > 0 {
				containerConfig.Healthcheck.Retries = hcConfig.Retries
			}
			if hcConfig.StartPeriodSeconds > 0 {
				containerConfig.Healthcheck.StartPeriod = time.Duration(hcConfig.StartPeriodSeconds) * time.Second
			}
		}
	}

	// Prepare volume mounts: preserve named volume vs bind mount semantics with strict security boundary validation
	var binds []string
	var allowedMountPrefixes []string

	// Populate allowed mount prefixes strictly from administrator-configured roots
	if e.pathValidator != nil {
		for _, r := range e.pathValidator.AllowedRoots() {
			if r != "" {
				allowedMountPrefixes = append(allowedMountPrefixes, r)
			}
		}
		if e.pathValidator.HostSourceRoot() != "" {
			allowedMountPrefixes = append(allowedMountPrefixes, e.pathValidator.HostSourceRoot())
		}
		if e.pathValidator.DaemonSourceRoot() != "" {
			allowedMountPrefixes = append(allowedMountPrefixes, e.pathValidator.DaemonSourceRoot())
		}
		if e.pathValidator.ContainerSourceRoot() != "" {
			allowedMountPrefixes = append(allowedMountPrefixes, e.pathValidator.ContainerSourceRoot())
		}
	}

	// Resolve project source paths for allowlisting across namespaces
	if paths, err := ResolveProjectSourcePaths(execCtx, project, e.sourceService, e.pathValidator, e.dockerClient); err == nil {
		if paths.DaemonPath != "" {
			allowedMountPrefixes = append(allowedMountPrefixes, paths.DaemonPath)
		}
		if paths.BackendPath != "" && paths.BackendPath != paths.DaemonPath {
			allowedMountPrefixes = append(allowedMountPrefixes, paths.BackendPath)
		}
		if paths.AgentPath != "" && paths.AgentPath != paths.DaemonPath && paths.AgentPath != paths.BackendPath {
			allowedMountPrefixes = append(allowedMountPrefixes, paths.AgentPath)
		}
	} else if hostRoot, containerRoot, err := ResolveProjectSourceRoot(execCtx, project, e.sourceService, e.pathValidator); err == nil {
		if hostRoot != "" {
			allowedMountPrefixes = append(allowedMountPrefixes, hostRoot)
		}
		if containerRoot != "" && containerRoot != hostRoot {
			allowedMountPrefixes = append(allowedMountPrefixes, containerRoot)
		}
	}
	for _, v := range serviceDeploy.Volumes {
		if strings.TrimSpace(v.Target) == "" {
			continue
		}

		resolvedVol, err := ResolveAndValidateVolumeMount(execCtx, project, v, e.sourceService, e.pathValidator, e.dockerClient)
		if err != nil {
			reason := err.Error()
			emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return err
		}

		if resolvedVol.IsNamedVolume {
			volName := fmt.Sprintf("forgelab-vol-%s-%s", project.ID.String()[:8], resolvedVol.VolumeName)
			if e.dockerClient != nil {
				_, _ = e.dockerClient.VolumeCreate(execCtx, dockervolume.CreateOptions{
					Name: volName,
					Labels: map[string]string{
						"forgelab.project_id":  project.ID.String(),
						"forgelab.volume_name": resolvedVol.VolumeName,
					},
				})
			}
			bindEntry := fmt.Sprintf("%s:%s", volName, resolvedVol.ContainerPath)
			if resolvedVol.ReadOnly {
				bindEntry += ":ro"
			}
			binds = append(binds, bindEntry)
		} else if resolvedVol.IsDockerSocket {
			reason := "Security violation: mounting host Docker socket into deployed containers is strictly forbidden"
			emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return ErrDockerSocketMountRejected
		} else {
			daemonPath := resolvedVol.DaemonPath
			if daemonPath == "" {
				daemonPath = resolvedVol.HostPath
			}
			bindEntry := fmt.Sprintf("%s:%s", daemonPath, resolvedVol.ContainerPath)
			if resolvedVol.ReadOnly {
				bindEntry += ":ro"
			}
			binds = append(binds, bindEntry)
		}
	}

	// Use snapshotted resource configuration from the service deployment, with fallback to service config
	targetCpuMillicores := serviceDeploy.CpuMillicores
	if targetCpuMillicores <= 0 && service.CpuMillicores > 0 {
		targetCpuMillicores = service.CpuMillicores
	}
	targetMemoryMB := serviceDeploy.MemoryMB
	if targetMemoryMB <= 0 && service.MemoryMB > 0 {
		targetMemoryMB = service.MemoryMB
	}
	targetPidsLimit := serviceDeploy.PidsLimit
	if targetPidsLimit <= 0 && service.PidsLimit > 0 {
		targetPidsLimit = service.PidsLimit
	}
	targetEphemeralStorageMB := 0
	if serviceDeploy.EphemeralStorageMB != nil && *serviceDeploy.EphemeralStorageMB > 0 {
		targetEphemeralStorageMB = *serviceDeploy.EphemeralStorageMB
	} else if service.EphemeralStorageMB != nil && *service.EphemeralStorageMB > 0 {
		targetEphemeralStorageMB = *service.EphemeralStorageMB
	}

	isJob := (service.Classification == models.ClassificationJob || serviceDeploy.Classification == models.ClassificationJob)
	hostConfig, err := ValidateAndBuildSecureHostConfig(ContainerSecurityOptions{
		PortBindings:             portBindings,
		TargetCpuMillicores:      targetCpuMillicores,
		TargetMemoryMB:           targetMemoryMB,
		TargetPidsLimit:          targetPidsLimit,
		TargetEphemeralStorageMB: targetEphemeralStorageMB,
		Binds:                    binds,
		NetworkMode:              "",
		AllowedMountPrefixes:     allowedMountPrefixes,
		AllowDockerSocket:        false, // NEVER authorize Docker socket for tenant project configurations
		IsJob:                    isJob,
	})
	if err != nil {
		if hostPort != nil {
			e.portManager.ReleasePort(*hostPort)
		}
		reason := fmt.Sprintf("Failed to validate container security configuration for '%s': %v", service.Name, err)
		emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
		emitStatus(models.DeployStatusFailed, nil, &reason)
		return errors.New(reason)
	}

	if primaryNetworkName != "" {
		hostConfig.NetworkMode = container.NetworkMode(primaryNetworkName)
	}

	var primaryAliases []string
	primaryAliases = append(primaryAliases, service.Name)
	if primaryNetworkComposeKey != "" {
		if svcAliases, ok := planAliasesMap[service.Name]; ok {
			for _, a := range svcAliases[primaryNetworkComposeKey] {
				if a != "" && a != service.Name {
					primaryAliases = append(primaryAliases, a)
				}
			}
		}
	}

	netConfig := &dockernetwork.NetworkingConfig{
		EndpointsConfig: map[string]*dockernetwork.EndpointSettings{
			primaryNetworkName: {
				Aliases: primaryAliases,
			},
		},
	}

	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = e.dockerClient.ContainerRemove(cleanupCtx, containerName, container.RemoveOptions{Force: true})
	cleanupCancel()

	hasWritableVolume := false
	for _, v := range service.Volumes {
		if !v.ReadOnly {
			hasWritableVolume = true
			break
		}
	}
	isStateful := hasWritableVolume || len(binds) > 0 || service.Classification == models.ClassificationInfrastructure

	var stoppedPreviousForStateful bool
	if isStateful && hasPreviousHealthy && previousContainerID != nil && *previousContainerID != "" {
		emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Stopping previous container %s for stateful service '%s' to release volume locks...", (*previousContainerID)[:12], service.Name))
		if previousDeploymentID != nil {
			e.StopLogCollector(*previousDeploymentID)
		}
		stopTimeoutSec := 15
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = e.dockerClient.ContainerStop(stopCtx, *previousContainerID, container.StopOptions{Timeout: &stopTimeoutSec})
		stopCancel()
		stoppedPreviousForStateful = true
	}

	var containerID string
	maxPortRetries := 3
	for attempt := 0; attempt <= maxPortRetries; attempt++ {
		resp, err := e.dockerClient.ContainerCreate(execCtx, containerConfig, hostConfig, netConfig, nil, containerName)
		if err != nil {
			if isPortConflictError(err) && hostPort != nil && attempt < maxPortRetries {
				emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Host port %d conflict during container create: %v. Retrying with a new port (attempt %d/%d)...", *hostPort, err, attempt+1, maxPortRetries))
				e.portManager.RegisterUsedPort(*hostPort)
				newPort, allocErr := e.portManager.AllocatePort()
				if allocErr == nil {
					hostPort = &newPort
					portBindings[nat.Port(targetPortStr)] = []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d", newPort)}}
					hostConfig.PortBindings = portBindings
					continue
				}
			}
			if hostPort != nil {
				e.portManager.ReleasePort(*hostPort)
			}
			if stoppedPreviousForStateful && previousContainerID != nil && *previousContainerID != "" {
				restartCtx, restartCancel := context.WithTimeout(context.Background(), 15*time.Second)
				_ = e.dockerClient.ContainerStart(restartCtx, *previousContainerID, container.StartOptions{})
				restartCancel()
			}
			reason := fmt.Sprintf("Failed to create container for service '%s': %v", service.Name, err)
			emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return errors.New(reason)
		}

		containerID = resp.ID

		// Connect to any secondary isolated project networks
		var secNetErr error
		for _, secNet := range secondaryNetworkNames {
			if e.dockerClient != nil {
				secComposeKey := secondaryNetworkComposeKeys[secNet]
				var secAliases []string
				secAliases = append(secAliases, service.Name)
				if secComposeKey != "" {
					if svcAliases, ok := planAliasesMap[service.Name]; ok {
						for _, a := range svcAliases[secComposeKey] {
							if a != "" && a != service.Name {
								secAliases = append(secAliases, a)
							}
						}
					}
				}

				if netConnErr := e.dockerClient.NetworkConnect(execCtx, secNet, resp.ID, &dockernetwork.EndpointSettings{
					Aliases: secAliases,
				}); netConnErr != nil {
					secNetErr = fmt.Errorf("failed to attach required network '%s' to service '%s': %w", secNet, service.Name, netConnErr)
					break
				}
			}
		}

		if secNetErr != nil {
			if hostPort != nil {
				e.portManager.ReleasePort(*hostPort)
			}
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = e.dockerClient.ContainerRemove(cleanupCtx, resp.ID, container.RemoveOptions{Force: true})
			cleanupCancel()
			if stoppedPreviousForStateful && previousContainerID != nil && *previousContainerID != "" {
				restartCtx, restartCancel := context.WithTimeout(context.Background(), 15*time.Second)
				_ = e.dockerClient.ContainerStart(restartCtx, *previousContainerID, container.StartOptions{})
				restartCancel()
			}
			reason := secNetErr.Error()
			emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return errors.New(reason)
		}

		if err := e.dockerClient.ContainerStart(execCtx, containerID, container.StartOptions{}); err != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = e.dockerClient.ContainerRemove(cleanupCtx, containerID, container.RemoveOptions{Force: true})
			cleanupCancel()

			if isPortConflictError(err) && hostPort != nil && attempt < maxPortRetries {
				emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Host port %d conflict during container start: %v. Retrying with a new port (attempt %d/%d)...", *hostPort, err, attempt+1, maxPortRetries))
				e.portManager.RegisterUsedPort(*hostPort)
				newPort, allocErr := e.portManager.AllocatePort()
				if allocErr == nil {
					hostPort = &newPort
					portBindings[nat.Port(targetPortStr)] = []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d", newPort)}}
					hostConfig.PortBindings = portBindings
					continue
				}
			}

			if hostPort != nil {
				e.portManager.ReleasePort(*hostPort)
			}
			if stoppedPreviousForStateful && previousContainerID != nil && *previousContainerID != "" {
				restartCtx, restartCancel := context.WithTimeout(context.Background(), 15*time.Second)
				_ = e.dockerClient.ContainerStart(restartCtx, *previousContainerID, container.StartOptions{})
				restartCancel()
			}
			reason := fmt.Sprintf("Failed to start container for service '%s': %v", service.Name, err)
			emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return errors.New(reason)
		}

		break
	}

	e.trackActiveContainer(containerID)
	defer e.untrackActiveContainer(containerID)
	_ = e.deploymentService.UpdateServiceDeploymentContainer(execCtx, serviceDeploy.ID, containerID, hostPort)

	emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Container %s started for service '%s'.", containerID[:12], service.Name))

	// Start runtime log collector — continuously reads container stdout/stderr
	e.StartRuntimeLogCollector(RuntimeCollectorParams{
		ContainerID:         containerID,
		ProjectID:           project.ID,
		DeploymentID:        serviceDeploy.DeploymentID,
		ServiceDeploymentID: &serviceDeploy.ID,
		ServiceID:           &service.ID,
		ServiceName:         service.Name,
		Redactor:            redactor,
	})

	if isJob {
		emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Service '%s' is a one-shot job (classification: %s). Waiting for container execution to complete...", service.Name, service.Classification))
		if e.dockerClient != nil {
			statusCh, errCh := e.dockerClient.ContainerWait(execCtx, containerID, container.WaitConditionNotRunning)
			select {
			case err := <-errCh:
				if err != nil {
					e.StopLogCollector(serviceDeploy.ID)
					e.StopContainerLogCollector(containerID)
					reason := fmt.Sprintf("Failed waiting for job container '%s': %v", service.Name, err)
					emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
					emitStatus(models.DeployStatusFailed, nil, &reason)
					return errors.New(reason)
				}
			case status := <-statusCh:
				if status.Error != nil && status.Error.Message != "" {
					e.StopLogCollector(serviceDeploy.ID)
					e.StopContainerLogCollector(containerID)
					reason := fmt.Sprintf("Job container '%s' failed: %s (exit code %d)", service.Name, status.Error.Message, status.StatusCode)
					emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
					emitStatus(models.DeployStatusFailed, nil, &reason)
					return errors.New(reason)
				}
				if status.StatusCode != 0 {
					e.StopLogCollector(serviceDeploy.ID)
					e.StopContainerLogCollector(containerID)
					reason := fmt.Sprintf("Job container '%s' exited with non-zero status code %d", service.Name, status.StatusCode)
					emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
					emitStatus(models.DeployStatusFailed, nil, &reason)
					return errors.New(reason)
				}
				emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Job container '%s' completed successfully with exit code 0.", service.Name))
			case <-execCtx.Done():
				e.StopLogCollector(serviceDeploy.ID)
				e.StopContainerLogCollector(containerID)
				reason := fmt.Sprintf("Job execution timed out or was cancelled for service '%s'", service.Name)
				emitStatus(models.DeployStatusFailed, nil, &reason)
				return errors.New(reason)
			}
		}
		e.StopLogCollector(serviceDeploy.ID)
		e.StopContainerLogCollector(containerID)
		err = e.serviceService.PromoteServiceDeployment(ctx, service.ID, serviceDeploy.ID, containerID, runImage, nil)
		if err != nil {
			reason := fmt.Sprintf("Failed to promote job service deployment '%s': %v", service.Name, err)
			emitLog(models.LogPhaseHealth, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return fmt.Errorf("failed to promote job service deployment: %w", err)
		}
		_ = e.deploymentService.UpdateServiceDeploymentContainer(ctx, serviceDeploy.ID, containerID, nil)
		_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, serviceDeploy.ID, models.DeployStatusStopped, nil)
		_ = e.serviceService.UpdateServiceStatus(ctx, service.ID, models.ServiceStatusStopped, &containerID, &runImage, nil)
		emitStatus(models.DeployStatusStopped, nil, nil)
		emitLog(models.LogPhaseRuntime, models.LogStreamSystem, fmt.Sprintf("Job service '%s' deployment #%d completed successfully!", service.Name, serviceDeploy.DeployNumber))
		return nil
	}

	// 4. Health Checking
	emitStatus(models.DeployStatusHealthChecking, hostPort, nil)
	emitLog(models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("Verifying health for service '%s'...", service.Name))

	healthStrat := service.HealthStrategy
	if healthStrat == "" {
		healthStrat = serviceDeploy.HealthStrategy
	}
	if hcConfig != nil && hcConfig.Strategy != "" {
		healthStrat = hcConfig.Strategy
	} else if healthStrat == "" {
		healthStrat = models.HealthStrategyAuto
	}
	healthPath := "/"
	if service.HealthCheckPath != nil && *service.HealthCheckPath != "" {
		healthPath = *service.HealthCheckPath
	}

	svcHealthy, healthReason := e.verifyServiceHealth(
		execCtx,
		containerID,
		intPort,
		hostPort,
		healthStrat,
		healthPath,
		func(msg string) { emitLog(models.LogPhaseHealth, models.LogStreamSystem, msg) },
		func(msg string) { emitLog(models.LogPhaseHealth, models.LogStreamStderr, msg) },
		hcConfig,
	)

	if !svcHealthy {
		// Health check failed - ENFORCE SAFETY INVARIANT:
		// Capture and log original health failure reason BEFORE stopping container / cleanup!
		detailedReason := fmt.Sprintf("Health verification failed for service '%s': %s", service.Name, healthReason)
		emitLog(models.LogPhaseHealth, models.LogStreamStderr, detailedReason)

		// Destroy newly spawned broken container and preserve previously healthy container intact!
		e.StopLogCollector(serviceDeploy.ID)
		e.StopContainerLogCollector(containerID)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = e.dockerClient.ContainerStop(cleanupCtx, containerID, container.StopOptions{})
		_ = e.dockerClient.ContainerRemove(cleanupCtx, containerID, container.RemoveOptions{Force: true})
		cleanupCancel()
		if hostPort != nil {
			e.portManager.ReleasePort(*hostPort)
		}

		if hasPreviousHealthy {
			if stoppedPreviousForStateful && previousContainerID != nil && *previousContainerID != "" {
				restartCtx, restartCancel := context.WithTimeout(context.Background(), 15*time.Second)
				_ = e.dockerClient.ContainerStart(restartCtx, *previousContainerID, container.StartOptions{})
				restartCancel()
			}
			emitLog(models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("SAFETY INVARIANT ENFORCED: New deployment for '%s' failed health check. Previous healthy container remains active!", service.Name))
		}
		emitStatus(models.DeployStatusFailed, nil, &detailedReason)
		return errors.New(detailedReason)
	}

	// 5. Promote service
	err = e.serviceService.PromoteServiceDeployment(execCtx, service.ID, serviceDeploy.ID, containerID, svcTag, hostPort)
	if err != nil {
		// Promotion failed - ENFORCE SAFETY INVARIANT:
		// The new container must not replace the previous current deployment,
		// the new deployment must become failed, and the old container must remain intact.
		e.StopLogCollector(serviceDeploy.ID)
		e.StopContainerLogCollector(containerID)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = e.dockerClient.ContainerStop(cleanupCtx, containerID, container.StopOptions{})
		_ = e.dockerClient.ContainerRemove(cleanupCtx, containerID, container.RemoveOptions{Force: true})
		cleanupCancel()
		if hostPort != nil {
			e.portManager.ReleasePort(*hostPort)
		}

		reason := fmt.Sprintf("Failed to promote service deployment '%s': %v", service.Name, err)
		emitLog(models.LogPhaseHealth, models.LogStreamStderr, reason)
		if hasPreviousHealthy {
			if stoppedPreviousForStateful && previousContainerID != nil && *previousContainerID != "" {
				restartCtx, restartCancel := context.WithTimeout(context.Background(), 15*time.Second)
				_ = e.dockerClient.ContainerStart(restartCtx, *previousContainerID, container.StartOptions{})
				restartCancel()
			}
			emitLog(models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("SAFETY INVARIANT ENFORCED: Promotion failed for '%s'. Previous healthy container and deployment remain active!", service.Name))
		}
		emitStatus(models.DeployStatusFailed, nil, &reason)
		return fmt.Errorf("failed to promote service deployment: %w", err)
	}

	_ = e.deploymentService.UpdateServiceDeploymentContainer(execCtx, serviceDeploy.ID, containerID, hostPort)
	_ = e.deploymentService.UpdateServiceDeploymentStatus(execCtx, serviceDeploy.ID, models.DeployStatusRunning, nil)
	emitStatus(models.DeployStatusRunning, hostPort, nil)

	// 6. Cleanup previous container for this service if different
	if previousContainerID != nil && *previousContainerID != "" && *previousContainerID != containerID {
		if serviceDeploy.DeploymentID == nil {
			// Single service deployment: safe to clean up immediately upon promotion
			if !stoppedPreviousForStateful {
				emitLog(models.LogPhaseRuntime, models.LogStreamSystem, fmt.Sprintf("Stopping previous container %s for service '%s'...", (*previousContainerID)[:12], service.Name))
				if previousDeploymentID != nil {
					e.StopLogCollector(*previousDeploymentID)
				}
				e.StopContainerLogCollector(*previousContainerID)
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
				_ = e.dockerClient.ContainerStop(cleanupCtx, *previousContainerID, container.StopOptions{})
				cleanupCancel()
			} else {
				e.StopContainerLogCollector(*previousContainerID)
			}
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = e.dockerClient.ContainerRemove(cleanupCtx, *previousContainerID, container.RemoveOptions{Force: true})
			cleanupCancel()
		} else {
			// Release deployment: preserve previous healthy container until full release promotion verification!
			emitLog(models.LogPhaseRuntime, models.LogStreamSystem, fmt.Sprintf("Preserving previous container %s for service '%s' until full release promotion verification.", (*previousContainerID)[:12], service.Name))
		}
	}

	// Update project port if this service is public or frontend
	if hostPort != nil && (service.Role == models.RoleFrontend || service.PublicExposed) {
		_, _ = e.projectService.UpdateProjectPort(execCtx, project.ID, *hostPort)
	}

	emitLog(models.LogPhaseRuntime, models.LogStreamSystem, fmt.Sprintf("Service '%s' deployment #%d is now RUNNING and live!", service.Name, serviceDeploy.DeployNumber))
	return nil
}

// ExecuteDeployment runs the project-wide release deployment orchestration layer.
func (e *Engine) ExecuteDeployment(ctx context.Context, deploymentID uuid.UUID) error {
	deployment, err := e.deploymentService.GetDeployment(ctx, deploymentID)
	if err != nil {
		return fmt.Errorf("failed to get deployment %s: %w", deploymentID, err)
	}

	// Stale / superseded deployment check: if already terminal, do not run!
	if models.IsDeploymentTerminalStatus(deployment.Status) {
		slog.Info("deployment already in terminal state; skipping execution", "deployment_id", deploymentID, "status", deployment.Status)
		return nil
	}
	if e.deploymentService != nil {
		if superseded, err := e.deploymentService.IsDeploymentSuperseded(ctx, deployment.ProjectID, deployment.DeployNumber); err == nil && superseded {
			slog.Info("deployment superseded by newer release; skipping execution", "deployment_id", deploymentID, "deploy_number", deployment.DeployNumber)
			return nil
		}
	}

	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()

	project, err := e.getProjectByID(execCtx, deployment.ProjectID)
	if err != nil {
		return fmt.Errorf("failed to get project %s: %w", deployment.ProjectID, err)
	}

	if deployment.ProjectID != project.ID {
		return fmt.Errorf("deployment %s does not belong to project %s", deployment.ID, project.ID)
	}

	e.registerActiveExec(deploymentID, project.ID, execCancel)
	defer e.unregisterActiveExec(deploymentID)

	if project.Status == models.ProjectStatusDeleting {
		return errors.New("deployment aborted: project deletion is in progress")
	}

	redactor := logging.NewLogRedactor()
	emitLog := func(phase, stream, message string) {
		redactedMsg := redactor.Redact(message)
		persistedLog, err := e.deploymentService.AddDeploymentServiceLog(ctx, &deployment.ID, nil, nil, phase, stream, redactedMsg)
		if err != nil {
			slog.Error("failed to persist release deployment log", "deployment_id", deployment.ID, "phase", phase, "error", err)
			return
		}
		if e.wsHub != nil {
			data := map[string]interface{}{
				"id":            persistedLog.ID,
				"sequence":      persistedLog.ID,
				"deployment_id": deployment.ID.String(),
				"timestamp":     persistedLog.Timestamp.Format(time.RFC3339Nano),
				"phase":         persistedLog.Phase,
				"stream":        persistedLog.Stream,
				"message":       persistedLog.Message,
			}
			channel := "deployment:" + deployment.ID.String()
			_ = e.wsHub.PublishEvent(channel, &ws.EventMessage{
				Type:     "log",
				Channel:  channel,
				Sequence: persistedLog.ID,
				Data:     data,
			})
			projectChannel := fmt.Sprintf("project:%s", project.ID.String())
			_ = e.wsHub.PublishEvent(projectChannel, &ws.EventMessage{
				Type:     "release_log",
				Channel:  projectChannel,
				Sequence: persistedLog.ID,
				Data:     data,
			})
		}
	}

	updateReleaseStatus := func(newStatus string, failureReason *string) {
		if err := e.deploymentService.UpdateDeploymentStatus(ctx, deployment.ID, newStatus, failureReason); err != nil {
			slog.Warn("failed to update release deployment status in db", "deployment_id", deployment.ID, "status", newStatus, "error", err)
			return
		}
		if e.wsHub != nil {
			channel := "deployment:" + deployment.ID.String()
			_ = e.wsHub.PublishEvent(channel, &ws.EventMessage{
				Type:    "status_change",
				Channel: channel,
				Data: map[string]interface{}{
					"deployment_id":  deployment.ID.String(),
					"project_id":     project.ID.String(),
					"new_status":     newStatus,
					"failure_reason": failureReason,
					"timestamp":      time.Now().Format(time.RFC3339),
				},
			})
		}
	}

	var allProjectServices []*models.Service
	if e.serviceService != nil {
		allProjectServices, _ = e.serviceService.ListServices(ctx, project.ID)
	}

	// Legacy single-container project fallback if 0 services defined
	if len(allProjectServices) == 0 {
		return e.executeLegacySingleContainerDeployment(ctx, deployment, project)
	}

	// Multi-service project release deployment
	updateReleaseStatus(models.DeployStatusBuilding, nil)
	emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Initiating Release #%d for %d service(s)...", deployment.DeployNumber, len(allProjectServices)))

	svcDeploys, err := e.deploymentService.ListServiceDeployments(ctx, deployment.ID)
	if err != nil || len(svcDeploys) == 0 {
		reason := "No service deployments configured for this release"
		updateReleaseStatus(models.DeployStatusFailed, &reason)
		emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
		return errors.New(reason)
	}

	// Sanitize tenant volumes: strip forbidden host Docker socket mounts before preflight validation
	for _, sd := range svcDeploys {
		safeVols, stripped := security.SanitizeServiceVolumes(sd.Volumes)
		if stripped {
			sd.Volumes = safeVols
			emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("[security] Notice: stripped host Docker socket mount from service '%s' (tenant containers are sandboxed and cannot access host Docker engine)", sd.ServiceName))
			if e.deploymentService != nil {
				_ = e.deploymentService.UpdateServiceDeploymentVolumes(execCtx, sd.ID, safeVols)
			}
		}
	}

	servicesMap := make(map[string]*models.Service, len(allProjectServices))
	for _, s := range allProjectServices {
		if sVols, sStripped := security.SanitizeServiceVolumes(s.Volumes); sStripped {
			s.Volumes = sVols
			if e.serviceService != nil {
				_ = e.serviceService.UpdateServiceVolumes(execCtx, s.ID, sVols)
			}
		}
		servicesMap[s.Name] = s
	}

	// Preflight validation for the release: fails fast before any builds or container creation
	if err := ValidateDeploymentPreflight(execCtx, PreflightOptions{
		Project:       project,
		Deployments:   svcDeploys,
		ServicesMap:   servicesMap,
		PathValidator: e.pathValidator,
		SourceService: e.sourceService,
		DockerClient:  e.dockerClient,
	}); err != nil {
		reason := fmt.Sprintf("Preflight validation failed: %v", err)
		updateReleaseStatus(models.DeployStatusFailed, &reason)
		emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
		return errors.New(reason)
	}

	// Proactively initialize common project mesh network and any declared service networks before executing DAG tiers
	var plan discovery.DeploymentPlan
	hasPlan := false
	if len(project.DeploymentPlan) > 0 && string(project.DeploymentPlan) != "{}" {
		if err := json.Unmarshal(project.DeploymentPlan, &plan); err == nil {
			hasPlan = true
		}
	}
	netConfigMap := make(map[string]discovery.NetworkConfig)
	if hasPlan {
		for _, nc := range plan.NetworkConfigs {
			netConfigMap[nc.Name] = nc
		}
	}

	commonProjectNet := fmt.Sprintf("forgelab-net-%s", project.ID.String())
	if err := e.EnsureNetworkWithOptions(execCtx, commonProjectNet, NetworkEnsureOptions{
		Driver: "bridge",
		Labels: map[string]string{
			"forgelab.project_id": project.ID.String(),
		},
	}); err != nil {
		reason := fmt.Sprintf("Failed to initialize project network mesh '%s': %v", commonProjectNet, err)
		updateReleaseStatus(models.DeployStatusFailed, &reason)
		emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
		return errors.New(reason)
	}

	for _, sd := range svcDeploys {
		var sNets []string
		if len(sd.Networks) > 0 {
			sNets = sd.Networks
		} else if svc, ok := servicesMap[sd.ServiceName]; ok && len(svc.Networks) > 0 {
			sNets = svc.Networks
		}
		for _, net := range sNets {
			nc, isDeclared := netConfigMap[net]
			var dockerNetName string
			opts := NetworkEnsureOptions{
				Driver:   "bridge",
				Internal: false,
				External: false,
				Labels: map[string]string{
					"forgelab.project_id": project.ID.String(),
					"forgelab.network":    net,
				},
			}
			if isDeclared {
				if nc.Driver != "" {
					opts.Driver = nc.Driver
				}
				opts.Internal = nc.Internal
				opts.External = nc.External
				if nc.External {
					dockerNetName = net
				} else {
					dockerNetName = fmt.Sprintf("forgelab-net-%s-%s", project.ID.String()[:8], net)
				}
			} else {
				dockerNetName = fmt.Sprintf("forgelab-net-%s-%s", project.ID.String()[:8], net)
			}

			if err := e.EnsureNetworkWithOptions(execCtx, dockerNetName, opts); err != nil {
				reason := fmt.Sprintf("Failed to initialize service network '%s': %v", dockerNetName, err)
				updateReleaseStatus(models.DeployStatusFailed, &reason)
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
				return errors.New(reason)
			}
		}
	}

	var (
		mu         sync.Mutex
		allHealthy = true
		anyHealthy = false
		errList    []string
	)

	maxConcurrent := 4
	sem := make(chan struct{}, maxConcurrent)

	tiers, err := buildServiceDeploymentTiers(svcDeploys)
	if err != nil {
		reason := fmt.Sprintf("Failed to compute validated DAG execution tiers: %v", err)
		emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
		updateReleaseStatus(models.DeployStatusFailed, &reason)
		return errors.New(reason)
	}
	emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("DAG execution plan computed: %d deployment tier(s).", len(tiers)))

	failedServices := make(map[string]bool)
	serviceExitCodes := make(map[string]int)
	serviceHealthy := make(map[string]bool)

	for tierIdx, tier := range tiers {
		if execCtx.Err() != nil {
			mu.Lock()
			allHealthy = false
			errList = append(errList, fmt.Sprintf("Deployment cancelled: %v", execCtx.Err()))
			mu.Unlock()
			break
		}
		emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Executing Tier %d (%d service(s))...", tierIdx+1, len(tier)))
		var tierWg sync.WaitGroup
		for _, sd := range tier {
			// If service deployment is already terminal failed or skipped from a prior attempt, mark as failed
			if sd.Status == models.DeployStatusFailed || sd.Status == models.DeployStatusSkipped || sd.Status == models.DeployStatusCrashed {
				// If this service deployment was prematurely marked interrupted by backend restart reconciliation,
				// recover it so it can execute in this active release run
				if sd.Status == models.DeployStatusFailed &&
					sd.FailureReason != nil &&
					strings.Contains(*sd.FailureReason, "interrupted: backend restarted") {
					slog.Info("recovering tier service deployment prematurely marked interrupted by backend restart", "service_deployment_id", sd.ID, "service", sd.ServiceName)
					_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, sd.ID, models.DeployStatusQueued, nil)
					sd.Status = models.DeployStatusQueued
					sd.FailureReason = nil
				} else {
					mu.Lock()
					allHealthy = false
					failedServices[sd.ServiceName] = true
					serviceExitCodes[sd.ServiceName] = 1
					serviceHealthy[sd.ServiceName] = false
					errList = append(errList, fmt.Sprintf("%s: service deployment is already in terminal state '%s'", sd.ServiceName, sd.Status))
					mu.Unlock()
					continue
				}
			}

			// If already running from a prior healthy deployment in this release, preserve healthy state
			if sd.Status == models.DeployStatusRunning {
				mu.Lock()
				anyHealthy = true
				serviceExitCodes[sd.ServiceName] = 0
				serviceHealthy[sd.ServiceName] = true
				mu.Unlock()
				continue
			}

			// Check if any dependencies failed or did not satisfy required conditions
			hasFailedDep := false
			var failedDepName string
			var failedCondition string
			for _, dep := range sd.DependsOn {
				reqCondition := ""
				if sd.DependsOnConditions != nil {
					reqCondition = sd.DependsOnConditions[dep]
				}
				if reqCondition == "" {
					reqCondition = "service_started"
				}

				if failedServices[dep] {
					hasFailedDep = true
					failedDepName = dep
					failedCondition = reqCondition
					break
				}

				code, exists := serviceExitCodes[dep]
				if !exists {
					hasFailedDep = true
					failedDepName = dep
					failedCondition = reqCondition
					break
				}

				if reqCondition == "service_completed_successfully" {
					if code != 0 {
						hasFailedDep = true
						failedDepName = dep
						failedCondition = reqCondition
						break
					}
				}

				if reqCondition == "service_healthy" {
					if !serviceHealthy[dep] {
						hasFailedDep = true
						failedDepName = dep
						failedCondition = reqCondition
						break
					}
				}
			}
			if hasFailedDep {
				mu.Lock()
				allHealthy = false
				failedServices[sd.ServiceName] = true
				serviceExitCodes[sd.ServiceName] = 1
				serviceHealthy[sd.ServiceName] = false
				errList = append(errList, fmt.Sprintf("%s: skipped because dependency '%s' failed condition '%s'", sd.ServiceName, failedDepName, failedCondition))
				mu.Unlock()
				reason := fmt.Sprintf("Skipped because dependency '%s' failed condition '%s'", failedDepName, failedCondition)
				_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, sd.ID, models.DeployStatusSkipped, &reason)
				continue
			}

			tierWg.Add(1)
			go func(sdItem *models.ServiceDeployment) {
				defer tierWg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if execErr := e.ExecuteServiceDeployment(execCtx, sdItem.ID); execErr != nil {
					mu.Lock()
					allHealthy = false
					failedServices[sdItem.ServiceName] = true
					serviceExitCodes[sdItem.ServiceName] = 1
					serviceHealthy[sdItem.ServiceName] = false
					errList = append(errList, fmt.Sprintf("%s: %v", sdItem.ServiceName, execErr))
					mu.Unlock()
				} else {
					mu.Lock()
					anyHealthy = true
					serviceExitCodes[sdItem.ServiceName] = 0
					serviceHealthy[sdItem.ServiceName] = true
					mu.Unlock()
				}
			}(sd)
		}
		tierWg.Wait()
	}

	newProjectStatus, _ := e.recalculateAndUpdateProjectStatus(ctx, project.ID)

	if allHealthy && anyHealthy {
		// Clean up preserved previous containers across services now that entire release is healthy
		for _, sd := range svcDeploys {
			if svc, ok := servicesMap[sd.ServiceName]; ok && svc.ContainerID != nil && *svc.ContainerID != "" && sd.ContainerID != nil && *svc.ContainerID != *sd.ContainerID {
				prevCid := *svc.ContainerID
				e.StopContainerLogCollector(prevCid)
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
				_ = e.dockerClient.ContainerStop(cleanupCtx, prevCid, container.StopOptions{})
				_ = e.dockerClient.ContainerRemove(cleanupCtx, prevCid, container.RemoveOptions{Force: true})
				cleanupCancel()
			}
		}
		_ = e.deploymentService.SetCurrentDeployment(ctx, project.ID, deployment.ID, newProjectStatus)
		updateReleaseStatus(models.DeployStatusRunning, nil)
		emitLog(models.LogPhaseRuntime, models.LogStreamSystem, fmt.Sprintf("Release #%d successfully deployed all services!", deployment.DeployNumber))
		return nil
	} else if anyHealthy {
		// Valid state transition: deployment status is Running (not partially_running!), while project status reflects partially_running
		_ = e.deploymentService.SetCurrentDeployment(ctx, project.ID, deployment.ID, newProjectStatus)
		updateReleaseStatus(models.DeployStatusRunning, nil)
		emitLog(models.LogPhaseRuntime, models.LogStreamSystem, fmt.Sprintf("Release #%d finished with partial success (%s). Only individual healthy services were updated; full release rollback is not claimed.", deployment.DeployNumber, strings.Join(errList, "; ")))
		return nil
	} else {
		reason := fmt.Sprintf("All services failed deployment: %s", strings.Join(errList, "; "))
		updateReleaseStatus(models.DeployStatusFailed, &reason)
		emitLog(models.LogPhaseHealth, models.LogStreamStderr, reason)
		return errors.New(reason)
	}
}

// executeLegacySingleContainerDeployment provides fallback execution for legacy projects with 0 services.
func (e *Engine) executeLegacySingleContainerDeployment(ctx context.Context, deployment *models.Deployment, project *models.Project) error {
	redactor := logging.NewLogRedactor()
	emitLog := func(phase, stream, message string) {
		redactedMsg := redactor.Redact(message)
		persistedLog, err := e.deploymentService.AddDeploymentServiceLog(ctx, &deployment.ID, nil, nil, phase, stream, redactedMsg)
		if err != nil {
			return
		}
		if e.wsHub != nil {
			data := map[string]interface{}{
				"id":            persistedLog.ID,
				"sequence":      persistedLog.ID,
				"deployment_id": deployment.ID.String(),
				"timestamp":     persistedLog.Timestamp.Format(time.RFC3339Nano),
				"phase":         persistedLog.Phase,
				"stream":        persistedLog.Stream,
				"message":       persistedLog.Message,
			}
			channel := "deployment:" + deployment.ID.String()
			_ = e.wsHub.PublishEvent(channel, &ws.EventMessage{
				Type:     "log",
				Channel:  channel,
				Sequence: persistedLog.ID,
				Data:     data,
			})
			projectChannel := fmt.Sprintf("project:%s", project.ID.String())
			_ = e.wsHub.PublishEvent(projectChannel, &ws.EventMessage{
				Type:     "release_log",
				Channel:  projectChannel,
				Sequence: persistedLog.ID,
				Data:     data,
			})
		}
	}

	updateStatus := func(newStatus string, failureReason *string) {
		prevStatus := deployment.Status
		if err := e.deploymentService.UpdateDeploymentStatus(ctx, deployment.ID, newStatus, failureReason); err != nil {
			slog.Warn("failed to update deployment status in db", "deployment_id", deployment.ID, "status", newStatus, "error", err)
			return
		}
		deployment.Status = newStatus
		if e.wsHub != nil {
			channel := "deployment:" + deployment.ID.String()
			_ = e.wsHub.PublishEvent(channel, &ws.EventMessage{
				Type:    "status_change",
				Channel: channel,
				Data: map[string]interface{}{
					"deployment_id":   deployment.ID.String(),
					"project_id":      project.ID.String(),
					"previous_status": prevStatus,
					"new_status":      newStatus,
					"timestamp":       time.Now().Format(time.RFC3339),
				},
			})
		}
	}

	var normalBuildArgs map[string]string
	var secretBuildVars map[string]string

	if e.secretService != nil {
		normalArgs, secretVars, allSecrets, err := e.secretService.GetBuildVariables(ctx, project.ID, nil, deployment.EnvSnapshot)
		if err == nil {
			normalBuildArgs = normalArgs
			secretBuildVars = secretVars
			redactor.SetSecrets(allSecrets)
		} else {
			_, secrets, err := e.secretService.GetDecryptedEnvMap(ctx, project.ID, nil, "")
			if err == nil {
				redactor.SetSecrets(secrets)
			}
		}
	}

	runImage := ""
	if deployment.ImageTag != nil {
		runImage = *deployment.ImageTag
	}
	reusingExistingImage := false

	// Check if configured to reuse an existing immutable image (e.g. rollback)
	if deployment.ExecutionMode == models.ExecutionModeReuseImage || (deployment.ImageDigest != nil && *deployment.ImageDigest != "") {
		candidateImage := ""
		if deployment.ImageDigest != nil && *deployment.ImageDigest != "" {
			candidateImage = *deployment.ImageDigest
		} else if deployment.ImageTag != nil && *deployment.ImageTag != "" {
			candidateImage = *deployment.ImageTag
		}

		if candidateImage != "" && e.dockerClient != nil {
			if inspect, _, err := e.dockerClient.ImageInspectWithRaw(ctx, candidateImage); err == nil {
				reusingExistingImage = true
				runImage = candidateImage
				idShort := inspect.ID
				if len(idShort) > 12 {
					idShort = idShort[:12]
				}
				emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Execution path: reusing existing immutable image '%s' (image ID: %s). Skipping source cloning and image build.", candidateImage, idShort))
				if deployment.ImageTag != nil && *deployment.ImageTag != "" && candidateImage != *deployment.ImageTag {
					_ = e.dockerClient.ImageTag(ctx, candidateImage, *deployment.ImageTag)
				}
			} else {
				if deployment.ExecutionMode == models.ExecutionModeReuseImage {
					reason := fmt.Sprintf("Rollback failed closed: immutable image '%s' is unavailable in Docker daemon (%v); will not rebuild from current source", candidateImage, err)
					emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
					updateStatus(models.DeployStatusFailed, &reason)
					return errors.New(reason)
				}
				emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Stored immutable image '%s' not cached in Docker daemon (%v). Falling back to source build pipeline.", candidateImage, err))
			}
		} else if deployment.ExecutionMode == models.ExecutionModeReuseImage {
			reason := "Rollback failed closed: no immutable image digest or tag is available for reuse"
			emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
			updateStatus(models.DeployStatusFailed, &reason)
			return errors.New(reason)
		}
	}

	if !reusingExistingImage {
		updateStatus(models.DeployStatusCloning, nil)
		var srcRev string
		if deployment.SourceRevision != nil {
			srcRev = *deployment.SourceRevision
		}
		buildSourceDir, cleanupDir, err := e.resolveSourceDirectory(ctx, project, deployment.ID.String(), emitLog, srcRev)
		if cleanupDir != "" {
			defer func() {
				_ = os.RemoveAll(cleanupDir)
			}()
		}
		if err != nil {
			reason := fmt.Sprintf("Source resolution failed: %v", err)
			emitLog(models.LogPhaseSource, models.LogStreamStderr, reason)
			updateStatus(models.DeployStatusFailed, &reason)
			return errors.New(reason)
		}

		updateStatus(models.DeployStatusBuilding, nil)
		if err := e.verifyDockerDaemon(ctx); err != nil {
			reason := fmt.Sprintf("Docker daemon check failed before build: %v. Please ensure Docker Desktop is running.", err)
			emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
			updateStatus(models.DeployStatusFailed, &reason)
			return errors.New(reason)
		}
		emitLog(models.LogPhaseBuild, models.LogStreamSystem, "Docker daemon available")
		if bv, ok := e.getCachedBuilderVersion(); ok && bv == types.BuilderBuildKit {
			emitLog(models.LogPhaseBuild, models.LogStreamSystem, "Builder version: 2 / BuildKit")
		} else if bv, ok := e.getCachedBuilderVersion(); ok && bv == types.BuilderV1 {
			emitLog(models.LogPhaseBuild, models.LogStreamSystem, "Builder version: 1 / legacy")
		} else if bv, ok := e.getCachedBuilderVersion(); ok && bv != "" {
			emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Builder version: %s", bv))
		} else {
			emitLog(models.LogPhaseBuild, models.LogStreamSystem, "Builder version: 1 / legacy")
		}
		emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Preparing build for image '%s'...", *deployment.ImageTag))

		buildStrategy := deployment.BuildStrategy
		if buildStrategy == "" {
			buildStrategy = project.BuildStrategy
		}
		if buildStrategy == "" {
			buildStrategy = models.BuildStrategyAuto
		}
		isAuto := buildStrategy == models.BuildStrategyAuto

		var relDockerPath string
		var tarStream io.ReadCloser
		var dfContent []byte

		var releaseSlot func()
		var slotReleased bool
		var slotRelMu sync.Mutex
		doReleaseSlot := func() {
			slotRelMu.Lock()
			defer slotRelMu.Unlock()
			if !slotReleased && releaseSlot != nil {
				slotReleased = true
				releaseSlot()
			}
		}
		defer doReleaseSlot()

		if project.SourceType == models.SourceTypeLocalAgent {
			cleanRelPath, pathErr := e.pathValidator.ValidateRelativeServicePath(project.BuildContext)
			if pathErr != nil {
				reason := fmt.Sprintf("Path isolation security violation: invalid build context: %v", pathErr)
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
				updateStatus(models.DeployStatusFailed, &reason)
				return errors.New(reason)
			}
			baseURL := resolveAgentBaseURL()
			agentToken := ""
			expectedAgentID := ""
			if e.sourceService != nil {
				if srcUUID, err := uuid.Parse(project.SourceReference); err == nil {
					if src, sErr := e.sourceService.GetSource(ctx, project.OwnerID, srcUUID); sErr == nil && src != nil {
						expectedAgentID = src.AgentID
					}
					tok, _ := e.sourceService.GetDecryptedAgentToken(ctx, project.OwnerID, srcUUID)
					agentToken = tok
				}
			}
			if agentToken == "" {
				reason := "Local agent session credential missing or expired; please re-select project folder"
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
				updateStatus(models.DeployStatusFailed, &reason)
				return errors.New(reason)
			}
			var targetSvc *models.Service
			if len(project.Services) > 0 && project.Services[0] != nil {
				targetSvc = project.Services[0]
			}
			pkgManager := ""
			if targetSvc != nil {
				pkgManager = targetSvc.PackageManager
			}
			if pkgManager == "" && deployment.RuntimeType == "nodejs" {
				pkgManager = "npm"
			}

			verifiedBase, verifyErr := agent.ResolveAndVerifyAgentBaseURL(ctx, baseURL, expectedAgentID)
			if verifyErr != nil {
				reason := fmt.Sprintf("Local agent identity verification failed: %v", verifyErr)
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
				updateStatus(models.DeployStatusFailed, &reason)
				return errors.New(reason)
			}

			agentStreamClient := &http.Client{
				Transport: &http.Transport{
					DialContext: (&net.Dialer{
						Timeout:   2 * time.Second,
						KeepAlive: 30 * time.Second,
					}).DialContext,
					TLSHandshakeTimeout:   5 * time.Second,
					ResponseHeaderTimeout: 15 * time.Second,
				},
			}

			agentURL := fmt.Sprintf("%s/api/agent/sources/%s/stream-context?service_path=%s&runtime=%s&port=%d&start_cmd=%s&package_manager=%s",
				verifiedBase,
				project.SourceReference,
				url.QueryEscape(cleanRelPath),
				url.QueryEscape(deployment.RuntimeType),
				deployment.InternalPort,
				url.QueryEscape(deployment.StartCommand),
				url.QueryEscape(pkgManager),
			)
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, agentURL, nil)
			if reqErr != nil {
				reason := fmt.Sprintf("Failed to create agent stream request: %v", reqErr)
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
				updateStatus(models.DeployStatusFailed, &reason)
				return errors.New(reason)
			}
			req.Header.Set("Authorization", "Bearer "+agentToken)
			req.Header.Set("X-Agent-Session-Token", agentToken)
			resp, httpErr := agentStreamClient.Do(req)

			if httpErr != nil || resp == nil || resp.StatusCode != http.StatusOK {
				reason := "Failed to stream source from local agent"
				if httpErr != nil {
					reason = httpErr.Error()
				} else if resp != nil {
					reason = fmt.Sprintf("Agent returned HTTP %d", resp.StatusCode)
				}
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
				updateStatus(models.DeployStatusFailed, &reason)
				return errors.New(reason)
			}
			tarStream = resp.Body
			relDockerPath = "Dockerfile.forgelab"
		} else {
			buildContextDir, resolvedDockerPath, valErr := e.pathValidator.ValidateServiceBuildPaths(
				buildSourceDir,
				project.BuildContext,
				project.BuildContext,
				project.DockerfilePath,
				isAuto,
			)
			if valErr != nil {
				reason := fmt.Sprintf("Path isolation security violation: %v", valErr)
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
				updateStatus(models.DeployStatusFailed, &reason)
				return errors.New(reason)
			}

			dockerfilePath := project.DockerfilePath
			if dockerfilePath == "" {
				dockerfilePath = "Dockerfile"
			}

			actualDockerPath := filepath.Join(buildContextDir, dockerfilePath)
			hasExistingDockerfile := false
			if _, err := os.Stat(actualDockerPath); err == nil {
				hasExistingDockerfile = true
			}

			relDockerPath = resolvedDockerPath
			var virtualFiles map[string][]byte

			if buildStrategy == models.BuildStrategyDockerfile || (buildStrategy == models.BuildStrategyAuto && hasExistingDockerfile) {
				if !hasExistingDockerfile {
					reason := fmt.Sprintf("Dockerfile '%s' not found in build context", dockerfilePath)
					emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
					updateStatus(models.DeployStatusFailed, &reason)
					return errors.New(reason)
				}
				emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Using Dockerfile at '%s'", dockerfilePath))
			} else {
				port := deployment.InternalPort
				if port == 0 {
					port = project.InternalPort
				}
				if port == 0 {
					port = 8080
				}
				startCmd := deployment.StartCommand
				if startCmd == "" {
					startCmd = project.StartCommand
				}
				pkgManager := ""
				if buildContextDir != "" {
					if _, err := os.Stat(filepath.Join(buildContextDir, "pnpm-lock.yaml")); err == nil {
						pkgManager = "pnpm"
					} else if _, err := os.Stat(filepath.Join(buildContextDir, "yarn.lock")); err == nil {
						pkgManager = "yarn"
					} else if _, err := os.Stat(filepath.Join(buildContextDir, "bun.lockb")); err == nil {
						pkgManager = "bun"
					} else if _, err := os.Stat(filepath.Join(buildContextDir, "bun.lock")); err == nil {
						pkgManager = "bun"
					} else if _, err := os.Stat(filepath.Join(buildContextDir, "package-lock.json")); err == nil {
						pkgManager = "npm"
					}
				}
				dockerfileContent := detector.GenerateDockerfile(deployment.RuntimeType, port, startCmd, pkgManager)
				relDockerPath = "Dockerfile.forgelab"
				virtualFiles = map[string][]byte{
					"Dockerfile.forgelab": []byte(dockerfileContent),
				}
			}

			if len(virtualFiles) > 0 {
				dfContent = virtualFiles["Dockerfile.forgelab"]
			} else if buildContextDir != "" && relDockerPath != "" {
				if b, err := os.ReadFile(filepath.Join(buildContextDir, relDockerPath)); err == nil {
					dfContent = b
				}
			}

			matcher, _ := LoadDockerignore(buildContextDir)
			if matcher == nil {
				matcher = NewDockerignoreMatcher(DefaultIgnorePatterns)
			}

			archFile, archErr := tararchive.CreateCompletedArchiveFile(ctx, e.workDir, tararchive.BuildContextOptions{
				BuildContextDir: buildContextDir,
				Matcher:         matcher,
				VirtualFiles:    virtualFiles,
				EmitLog: func(phase, stream, msg string) {
					emitLog(phase, stream, msg)
				},
			}, relDockerPath)
			if archErr != nil {
				reason := fmt.Sprintf("Build context generation failed: %v", archErr)
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
				updateStatus(models.DeployStatusFailed, &reason)
				return errors.New(reason)
			}
			defer func() {
				_ = archFile.Close()
				_ = os.Remove(archFile.Name())
			}()
			tarStream = archFile
		}

		buildArgs := make(map[string]*string)
		for k, v := range normalBuildArgs {
			val := v
			buildArgs[k] = &val
		}
		if len(buildArgs) > 0 {
			emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Injected %d non-secret build arguments (build secrets isolated from BuildArgs/image history).", len(buildArgs)))
		}
		if len(secretBuildVars) > 0 {
			emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Isolated %d build secrets from Docker BuildArgs and image history.", len(secretBuildVars)))
		}

		buildOpts := types.ImageBuildOptions{
			Tags:       []string{*deployment.ImageTag},
			Dockerfile: relDockerPath,
			BuildArgs:  buildArgs,
			Remove:     true,
			Labels: map[string]string{
				"forgelab.managed":       "true",
				"forgelab.project_id":    project.ID.String(),
				"forgelab.deployment_id": deployment.ID.String(),
			},
		}

		if resolvedOpts, err := e.resolveBuilder(ctx, dfContent, buildOpts, nil); err == nil {
			buildOpts = resolvedOpts
		}

		if err := e.buildImage(ctx, tarStream, buildOpts, func(msg string) {
			emitLog(models.LogPhaseBuild, models.LogStreamStdout, msg)
		}); err != nil {
			reason := err.Error()
			cat := ClassifyDockerBuildError(err)
			if cat == CategoryStorageDaemon || cat == CategoryDaemonUnreachable || cat == CategoryBuildKitRequired {
				diag := e.formatDockerDiagnostic(ctx, err, cat)
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, diag)
			} else {
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
			}
			updateStatus(models.DeployStatusFailed, &reason)
			return errors.New(reason)
		}

		if inspect, _, err := e.dockerClient.ImageInspectWithRaw(ctx, *deployment.ImageTag); err == nil {
			var digest string
			if len(inspect.RepoDigests) > 0 {
				digest = inspect.RepoDigests[0]
			} else if inspect.ID != "" {
				digest = inspect.ID
			}
			if digest != "" && e.deploymentService != nil {
				_ = e.deploymentService.UpdateDeploymentImageDigest(ctx, deployment.ID, digest)
			}
		}
		runImage = *deployment.ImageTag
		doReleaseSlot() // Release Docker build slot immediately after image build completes
	}

	updateStatus(models.DeployStatusStarting, nil)
	allocatedPort, err := e.portManager.AllocatePort()
	if err != nil {
		reason := fmt.Sprintf("Failed to allocate port: %v", err)
		emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
		updateStatus(models.DeployStatusFailed, &reason)
		return errors.New(reason)
	}

	intPort := deployment.InternalPort
	if intPort == 0 {
		intPort = project.InternalPort
	}
	if intPort == 0 {
		intPort = 8080
	}
	targetPortStr := fmt.Sprintf("%d/tcp", intPort)

	var envMap map[string]string
	if e.secretService != nil {
		envMap, _, _ = e.secretService.GetDecryptedEnvMap(ctx, project.ID, nil, models.EnvScopeRuntime)
	}

	if len(envMap) == 0 && e.projectService != nil && project.SourceType == models.SourceTypeLocalAgent {
		if importedCount, err := e.projectService.ImportProjectEnvironment(ctx, project, project.OwnerID); err == nil && importedCount > 0 {
			if e.secretService != nil {
				if reloaded, _, err := e.secretService.GetDecryptedEnvMap(ctx, project.ID, nil, models.EnvScopeRuntime); err == nil && len(reloaded) > 0 {
					envMap = reloaded
				}
			}
		}
	}

	projNameLower := strings.ToLower(project.Name)
	if strings.Contains(projNameLower, "postgres") {
		if envMap == nil {
			envMap = make(map[string]string)
		}
		if _, hasPass := envMap["POSTGRES_PASSWORD"]; !hasPass {
			if _, hasAuth := envMap["POSTGRES_HOST_AUTH_METHOD"]; !hasAuth {
				envMap["POSTGRES_PASSWORD"] = "forgelab_dev_password"
			}
		}
		if _, hasUser := envMap["POSTGRES_USER"]; !hasUser {
			envMap["POSTGRES_USER"] = "forgelab"
		}
		if _, hasDB := envMap["POSTGRES_DB"]; !hasDB {
			envMap["POSTGRES_DB"] = "forgelab"
		}
	}
	if strings.Contains(projNameLower, "mysql") || strings.Contains(projNameLower, "mariadb") {
		if envMap == nil {
			envMap = make(map[string]string)
		}
		if _, hasPass := envMap["MYSQL_ROOT_PASSWORD"]; !hasPass {
			if _, hasEmpty := envMap["MYSQL_ALLOW_EMPTY_PASSWORD"]; !hasEmpty {
				envMap["MYSQL_ROOT_PASSWORD"] = "root"
			}
		}
		if _, hasDB := envMap["MYSQL_DATABASE"]; !hasDB {
			envMap["MYSQL_DATABASE"] = "forgelab"
		}
	}

	envSlice := make([]string, 0, len(envMap)+1)
	for k, v := range envMap {
		envSlice = append(envSlice, fmt.Sprintf("%s=%s", k, v))
	}
	envSlice = append(envSlice, fmt.Sprintf("PORT=%d", intPort))

	containerConfig := &container.Config{
		Image: runImage,
		Env:   envSlice,
		ExposedPorts: nat.PortSet{
			nat.Port(targetPortStr): struct{}{},
		},
		Labels: map[string]string{
			"forgelab.project_id":    project.ID.String(),
			"forgelab.deployment_id": deployment.ID.String(),
		},
	}

	hostConfig, err := ValidateAndBuildSecureHostConfig(ContainerSecurityOptions{
		PortBindings: nat.PortMap{
			nat.Port(targetPortStr): []nat.PortBinding{
				{
					HostIP:   "0.0.0.0",
					HostPort: fmt.Sprintf("%d", allocatedPort),
				},
			},
		},
		TargetCpuMillicores: 1000,
		TargetMemoryMB:      1024,
		TargetPidsLimit:     256,
		Binds:               nil,
		NetworkMode:         "",
	})
	if err != nil {
		e.portManager.ReleasePort(allocatedPort)
		reason := fmt.Sprintf("Failed to validate container security configuration: %v", err)
		emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
		updateStatus(models.DeployStatusFailed, &reason)
		return errors.New(reason)
	}

	containerName := fmt.Sprintf("forgelab-%s-%d", project.Slug, deployment.DeployNumber)
	_ = e.dockerClient.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true})

	var containerID string
	maxLegacyPortRetries := 3
	for attempt := 0; attempt <= maxLegacyPortRetries; attempt++ {
		resp, err := e.dockerClient.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, containerName)
		if err != nil {
			if isPortConflictError(err) && attempt < maxLegacyPortRetries {
				emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Host port %d conflict during container create: %v. Retrying with a new port (attempt %d/%d)...", allocatedPort, err, attempt+1, maxLegacyPortRetries))
				e.portManager.RegisterUsedPort(allocatedPort)
				newPort, allocErr := e.portManager.AllocatePort()
				if allocErr == nil {
					allocatedPort = newPort
					containerConfig.ExposedPorts = nat.PortSet{nat.Port(targetPortStr): struct{}{}}
					hostConfig.PortBindings = nat.PortMap{
						nat.Port(targetPortStr): []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d", allocatedPort)}},
					}
					continue
				}
			}
			e.portManager.ReleasePort(allocatedPort)
			reason := fmt.Sprintf("Failed to create container: %v", err)
			emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
			updateStatus(models.DeployStatusFailed, &reason)
			return errors.New(reason)
		}

		containerID = resp.ID

		if err := e.dockerClient.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
			_ = e.dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
			if isPortConflictError(err) && attempt < maxLegacyPortRetries {
				emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Host port %d conflict during container start: %v. Retrying with a new port (attempt %d/%d)...", allocatedPort, err, attempt+1, maxLegacyPortRetries))
				e.portManager.RegisterUsedPort(allocatedPort)
				newPort, allocErr := e.portManager.AllocatePort()
				if allocErr == nil {
					allocatedPort = newPort
					containerConfig.ExposedPorts = nat.PortSet{nat.Port(targetPortStr): struct{}{}}
					hostConfig.PortBindings = nat.PortMap{
						nat.Port(targetPortStr): []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d", allocatedPort)}},
					}
					continue
				}
			}
			e.portManager.ReleasePort(allocatedPort)
			reason := fmt.Sprintf("Failed to start container: %v", err)
			emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
			updateStatus(models.DeployStatusFailed, &reason)
			return errors.New(reason)
		}
		break
	}

	e.trackActiveContainer(containerID)
	defer e.untrackActiveContainer(containerID)
	_ = e.deploymentService.UpdateDeploymentContainer(ctx, deployment.ID, containerID)

	updateStatus(models.DeployStatusHealthChecking, nil)
	emitLog(models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("Starting health checks on port %d...", allocatedPort))

	healthPath := "/"
	if deployment.HealthStrategy != "" {
		if project.HealthCheckPath != nil && *project.HealthCheckPath != "" {
			healthPath = *project.HealthCheckPath
		}
	}
	healthStrat := deployment.HealthStrategy
	if healthStrat == "" {
		healthStrat = project.HealthStrategy
	}

	healthy, healthReason := e.verifyServiceHealth(
		ctx,
		containerID,
		intPort,
		&allocatedPort,
		healthStrat,
		healthPath,
		func(msg string) { emitLog(models.LogPhaseHealth, models.LogStreamSystem, msg) },
		func(msg string) { emitLog(models.LogPhaseHealth, models.LogStreamStderr, msg) },
	)

	if !healthy {
		detailedReason := fmt.Sprintf("Health verification failed: %s", healthReason)
		emitLog(models.LogPhaseHealth, models.LogStreamStderr, detailedReason)
		e.StopLogCollector(deployment.ID)
		e.StopContainerLogCollector(containerID)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = e.dockerClient.ContainerStop(cleanupCtx, containerID, container.StopOptions{})
		_ = e.dockerClient.ContainerRemove(cleanupCtx, containerID, container.RemoveOptions{Force: true})
		cleanupCancel()
		e.portManager.ReleasePort(allocatedPort)
		updateStatus(models.DeployStatusFailed, &detailedReason)
		emitLog(models.LogPhaseHealth, models.LogStreamSystem, "SAFETY INVARIANT ENFORCED: New deployment failed health check. Previous healthy release remains active!")
		return errors.New(detailedReason)
	}

	updateStatus(models.DeployStatusRunning, nil)
	oldDeploymentID := project.CurrentDeploymentID
	_ = e.deploymentService.SetCurrentDeployment(ctx, project.ID, deployment.ID, models.ProjectStatusRunning)
	_, _ = e.projectService.UpdateProjectPort(ctx, project.ID, allocatedPort)

	// Start runtime log collector for legacy single-container deployment
	e.StartRuntimeLogCollector(RuntimeCollectorParams{
		ContainerID:  containerID,
		ProjectID:    project.ID,
		DeploymentID: &deployment.ID,
		Redactor:     redactor,
	})

	if oldDeploymentID != nil && *oldDeploymentID != deployment.ID {
		oldDeploy, err := e.deploymentService.GetDeployment(ctx, *oldDeploymentID)
		if err == nil && oldDeploy.ContainerID != nil && *oldDeploy.ContainerID != "" {
			e.StopLogCollector(oldDeploy.ID)
			e.StopContainerLogCollector(*oldDeploy.ContainerID)
			emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Stopping previous deployment %d container...", oldDeploy.DeployNumber))
			_ = e.dockerClient.ContainerStop(ctx, *oldDeploy.ContainerID, container.StopOptions{})
			_ = e.dockerClient.ContainerRemove(ctx, *oldDeploy.ContainerID, container.RemoveOptions{Force: true})
			_ = e.deploymentService.UpdateDeploymentStatus(ctx, oldDeploy.ID, models.DeployStatusStopped, nil)
		}
	}

	emitLog(models.LogPhaseRuntime, models.LogStreamSystem, fmt.Sprintf("Deployment #%d is now RUNNING and live on port %d!", deployment.DeployNumber, allocatedPort))
	return nil
}

// stripDockerTimestamp strips the RFC3339/RFC3339Nano timestamp prefix added by Docker's log streaming.
func stripDockerTimestamp(line string) string {
	return logging.StripDockerTimestamp(line)
}

// DemuxDockerStream reads a Docker multiplexed log stream (8-byte header frames)
// and emits individual lines for stdout and stderr to onLine.
// It stops cleanly when ctx is done or reader returns io.EOF.
func DemuxDockerStream(ctx context.Context, reader io.Reader, onLine func(stream string, line string)) error {
	return logging.DemuxDockerStream(ctx, reader, onLine)
}
