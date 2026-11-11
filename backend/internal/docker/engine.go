package docker

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/moby/moby/client"

	"github.com/forgelab/backend/internal/network"
	"github.com/forgelab/backend/internal/proxy"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
	ws "github.com/forgelab/backend/internal/websocket"
)

type sharedBuildPromise = SharedBuildPromise

type activeExecEntry struct {
	id        uuid.UUID
	projectID uuid.UUID
	cancel    context.CancelFunc
	done      chan struct{}
}

// RollbackJobEnqueuer enqueues automatic rollback deployments created by runtime health monitoring.
type RollbackJobEnqueuer interface {
	EnqueueServiceDeployment(ctx context.Context, serviceDeploymentID uuid.UUID) error
}

// ReconciliationState represents the operational reconciliation status between Docker daemon and database.
type ReconciliationState struct {
	Status         string     `json:"status"` // "pending", "completed", "failed", "unreachable"
	LastReconciled *time.Time `json:"last_reconciled,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	Authoritative  bool       `json:"authoritative"`
}

type Engine struct {
	clientMu              sync.Mutex // synchronizes streaming transport wrapping and client updates
	dockerClient          *client.Client
	projectService        *services.ProjectService
	deploymentService     *services.DeploymentService
	secretService         *services.SecretService
	sourceService         *services.SourceService
	githubService         *services.GitHubService
	serviceService        *services.ServiceService
	portManager           *network.PortManager
	pathValidator         *security.PathValidator
	wsHub                 *ws.Hub
	workDir               string
	localBuildMode        string
	maxConcurrentBuilds   int
	buildSem              *BuildSemaphore
	semMu                 sync.Mutex
	buildMaintenanceMu    sync.RWMutex          // synchronizes Docker image builds vs image pruning/maintenance
	pruneFilterLabel      string                // optional label filter for scoped image pruning
	activeLogCollectors   sync.Map              // map[string]context.CancelFunc — tracks running log collector goroutines
	collectorRegistry     *LogCollectorRegistry // race-safe registry and operational diagnostics for runtime log collectors
	activeCancels         sync.Map              // map[uuid.UUID]context.CancelFunc — tracks active deployment execution cancel funcs
	activeExecMu          sync.Mutex            // protects activeExecs map
	activeExecs           map[uuid.UUID]*activeExecEntry
	activeContainers      sync.Map                // map[string]time.Time — containerID -> creation timestamp, tracks in-flight containers during deployment
	sharedBuildArtifacts  sync.Map                // map[string]*sharedBuildPromise — deduplicates and coordinates builds for identical context+dockerfile
	sharedCoordMu         sync.Mutex              // protects initialization and mutation of sharedCoordinator
	sharedCoordinator     *SharedBuildCoordinator // Coordinates shared builds across services with completion guarantees
	cachedBuilderVer      atomic.Value            // stores build.BuilderVersion advertised by Docker daemon
	streamingHookSet      atomic.Bool             // true when dockerClient is already configured with BuildStreamRequestHook
	streamingCli          *client.Client          // cached streaming-hook Docker client built by ensureStreamingTransport
	startTime             time.Time               // instance boot timestamp for tracking interrupted deployments
	publicHost            string                  // host name or IP used for browser-facing URLs
	appHostIP             string                  // host IP binding for application ports (defaults to 127.0.0.1)
	reconcileMu           sync.RWMutex
	reconcileState        ReconciliationState
	queueEnqueuer         RollbackJobEnqueuer
	healthStrikeMu        sync.Mutex
	healthStrikes         map[uuid.UUID]int
	lastAutoRollbackAt    map[uuid.UUID]time.Time
	autoRollbackEnabled   bool
	autoRollbackThreshold int
	autoRollbackCooldown  time.Duration
	trafficRouter         *proxy.TrafficRouter
	onRouteChange         func(context.Context)
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
		dockerClient:          dockerClient,
		projectService:        projectService,
		deploymentService:     deploymentService,
		secretService:         secretService,
		sourceService:         sourceService,
		githubService:         githubService,
		serviceService:        services.NewServiceService(nil),
		portManager:           portManager,
		pathValidator:         pathValidator,
		wsHub:                 wsHub,
		workDir:               workDir,
		localBuildMode:        "snapshot",
		maxConcurrentBuilds:   DefaultMaxConcurrentBuilds(),
		buildSem:              NewBuildSemaphore(DefaultMaxConcurrentBuilds()),
		sharedCoordinator:     NewSharedBuildCoordinator(),
		activeExecs:           make(map[uuid.UUID]*activeExecEntry),
		collectorRegistry:     NewLogCollectorRegistry(),
		startTime:             time.Now(),
		healthStrikes:         make(map[uuid.UUID]int),
		lastAutoRollbackAt:    make(map[uuid.UUID]time.Time),
		autoRollbackEnabled:   true,
		autoRollbackThreshold: 3,
		autoRollbackCooldown:  5 * time.Minute,
		reconcileState: ReconciliationState{
			Status:        "pending",
			Authoritative: false,
		},
	}
}

// SetQueueEnqueuer configures the queue enqueuer used by automatic health rollback.
func (e *Engine) SetQueueEnqueuer(q RollbackJobEnqueuer) {
	e.queueEnqueuer = q
}

// SetTrafficRouter configures the blue-green upstream traffic router.
func (e *Engine) SetTrafficRouter(router *proxy.TrafficRouter) {
	e.trafficRouter = router
}

// GetTrafficRouter returns the configured blue-green traffic router, if any.
func (e *Engine) GetTrafficRouter() *proxy.TrafficRouter {
	return e.trafficRouter
}

// SetAutoRollbackConfig configures automatic rollback behavior on sustained unhealthy runtime checks.
func (e *Engine) SetAutoRollbackConfig(enabled bool, threshold int, cooldown time.Duration) {
	e.healthStrikeMu.Lock()
	defer e.healthStrikeMu.Unlock()
	e.autoRollbackEnabled = enabled
	if threshold > 0 {
		e.autoRollbackThreshold = threshold
	} else if e.autoRollbackThreshold <= 0 {
		e.autoRollbackThreshold = 3
	}
	if cooldown >= 0 {
		e.autoRollbackCooldown = cooldown
	}
}

// GetHealthStrikeCount returns the current consecutive unhealthy check count for a service.
func (e *Engine) GetHealthStrikeCount(serviceID uuid.UUID) int {
	e.healthStrikeMu.Lock()
	defer e.healthStrikeMu.Unlock()
	if e.healthStrikes == nil {
		return 0
	}
	return e.healthStrikes[serviceID]
}

func (e *Engine) resetHealthStrike(serviceID uuid.UUID) {
	e.healthStrikeMu.Lock()
	defer e.healthStrikeMu.Unlock()
	if e.healthStrikes != nil {
		delete(e.healthStrikes, serviceID)
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

// SetStreamingHookConfigured marks whether the Engine's Docker client was already
// constructed with BuildStreamRequestHook attached.
func (e *Engine) SetStreamingHookConfigured(configured bool) {
	e.streamingHookSet.Store(configured)
}

// SetOnRouteChange registers a callback invoked after a service route changes on cutover.
func (e *Engine) SetOnRouteChange(fn func(context.Context)) {
	e.onRouteChange = fn
}
