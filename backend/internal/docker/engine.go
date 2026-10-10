package docker

import (
	"bytes"
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
	"github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	dockernetwork "github.com/docker/docker/api/types/network"
	dockervolume "github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/agent"
	"github.com/forgelab/backend/internal/detector"
	"github.com/forgelab/backend/internal/logging"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/network"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
	"github.com/forgelab/backend/internal/tararchive"
	ws "github.com/forgelab/backend/internal/websocket"
)

type sharedBuildPromise struct {
	done     chan struct{}
	imageTag string
	digest   string
	err      error
}

type Engine struct {
	dockerClient        *client.Client
	projectService      *services.ProjectService
	deploymentService   *services.DeploymentService
	secretService       *services.SecretService
	sourceService       *services.SourceService
	githubService       *services.GitHubService
	serviceService      *services.ServiceService
	portManager         *network.PortManager
	pathValidator       *security.PathValidator
	wsHub               *ws.Hub
	workDir             string
	localBuildMode      string
	maxConcurrentBuilds int
	buildSem            *BuildSemaphore
	semMu               sync.Mutex
	buildMaintenanceMu  sync.RWMutex // synchronizes Docker image builds vs image pruning/maintenance
	pruneFilterLabel    string       // optional label filter for scoped image pruning
	activeLogCollectors sync.Map     // map[string]context.CancelFunc — tracks running log collector goroutines
	activeCancels       sync.Map     // map[uuid.UUID]context.CancelFunc — tracks active deployment execution cancel funcs
	sharedBuildArtifacts sync.Map    // map[string]*sharedBuildPromise — deduplicates and coordinates builds for identical context+dockerfile
	cachedBuilderVer     atomic.Value // stores types.BuilderVersion advertised by Docker daemon
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
		maxConcurrentBuilds: 1,
		buildSem:            NewBuildSemaphore(1),
	}
}

func (e *Engine) getBuildSemaphore() *BuildSemaphore {
	e.semMu.Lock()
	defer e.semMu.Unlock()
	if e.buildSem == nil {
		lim := e.maxConcurrentBuilds
		if lim <= 0 {
			lim = 1
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

func (e *Engine) getOrInitSharedBuild(key string) (*sharedBuildPromise, bool) {
	val, loaded := e.sharedBuildArtifacts.Load(key)
	if loaded {
		return val.(*sharedBuildPromise), true
	}
	newPromise := &sharedBuildPromise{
		done: make(chan struct{}),
	}
	actual, loaded := e.sharedBuildArtifacts.LoadOrStore(key, newPromise)
	return actual.(*sharedBuildPromise), loaded
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
	if val, ok := e.activeCancels.Load(deploymentID); ok {
		if cancel, isFunc := val.(context.CancelFunc); isFunc {
			cancel()
			return true
		}
	}
	return false
}

// acquireBuildSlot acquires a slot under the global Docker build concurrency limit.
func (e *Engine) acquireBuildSlot(ctx context.Context) (func(), error) {
	return e.getBuildSemaphore().Acquire(ctx)
}

// SetServiceService configures the service management service.
func (e *Engine) SetServiceService(ss *services.ServiceService) {
	e.serviceService = ss
}

// SetLocalBuildMode sets the local directory deployment mode ("direct" or "snapshot").
func (e *Engine) SetLocalBuildMode(mode string) {
	if mode != "" {
		e.localBuildMode = mode
	}
}

// resolveSourceDirectory acquires or resolves the source code directory for build operations.
func (e *Engine) resolveSourceDirectory(ctx context.Context, project *models.Project, execID string, emitLog func(phase, stream, message string), sourceRevision ...string) (buildSourceDir string, cleanupDir string, err error) {
	if project.SourceType == models.SourceTypeGitHub {
		snapshotDir := filepath.Join(e.workDir, execID)
		_ = os.MkdirAll(snapshotDir, 0755)
		cleanupDir = snapshotDir
		buildSourceDir = snapshotDir

		ref := project.Branch
		if len(sourceRevision) > 0 && sourceRevision[0] != "" {
			ref = sourceRevision[0]
		}
		if ref == "" {
			ref = "main"
		}

		parts := strings.Split(project.SourceReference, "/")
		if len(parts) != 2 {
			return "", cleanupDir, fmt.Errorf("invalid GitHub repository reference '%s'. Expected format 'owner/repo'", project.SourceReference)
		}
		if e.githubService == nil {
			return "", cleanupDir, errors.New("GitHub integration service is not available")
		}
		if len(ref) != 40 {
			if sha, err := e.githubService.ResolveCommitSHA(ctx, project.OwnerID, parts[0], parts[1], ref); err == nil && len(sha) == 40 {
				ref = sha
			}
		}
		emitLog(models.LogPhaseSource, models.LogStreamSystem, fmt.Sprintf("Acquiring GitHub repository archive for '%s' (revision: %s)...", project.SourceReference, ref))
		if err := e.githubService.AcquireRepoTarball(ctx, project.OwnerID, parts[0], parts[1], ref, snapshotDir); err != nil {
			return "", cleanupDir, fmt.Errorf("failed to acquire GitHub repository archive: %w", err)
		}
		emitLog(models.LogPhaseSource, models.LogStreamSystem, "GitHub repository archive acquired successfully.")
		return buildSourceDir, cleanupDir, nil

	} else if project.SourceType == models.SourceTypeLocalUpload || (project.SourceType == models.SourceTypeLocal && project.SourceReference != "") {
		sourceUUID, err := uuid.Parse(project.SourceReference)
		if err != nil {
			return "", "", fmt.Errorf("invalid local source upload ID '%s': %w", project.SourceReference, err)
		}
		if e.sourceService == nil {
			return "", "", errors.New("local source upload service is not available")
		}
		p, err := e.sourceService.GetSourcePath(ctx, project.OwnerID, sourceUUID)
		if err != nil {
			return "", "", fmt.Errorf("failed to locate uploaded source files: %w", err)
		}
		buildSourceDir = p
		emitLog(models.LogPhaseSource, models.LogStreamSystem, "Using isolated uploaded source workspace.")
		return buildSourceDir, "", nil

	} else if project.SourceType == models.SourceTypeLocalDirectory || project.RepositoryPath != "" {
		canonicalSource, err := e.pathValidator.ValidateSourcePath(project.RepositoryPath)
		if err != nil {
			return "", "", fmt.Errorf("source path validation failed: %w", err)
		}

		emitLog(models.LogPhaseSource, models.LogStreamSystem, fmt.Sprintf("Using local directory: %s", project.RepositoryPath))

		if e.localBuildMode != "direct" {
			emitLog(models.LogPhaseSource, models.LogStreamSystem, "Using snapshot filesystem build mode.")
			snapshotDir := filepath.Join(e.workDir, execID)
			_ = os.MkdirAll(snapshotDir, 0755)
			cleanupDir = snapshotDir
			if err := copyDirectory(canonicalSource, snapshotDir); err != nil {
				return "", cleanupDir, fmt.Errorf("failed to snapshot source files: %w", err)
			}
			buildSourceDir = snapshotDir
		} else {
			emitLog(models.LogPhaseSource, models.LogStreamSystem, "Using direct filesystem build mode.")
			buildSourceDir = canonicalSource
		}
		return buildSourceDir, cleanupDir, nil

	} else if project.SourceType == models.SourceTypeLocalAgent {
		emitLog(models.LogPhaseSource, models.LogStreamSystem, fmt.Sprintf("Connected to ForgeLAB local agent (session: %s).", project.SourceReference))
		return "", "", nil
	}

	return "", "", errors.New("no valid local directory, uploaded source, or repository configured")
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
	_ = e.projectService.UpdateProjectStatus(ctx, projectID, newStatus)
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
		slog.Info("service deployment already in terminal state; skipping execution", "service_deployment_id", serviceDeploymentID, "status", serviceDeploy.Status)
		return nil
	}

	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()
	e.RegisterActiveCancel(serviceDeploymentID, execCancel)
	defer e.UnregisterActiveCancel(serviceDeploymentID)

	service, err := e.serviceService.GetService(execCtx, serviceDeploy.ServiceID)
	if err != nil {
		return fmt.Errorf("failed to get service %s: %w", serviceDeploy.ServiceID, err)
	}

	project, err := e.getProjectByID(execCtx, service.ProjectID)
	if err != nil {
		return fmt.Errorf("failed to get project %s: %w", service.ProjectID, err)
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
				Type:    "log",
				Channel: sdChannel,
				Data:    data,
			})

			// If part of a release, also publish to the release-scoped channels
			if serviceDeploy.DeploymentID != nil {
				data["deployment_id"] = serviceDeploy.DeploymentID.String()
				serviceChannel := fmt.Sprintf("deployment:%s:service:%s", serviceDeploy.DeploymentID.String(), service.ID.String())
				_ = e.wsHub.PublishEvent(serviceChannel, &ws.EventMessage{
					Type:    "log",
					Channel: serviceChannel,
					Data:    data,
				})
				channel := "deployment:" + serviceDeploy.DeploymentID.String()
				_ = e.wsHub.PublishEvent(channel, &ws.EventMessage{
					Type:    "log",
					Channel: channel,
					Data:    data,
				})
			}
			projectChannel := fmt.Sprintf("project:%s", project.ID.String())
			_ = e.wsHub.PublishEvent(projectChannel, &ws.EventMessage{
				Type:    "service_deployment_log",
				Channel: projectChannel,
				Data:    data,
			})
		}
	}

	emitStatus := func(newStatus string, hostPort *int, failureReason *string) {
		_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, serviceDeploy.ID, newStatus, failureReason)
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
		servicesMap[s.Name] = s
	}

	// Preflight validation for single service deployment: verifies service config and that dependencies exist in project
	if err := ValidateDeploymentPreflight(execCtx, PreflightOptions{
		Project:             project,
		Deployments:         []*models.ServiceDeployment{serviceDeploy},
		ServicesMap:         servicesMap,
		PathValidator:       e.pathValidator,
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
			sharedKey = fmt.Sprintf("%s:%s:%s:%s", project.ID.String(), srcRev, cleanCtx, cleanDF)
			p, loaded := e.getOrInitSharedBuild(sharedKey)
			sharedPromise = p
			if loaded {
				emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Service '%s' shares build context and Dockerfile ('%s', '%s') with another service. Waiting for shared build artifact...", service.Name, cleanCtx, cleanDF))
				select {
				case <-sharedPromise.done:
					if sharedPromise.err != nil {
						reason := fmt.Sprintf("Shared image build failed: %v", sharedPromise.err)
						emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
						emitStatus(models.DeployStatusFailed, nil, &reason)
						return errors.New(reason)
					}
					if sharedPromise.imageTag != "" {
						runImage = sharedPromise.imageTag
						if e.dockerClient != nil {
							_ = e.dockerClient.ImageTag(execCtx, sharedPromise.imageTag, svcTag)
							runImage = svcTag
							if service.Image != "" {
								_ = e.dockerClient.ImageTag(execCtx, sharedPromise.imageTag, service.Image)
							}
						}
						if sharedPromise.digest != "" && e.deploymentService != nil {
							_ = e.deploymentService.UpdateServiceDeploymentImageDigest(ctx, serviceDeploy.ID, sharedPromise.digest)
						}
						reusingExistingImage = true
						emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Reusing shared build artifact '%s' (build context: '%s', dockerfile: '%s'). Skipping Docker build.", sharedPromise.imageTag, cleanCtx, cleanDF))
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
			if e.sourceService != nil {
				if srcUUID, err := uuid.Parse(project.SourceReference); err == nil {
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
			}
			agentURL := fmt.Sprintf("%s/api/agent/sources/%s/stream-context?%s",
				baseURL,
				project.SourceReference,
				agentQuery.Encode(),
			)
			req, reqErr := http.NewRequestWithContext(execCtx, http.MethodGet, agentURL, nil)
			if reqErr != nil {
				reason := fmt.Sprintf("Failed to request agent stream context: %v", reqErr)
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
				emitStatus(models.DeployStatusFailed, nil, &reason)
				return errors.New(reason)
			}
			req.Header.Set("Authorization", "Bearer "+agentToken)
			req.Header.Set("X-Agent-Session-Token", agentToken)
			resp, httpErr := http.DefaultClient.Do(req)
			if httpErr != nil || resp.StatusCode != http.StatusOK {
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
				generatedContent := detector.GenerateDockerfile(service.RuntimeType, intPort, service.StartCommand)
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
			"forgelab.managed":      "true",
			"forgelab.project_id":   project.ID.String(),
			"forgelab.service_name": service.Name,
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

		if err := e.buildImage(execCtx, tarArchive, buildOpts, func(msg string) {
			emitLog(models.LogPhaseBuild, models.LogStreamStdout, msg)
		}); err != nil {
			if sharedPromise != nil {
				sharedPromise.err = err
				close(sharedPromise.done)
				e.sharedBuildArtifacts.Delete(sharedKey)
			}
			reason := err.Error()
			cat := ClassifyDockerBuildError(err)
			if cat == CategoryStorageDaemon || cat == CategoryDaemonUnreachable || cat == CategoryBuildKitRequired {
				diag := e.formatDockerDiagnostic(ctx, err, cat)
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, diag)
			} else {
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
			}
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
			sharedPromise.imageTag = svcTag
			sharedPromise.digest = digest
			close(sharedPromise.done)
		}
		if service.Image != "" && e.dockerClient != nil {
			_ = e.dockerClient.ImageTag(ctx, svcTag, service.Image)
		}
		runImage = svcTag
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
		serviceDeploy.Classification == models.ClassificationJob
	shouldExposePort := !isInternalService && (service.PublicExposed || service.Role == models.RoleFrontend || len(allProjectServices) <= 1)

	if shouldExposePort {
		allocated, err := e.portManager.AllocatePort()
		if err == nil {
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
	}

	svcEnv := make([]string, 0, len(runtimeEnvMap)+len(allProjectServices)*3+2)
	for k, v := range runtimeEnvMap {
		svcEnv = append(svcEnv, fmt.Sprintf("%s=%s", k, v))
	}
	svcEnv = append(svcEnv, fmt.Sprintf("PORT=%d", intPort))
	svcEnv = append(svcEnv, fmt.Sprintf("SERVICE_NAME=%s", service.Name))

	for _, peer := range allProjectServices {
		peerIntPort := peer.InternalPort
		if peerIntPort <= 0 {
			peerIntPort = 8080
		}
		upperName := strings.ToUpper(strings.ReplaceAll(peer.Name, "-", "_"))
		svcEnv = append(svcEnv, fmt.Sprintf("%s_HOST=%s", upperName, peer.Name))
		svcEnv = append(svcEnv, fmt.Sprintf("%s_PORT=%d", upperName, peerIntPort))
		svcEnv = append(svcEnv, fmt.Sprintf("%s_URL=http://%s:%d", upperName, peer.Name, peerIntPort))
		if peer.Role == models.RoleBackend {
			svcEnv = append(svcEnv, fmt.Sprintf("BACKEND_URL=http://%s:%d", peer.Name, peerIntPort))
			svcEnv = append(svcEnv, fmt.Sprintf("API_URL=http://%s:%d", peer.Name, peerIntPort))
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

	if len(serviceNetworks) > 0 {
		for i, net := range serviceNetworks {
			projNet := fmt.Sprintf("forgelab-net-%s-%s", project.ID.String()[:8], net)
			if e.dockerClient != nil {
				_, netErr := e.dockerClient.NetworkInspect(execCtx, projNet, dockernetwork.InspectOptions{})
				if netErr != nil {
					_, _ = e.dockerClient.NetworkCreate(execCtx, projNet, dockernetwork.CreateOptions{
						Driver: "bridge",
						Labels: map[string]string{
							"forgelab.project_id": project.ID.String(),
							"forgelab.network":    net,
						},
					})
				}
			}
			if i == 0 {
				primaryNetworkName = projNet
			} else {
				secondaryNetworkNames = append(secondaryNetworkNames, projNet)
			}
		}
	}

	// Always ensure common project network is present so all services in the project can resolve each other via DNS aliases
	commonProjectNet := fmt.Sprintf("forgelab-net-%s", project.ID.String())
	if e.dockerClient != nil {
		_, netErr := e.dockerClient.NetworkInspect(execCtx, commonProjectNet, dockernetwork.InspectOptions{})
		if netErr != nil {
			_, _ = e.dockerClient.NetworkCreate(execCtx, commonProjectNet, dockernetwork.CreateOptions{
				Driver: "bridge",
				Labels: map[string]string{
					"forgelab.project_id": project.ID.String(),
				},
			})
		}
	}
	if primaryNetworkName == "" {
		primaryNetworkName = commonProjectNet
	} else {
		foundCommon := false
		for _, sn := range secondaryNetworkNames {
			if sn == commonProjectNet {
				foundCommon = true
				break
			}
		}
		if !foundCommon && primaryNetworkName != commonProjectNet {
			secondaryNetworkNames = append(secondaryNetworkNames, commonProjectNet)
		}
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
	projectRoot := project.RepositoryPath
	if projectRoot != "" {
		allowedMountPrefixes = append(allowedMountPrefixes, projectRoot)
	}

	for _, v := range serviceDeploy.Volumes {
		if v.Target == "" {
			continue
		}

		volType := v.Type
		if volType == "" {
			if strings.HasPrefix(v.Source, ".") || strings.HasPrefix(v.Source, "/") || strings.HasPrefix(v.Source, "~") || strings.Contains(v.Source, string(filepath.Separator)) || strings.Contains(v.Source, "/") {
				volType = models.VolumeTypeBind
			} else {
				volType = models.VolumeTypeNamed
			}
		}

		if volType == models.VolumeTypeNamed || volType == "volume" {
			volName := fmt.Sprintf("forgelab-vol-%s-%s", project.ID.String()[:8], v.Source)
			if e.dockerClient != nil {
				_, _ = e.dockerClient.VolumeCreate(execCtx, dockervolume.CreateOptions{
					Name: volName,
					Labels: map[string]string{
						"forgelab.project_id":  project.ID.String(),
						"forgelab.volume_name": v.Source,
					},
				})
			}
			bindEntry := fmt.Sprintf("%s:%s", volName, v.Target)
			if v.ReadOnly {
				bindEntry += ":ro"
			}
			binds = append(binds, bindEntry)
		} else if volType == models.VolumeTypeBind || volType == "bind" {
			if !filepath.IsAbs(v.Source) && (project.SourceType == models.SourceTypeGitHub || project.SourceType == models.SourceTypeLocalAgent) {
				reason := fmt.Sprintf("Relative bind mount '%s' is not supported for remote/agent source '%s'. Use a named volume or absolute host path.", v.Source, project.SourceType)
				emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
				emitStatus(models.DeployStatusFailed, nil, &reason)
				return errors.New(reason)
			}

			hostPath := v.Source
			if !filepath.IsAbs(hostPath) {
				if projectRoot != "" {
					hostPath = filepath.Join(projectRoot, hostPath)
				} else if project.SourceType == models.SourceTypeLocalUpload && e.sourceService != nil {
					if srcUUID, err := uuid.Parse(project.SourceReference); err == nil {
						if p, err := e.sourceService.GetSourcePath(execCtx, project.OwnerID, srcUUID); err == nil {
							hostPath = filepath.Join(p, hostPath)
							allowedMountPrefixes = append(allowedMountPrefixes, p)
						}
					}
				}
			}
			hostPath = filepath.Clean(hostPath)
			bindEntry := fmt.Sprintf("%s:%s", hostPath, v.Target)
			if v.ReadOnly {
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

	hostConfig, err := ValidateAndBuildSecureHostConfig(ContainerSecurityOptions{
		PortBindings:         portBindings,
		TargetCpuMillicores:  targetCpuMillicores,
		TargetMemoryMB:       targetMemoryMB,
		TargetPidsLimit:      targetPidsLimit,
		Binds:                binds,
		NetworkMode:          "",
		AllowedMountPrefixes: allowedMountPrefixes,
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

	netConfig := &dockernetwork.NetworkingConfig{
		EndpointsConfig: map[string]*dockernetwork.EndpointSettings{
			primaryNetworkName: {
				Aliases: []string{service.Name},
			},
		},
	}

	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = e.dockerClient.ContainerRemove(cleanupCtx, containerName, container.RemoveOptions{Force: true})
	cleanupCancel()

	resp, err := e.dockerClient.ContainerCreate(execCtx, containerConfig, hostConfig, netConfig, nil, containerName)
	if err != nil {
		if hostPort != nil {
			e.portManager.ReleasePort(*hostPort)
		}
		reason := fmt.Sprintf("Failed to create container for service '%s': %v", service.Name, err)
		emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
		emitStatus(models.DeployStatusFailed, nil, &reason)
		return errors.New(reason)
	}

	// Connect to any secondary isolated project networks
	for _, secNet := range secondaryNetworkNames {
		if e.dockerClient != nil {
			_ = e.dockerClient.NetworkConnect(execCtx, secNet, resp.ID, &dockernetwork.EndpointSettings{
				Aliases: []string{service.Name},
			})
		}
	}

	containerID := resp.ID
	if err := e.dockerClient.ContainerStart(execCtx, containerID, container.StartOptions{}); err != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = e.dockerClient.ContainerRemove(cleanupCtx, containerID, container.RemoveOptions{Force: true})
		cleanupCancel()
		if hostPort != nil {
			e.portManager.ReleasePort(*hostPort)
		}
		reason := fmt.Sprintf("Failed to start container for service '%s': %v", service.Name, err)
		emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
		emitStatus(models.DeployStatusFailed, nil, &reason)
		return errors.New(reason)
	}

	emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Container %s started for service '%s'.", containerID[:12], service.Name))

	// Start runtime log collector — continuously reads container stdout/stderr
	e.startRuntimeLogCollector(execCtx, containerID, serviceDeploy.ID, service.ID, project.ID, redactor, emitLog)

	isJob := service.Classification == models.ClassificationJob || serviceDeploy.Classification == models.ClassificationJob
	if isJob {
		emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Service '%s' is a one-shot job (classification: %s). Waiting for container execution to complete...", service.Name, service.Classification))
		if e.dockerClient != nil {
			statusCh, errCh := e.dockerClient.ContainerWait(execCtx, containerID, container.WaitConditionNotRunning)
			select {
			case err := <-errCh:
				if err != nil {
					reason := fmt.Sprintf("Failed waiting for job container '%s': %v", service.Name, err)
					emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
					emitStatus(models.DeployStatusFailed, nil, &reason)
					return errors.New(reason)
				}
			case status := <-statusCh:
				if status.Error != nil && status.Error.Message != "" {
					reason := fmt.Sprintf("Job container '%s' failed: %s (exit code %d)", service.Name, status.Error.Message, status.StatusCode)
					emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
					emitStatus(models.DeployStatusFailed, nil, &reason)
					return errors.New(reason)
				}
				if status.StatusCode != 0 {
					reason := fmt.Sprintf("Job container '%s' exited with non-zero status code %d", service.Name, status.StatusCode)
					emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
					emitStatus(models.DeployStatusFailed, nil, &reason)
					return errors.New(reason)
				}
				emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Job container '%s' completed successfully with exit code 0.", service.Name))
			case <-execCtx.Done():
				reason := fmt.Sprintf("Job execution timed out or was cancelled for service '%s'", service.Name)
				emitStatus(models.DeployStatusFailed, nil, &reason)
				return errors.New(reason)
			}
		}
		e.StopLogCollector(serviceDeploy.ID)
		err = e.serviceService.PromoteServiceDeployment(ctx, service.ID, serviceDeploy.ID, containerID, runImage, nil)
		if err != nil {
			reason := fmt.Sprintf("Failed to promote job service deployment '%s': %v", service.Name, err)
			emitLog(models.LogPhaseHealth, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return fmt.Errorf("failed to promote job service deployment: %w", err)
		}
		_ = e.deploymentService.UpdateServiceDeploymentContainer(ctx, serviceDeploy.ID, containerID, nil)
		_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, serviceDeploy.ID, models.DeployStatusRunning, nil)
		emitStatus(models.DeployStatusRunning, nil, nil)
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
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = e.dockerClient.ContainerStop(cleanupCtx, containerID, container.StopOptions{})
		_ = e.dockerClient.ContainerRemove(cleanupCtx, containerID, container.RemoveOptions{Force: true})
		cleanupCancel()
		if hostPort != nil {
			e.portManager.ReleasePort(*hostPort)
		}

		if hasPreviousHealthy {
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
		emitLog(models.LogPhaseRuntime, models.LogStreamSystem, fmt.Sprintf("Stopping previous container %s for service '%s'...", (*previousContainerID)[:12], service.Name))
		if previousDeploymentID != nil {
			e.StopLogCollector(*previousDeploymentID)
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = e.dockerClient.ContainerStop(cleanupCtx, *previousContainerID, container.StopOptions{})
		_ = e.dockerClient.ContainerRemove(cleanupCtx, *previousContainerID, container.RemoveOptions{Force: true})
		cleanupCancel()
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

	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()
	e.RegisterActiveCancel(deploymentID, execCancel)
	defer e.UnregisterActiveCancel(deploymentID)

	project, err := e.getProjectByID(execCtx, deployment.ProjectID)
	if err != nil {
		return fmt.Errorf("failed to get project %s: %w", deployment.ProjectID, err)
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
				"deployment_id": deployment.ID.String(),
				"timestamp":     persistedLog.Timestamp.Format(time.RFC3339Nano),
				"phase":         persistedLog.Phase,
				"stream":        persistedLog.Stream,
				"message":       persistedLog.Message,
			}
			channel := "deployment:" + deployment.ID.String()
			_ = e.wsHub.PublishEvent(channel, &ws.EventMessage{
				Type:    "log",
				Channel: channel,
				Data:    data,
			})
			projectChannel := fmt.Sprintf("project:%s", project.ID.String())
			_ = e.wsHub.PublishEvent(projectChannel, &ws.EventMessage{
				Type:    "release_log",
				Channel: projectChannel,
				Data:    data,
			})
		}
	}

	updateReleaseStatus := func(newStatus string, failureReason *string) {
		_ = e.deploymentService.UpdateDeploymentStatus(ctx, deployment.ID, newStatus, failureReason)
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

	servicesMap := make(map[string]*models.Service, len(allProjectServices))
	for _, s := range allProjectServices {
		servicesMap[s.Name] = s
	}

	// Preflight validation for the release: fails fast before any builds or container creation
	if err := ValidateDeploymentPreflight(execCtx, PreflightOptions{
		Project:       project,
		Deployments:   svcDeploys,
		ServicesMap:   servicesMap,
		PathValidator: e.pathValidator,
	}); err != nil {
		reason := fmt.Sprintf("Preflight validation failed: %v", err)
		updateReleaseStatus(models.DeployStatusFailed, &reason)
		emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
		return errors.New(reason)
	}

	var (
		mu         sync.Mutex
		allHealthy = true
		anyHealthy = false
		errList    []string
	)

	maxConcurrent := 4
	sem := make(chan struct{}, maxConcurrent)

	tiers := buildServiceDeploymentTiers(svcDeploys)
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
				_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, sd.ID, models.DeployStatusFailed, &reason)
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
		updateReleaseStatus(models.DeployStatusRunning, nil)
		_ = e.deploymentService.SetCurrentDeployment(ctx, project.ID, deployment.ID, newProjectStatus)
		emitLog(models.LogPhaseRuntime, models.LogStreamSystem, fmt.Sprintf("Release #%d successfully deployed all services!", deployment.DeployNumber))
		return nil
	} else if anyHealthy {
		// Valid state transition: deployment status is Running (not partially_running!), while project status reflects partially_running
		updateReleaseStatus(models.DeployStatusRunning, nil)
		_ = e.deploymentService.SetCurrentDeployment(ctx, project.ID, deployment.ID, newProjectStatus)
		emitLog(models.LogPhaseRuntime, models.LogStreamSystem, fmt.Sprintf("Release #%d finished with partial success (%s).", deployment.DeployNumber, strings.Join(errList, "; ")))
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
				"deployment_id": deployment.ID.String(),
				"timestamp":     persistedLog.Timestamp.Format(time.RFC3339Nano),
				"phase":         persistedLog.Phase,
				"stream":        persistedLog.Stream,
				"message":       persistedLog.Message,
			}
			channel := "deployment:" + deployment.ID.String()
			_ = e.wsHub.PublishEvent(channel, &ws.EventMessage{
				Type:    "log",
				Channel: channel,
				Data:    data,
			})
		}
	}

	updateStatus := func(newStatus string, failureReason *string) {
		prevStatus := deployment.Status
		_ = e.deploymentService.UpdateDeploymentStatus(ctx, deployment.ID, newStatus, failureReason)
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
		buildSourceDir, cleanupDir, err := e.resolveSourceDirectory(ctx, project, deployment.ID.String(), emitLog)
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
			if e.sourceService != nil {
				if srcUUID, err := uuid.Parse(project.SourceReference); err == nil {
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
			agentURL := fmt.Sprintf("%s/api/agent/sources/%s/stream-context?service_path=%s&runtime=%s&port=%d&start_cmd=%s",
				baseURL,
				project.SourceReference,
				url.QueryEscape(cleanRelPath),
				url.QueryEscape(deployment.RuntimeType),
				deployment.InternalPort,
				url.QueryEscape(deployment.StartCommand),
			)
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, agentURL, nil)
			if reqErr != nil {
				reason := fmt.Sprintf("Failed to request agent stream context: %v", reqErr)
				emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
				updateStatus(models.DeployStatusFailed, &reason)
				return errors.New(reason)
			}
			req.Header.Set("Authorization", "Bearer "+agentToken)
			req.Header.Set("X-Agent-Session-Token", agentToken)
			resp, httpErr := http.DefaultClient.Do(req)
			if httpErr != nil || resp.StatusCode != http.StatusOK {
				reason := "Failed to stream source from local agent"
				if httpErr != nil {
					reason = httpErr.Error()
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
				dockerfileContent := detector.GenerateDockerfile(deployment.RuntimeType, port, startCmd)
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

	resp, err := e.dockerClient.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, containerName)
	if err != nil {
		e.portManager.ReleasePort(allocatedPort)
		reason := fmt.Sprintf("Failed to create container: %v", err)
		emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
		updateStatus(models.DeployStatusFailed, &reason)
		return errors.New(reason)
	}

	containerID := resp.ID
	_ = e.deploymentService.UpdateDeploymentContainer(ctx, deployment.ID, containerID)

	if err := e.dockerClient.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		_ = e.dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
		e.portManager.ReleasePort(allocatedPort)
		reason := fmt.Sprintf("Failed to start container: %v", err)
		emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
		updateStatus(models.DeployStatusFailed, &reason)
		return errors.New(reason)
	}

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

	if oldDeploymentID != nil && *oldDeploymentID != deployment.ID {
		oldDeploy, err := e.deploymentService.GetDeployment(ctx, *oldDeploymentID)
		if err == nil && oldDeploy.ContainerID != nil && *oldDeploy.ContainerID != "" {
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

// startRuntimeLogCollector starts a background goroutine that reads container stdout/stderr
// from the Docker daemon and persists + publishes each individual line with phase=runtime.
// It uses a cancellable context tied to the deployment lifecycle, and exits cleanly when the
// container exits (io.EOF) or the context is cancelled.
func (e *Engine) startRuntimeLogCollector(parentCtx context.Context, containerID string, serviceDeployID, serviceID, projectID uuid.UUID, redactor *logging.LogRedactor, emitLog func(phase, stream, message string)) {
	logCtx, logCancel := context.WithCancel(parentCtx)
	collectorKey := serviceDeployID.String()

	// Cancel any previous collector for this service deployment
	if prev, loaded := e.activeLogCollectors.LoadAndDelete(collectorKey); loaded {
		if cancelFn, ok := prev.(context.CancelFunc); ok {
			cancelFn()
		}
	}
	e.activeLogCollectors.Store(collectorKey, logCancel)

	go func() {
		defer func() {
			e.activeLogCollectors.Delete(collectorKey)
			logCancel()
		}()

		reader, err := e.dockerClient.ContainerLogs(logCtx, containerID, container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Follow:     true,
			Timestamps: true,
			Since:      time.Now().Add(-2 * time.Second).Format(time.RFC3339),
		})
		if err != nil {
			slog.Error("failed to attach runtime log collector", "container", containerID[:12], "error", err)
			return
		}
		defer reader.Close()

		if err := DemuxDockerStream(logCtx, reader, func(stream string, line string) {
			emitLog(models.LogPhaseRuntime, stream, line)
		}); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("runtime log collector demux error", "container", containerID[:12], "error", err)
		} else {
			slog.Info("runtime log collector ended", "container", containerID[:12], "service_deployment_id", serviceDeployID)
		}
	}()
}

// StopLogCollector stops the runtime log collector for a specific service deployment.
func (e *Engine) StopLogCollector(serviceDeployID uuid.UUID) {
	if cancelFn, loaded := e.activeLogCollectors.LoadAndDelete(serviceDeployID.String()); loaded {
		if fn, ok := cancelFn.(context.CancelFunc); ok {
			fn()
		}
	}
}

// StopAllLogCollectors stops all active runtime log collector goroutines (used during shutdown).
func (e *Engine) StopAllLogCollectors() {
	e.activeLogCollectors.Range(func(key, value interface{}) bool {
		if cancelFn, ok := value.(context.CancelFunc); ok {
			cancelFn()
		}
		e.activeLogCollectors.Delete(key)
		return true
	})
	slog.Info("all runtime log collectors stopped")
}

// Helper methods for Docker Engine
func (e *Engine) getProjectByID(ctx context.Context, projectID uuid.UUID) (*models.Project, error) {
	return e.projectService.GetProjectByIDWithoutOwnership(ctx, projectID)
}

// StopApp stops the currently running container for a project.
func (e *Engine) StopApp(ctx context.Context, projectID, ownerID uuid.UUID) error {
	project, err := e.projectService.GetProject(ctx, projectID, ownerID)
	if err != nil {
		return err
	}

	if project.CurrentDeploymentID == nil {
		return errors.New("project has no active deployment to stop")
	}

	deployment, err := e.deploymentService.GetDeployment(ctx, *project.CurrentDeploymentID)
	if err != nil {
		return err
	}

	if deployment.ContainerID != nil && *deployment.ContainerID != "" {
		if err := e.dockerClient.ContainerStop(ctx, *deployment.ContainerID, container.StopOptions{}); err != nil {
			slog.Error("failed to stop container", "container_id", *deployment.ContainerID, "error", err)
		}
	}

	_ = e.deploymentService.UpdateDeploymentStatus(ctx, deployment.ID, models.DeployStatusStopped, nil)
	_, _ = e.projectService.UpdateProject(ctx, projectID, ownerID, services.UpdateProjectInput{})
	_ = e.deploymentService.SetCurrentDeployment(ctx, projectID, deployment.ID, models.ProjectStatusStopped)

	slog.Info("project app stopped", "project_id", projectID)
	return nil
}

// StartApp starts the stopped container for a project.
func (e *Engine) StartApp(ctx context.Context, projectID, ownerID uuid.UUID) error {
	project, err := e.projectService.GetProject(ctx, projectID, ownerID)
	if err != nil {
		return err
	}

	if project.CurrentDeploymentID == nil {
		return errors.New("project has no active deployment to start")
	}

	deployment, err := e.deploymentService.GetDeployment(ctx, *project.CurrentDeploymentID)
	if err != nil {
		return err
	}

	if deployment.ContainerID != nil && *deployment.ContainerID != "" {
		if err := e.dockerClient.ContainerStart(ctx, *deployment.ContainerID, container.StartOptions{}); err != nil {
			return fmt.Errorf("failed to start container: %w", err)
		}
	}

	_ = e.deploymentService.UpdateDeploymentStatus(ctx, deployment.ID, models.DeployStatusRunning, nil)
	_ = e.deploymentService.SetCurrentDeployment(ctx, projectID, deployment.ID, models.ProjectStatusRunning)

	slog.Info("project app started", "project_id", projectID)
	return nil
}

// RestartApp restarts the container for a project.
func (e *Engine) RestartApp(ctx context.Context, projectID, ownerID uuid.UUID) error {
	project, err := e.projectService.GetProject(ctx, projectID, ownerID)
	if err != nil {
		return err
	}

	if project.CurrentDeploymentID == nil {
		return errors.New("project has no active deployment to restart")
	}

	deployment, err := e.deploymentService.GetDeployment(ctx, *project.CurrentDeploymentID)
	if err != nil {
		return err
	}

	if deployment.ContainerID != nil && *deployment.ContainerID != "" {
		timeout := 10
		if err := e.dockerClient.ContainerRestart(ctx, *deployment.ContainerID, container.StopOptions{Timeout: &timeout}); err != nil {
			return fmt.Errorf("failed to restart container: %w", err)
		}
	}

	_ = e.deploymentService.UpdateDeploymentStatus(ctx, deployment.ID, models.DeployStatusRunning, nil)
	_ = e.deploymentService.SetCurrentDeployment(ctx, projectID, deployment.ID, models.ProjectStatusRunning)

	slog.Info("project app restarted", "project_id", projectID)
	return nil
}

// StopService stops the container of an individual service and recalculates project status.
func (e *Engine) StopService(ctx context.Context, projectID, serviceID, ownerID uuid.UUID) error {
	project, err := e.projectService.GetProject(ctx, projectID, ownerID)
	if err != nil {
		return err
	}
	if e.serviceService == nil {
		return errors.New("service management unavailable")
	}
	svc, err := e.serviceService.GetService(ctx, serviceID)
	if err != nil {
		return err
	}
	if svc.CurrentServiceDeploymentID != nil {
		e.StopLogCollector(*svc.CurrentServiceDeploymentID)
	}
	if svc.ContainerID != nil && *svc.ContainerID != "" {
		_ = e.dockerClient.ContainerStop(ctx, *svc.ContainerID, container.StopOptions{})
	}
	_ = e.serviceService.UpdateServiceStatus(ctx, serviceID, models.ServiceStatusStopped, nil, nil, nil)
	svcs, _ := e.serviceService.ListServices(ctx, project.ID)
	newStatus := services.CalculateProjectStatus(svcs)
	_ = e.projectService.UpdateProjectStatus(ctx, project.ID, newStatus)
	return nil
}

// StartService starts the container of an individual service and recalculates project status.
func (e *Engine) StartService(ctx context.Context, projectID, serviceID, ownerID uuid.UUID) error {
	project, err := e.projectService.GetProject(ctx, projectID, ownerID)
	if err != nil {
		return err
	}
	if e.serviceService == nil {
		return errors.New("service management unavailable")
	}
	svc, err := e.serviceService.GetService(ctx, serviceID)
	if err != nil {
		return err
	}
	if svc.ContainerID != nil && *svc.ContainerID != "" {
		if err := e.dockerClient.ContainerStart(ctx, *svc.ContainerID, container.StartOptions{}); err != nil {
			return fmt.Errorf("failed to start service container: %w", err)
		}
	}
	_ = e.serviceService.UpdateServiceStatus(ctx, serviceID, models.ServiceStatusRunning, nil, nil, nil)
	svcs, _ := e.serviceService.ListServices(ctx, project.ID)
	newStatus := services.CalculateProjectStatus(svcs)
	_ = e.projectService.UpdateProjectStatus(ctx, project.ID, newStatus)
	return nil
}

// RestartService restarts an individual service.
func (e *Engine) RestartService(ctx context.Context, projectID, serviceID, ownerID uuid.UUID) error {
	_ = e.StopService(ctx, projectID, serviceID, ownerID)
	return e.StartService(ctx, projectID, serviceID, ownerID)
}

// CleanUpProjectContainers stops and removes all containers associated with a project before deletion.
func (e *Engine) CleanUpProjectContainers(ctx context.Context, projectID uuid.UUID) {
	if e.dockerClient == nil {
		return
	}

	// 1. Clean up legacy single-container deployment containers
	deployments, err := e.deploymentService.ListDeployments(ctx, projectID)
	if err == nil {
		for _, d := range deployments {
			if d.ContainerID != nil && *d.ContainerID != "" {
				_ = e.dockerClient.ContainerStop(ctx, *d.ContainerID, container.StopOptions{})
				_ = e.dockerClient.ContainerRemove(ctx, *d.ContainerID, container.RemoveOptions{Force: true, RemoveVolumes: true})
			}
		}
	}

	// 2. Clean up multi-service containers
	if e.serviceService != nil {
		svcs, _ := e.serviceService.ListServices(ctx, projectID)
		for _, svc := range svcs {
			if svc.ContainerID != nil && *svc.ContainerID != "" {
				_ = e.dockerClient.ContainerStop(ctx, *svc.ContainerID, container.StopOptions{})
				_ = e.dockerClient.ContainerRemove(ctx, *svc.ContainerID, container.RemoveOptions{Force: true, RemoveVolumes: true})
			}
		}
	}
}

// ReconcileDaemonContainers reconciles DB container state against Docker daemon state on startup:
// 1. Detects containers stopped/crashed while backend was offline and marks services/projects as crashed/stopped.
// 2. Cleans up orphaned ForgeLAB containers whose project or deployment is terminated.
// 3. Syncs active host port allocations with the in-memory port manager.
func (e *Engine) ReconcileDaemonContainers(ctx context.Context) error {
	if e.dockerClient == nil {
		return nil
	}

	containers, err := e.dockerClient.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("failed to list docker containers for reconciliation: %w", err)
	}

	containerMap := make(map[string]types.Container, len(containers))
	var runningPorts []int

	for _, c := range containers {
		containerMap[c.ID] = c
		// Match short ID prefix as well
		if len(c.ID) > 12 {
			containerMap[c.ID[:12]] = c
		}

		if c.State == "running" {
			for _, p := range c.Ports {
				if p.PublicPort > 0 {
					runningPorts = append(runningPorts, int(p.PublicPort))
				}
			}
		}
	}

	// 1. Sync port allocations with active running containers
	if e.portManager != nil {
		e.portManager.ReconcileUsedPorts(runningPorts)
		slog.Info("reconciled host port manager with running containers", "active_ports_count", len(runningPorts))
	}

	// Track IDs of containers that are legitimately active in the database
	legitimateContainers := make(map[string]bool)

	// 2. Reconcile active multi-service configurations
	if e.serviceService != nil {
		services, err := e.serviceService.ListActiveServices(ctx)
		if err != nil {
			slog.Warn("reconciliation: failed to list active services", "error", err)
		} else {
			for _, svc := range services {
				if svc.ContainerID == nil || *svc.ContainerID == "" {
					if svc.Status == models.ServiceStatusRunning {
						_ = e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusStopped, nil, nil, nil)
						slog.Info("reconciliation: service has no container, marked stopped", "service_id", svc.ID)
					}
					continue
				}

				cID := *svc.ContainerID
				dockCont, exists := containerMap[cID]
				if !exists {
					// Container disappeared while offline
					_ = e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusFailed, nil, nil, nil)
					if svc.CurrentServiceDeploymentID != nil {
						_ = e.deploymentService.FailServiceDeployment(ctx, *svc.CurrentServiceDeploymentID, "Container disappeared from Docker daemon while backend was offline")
					}
					slog.Warn("reconciliation: service container missing, marked failed", "service_id", svc.ID, "container_id", cID)
				} else if dockCont.State != "running" {
					// Container is dead or exited
					_ = e.serviceService.UpdateServiceStatus(ctx, svc.ID, models.ServiceStatusFailed, nil, nil, nil)
					if svc.CurrentServiceDeploymentID != nil {
						_ = e.deploymentService.FailServiceDeployment(ctx, *svc.CurrentServiceDeploymentID, fmt.Sprintf("Container stopped with state '%s' while backend was offline", dockCont.State))
					}
					slog.Warn("reconciliation: service container exited, marked failed", "service_id", svc.ID, "state", dockCont.State)
				} else {
					// Legitimately running
					legitimateContainers[dockCont.ID] = true
					if len(dockCont.ID) > 12 {
						legitimateContainers[dockCont.ID[:12]] = true
					}
				}
			}
		}
	}

	// 3. Reconcile active legacy single-container deployments
	if e.deploymentService != nil {
		deployments, err := e.deploymentService.ListActiveDeployments(ctx)
		if err != nil {
			slog.Warn("reconciliation: failed to list active deployments", "error", err)
		} else {
			for _, d := range deployments {
				if d.ContainerID == nil || *d.ContainerID == "" {
					continue
				}
				cID := *d.ContainerID
				dockCont, exists := containerMap[cID]
				if !exists || dockCont.State != "running" {
					stateMsg := "missing"
					if exists {
						stateMsg = dockCont.State
					}
					reason := fmt.Sprintf("Container %s while backend was offline", stateMsg)
					_ = e.deploymentService.UpdateDeploymentStatus(ctx, d.ID, models.DeployStatusFailed, &reason)
					if e.projectService != nil {
						_ = e.projectService.UpdateProjectStatus(ctx, d.ProjectID, models.ProjectStatusStopped)
					}
					slog.Warn("reconciliation: legacy deployment container dead/missing", "deployment_id", d.ID, "container_id", cID)
				} else {
					legitimateContainers[dockCont.ID] = true
					if len(dockCont.ID) > 12 {
						legitimateContainers[dockCont.ID[:12]] = true
					}
				}
			}
		}
	}

	// 4. Clean up orphaned ForgeLAB application containers
	orphansCleaned := 0
	stopTimeout := 5
	for _, c := range containers {
		// NEVER touch Docker Compose infrastructure containers (backend, frontend, postgres, redis, migrate)!
		if _, isCompose := c.Labels["com.docker.compose.project"]; isCompose {
			continue
		}
		if _, isComposeService := c.Labels["com.docker.compose.service"]; isComposeService {
			continue
		}

		// Only consider application containers provisioned by ForgeLAB engine
		_, hasProjectLabel := c.Labels["forgelab.project_id"]
		_, hasDeploymentLabel := c.Labels["forgelab.deployment_id"]
		_, hasServiceDeploymentLabel := c.Labels["forgelab.service_deployment_id"]

		if !hasProjectLabel && !hasDeploymentLabel && !hasServiceDeploymentLabel {
			continue
		}

		if !legitimateContainers[c.ID] {
			cShort := c.ID
			if len(cShort) > 12 {
				cShort = cShort[:12]
			}
			slog.Info("reconciliation: removing orphaned container", "container_id", cShort, "labels", c.Labels)
			_ = e.dockerClient.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &stopTimeout})
			_ = e.dockerClient.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
			orphansCleaned++
		}
	}

	if orphansCleaned > 0 {
		slog.Info("reconciliation completed: cleaned up orphaned containers", "count", orphansCleaned)
	}

	return nil
}

// PruneDanglingResources removes expired build workspaces and dangling Docker images to bound storage growth.
func (e *Engine) PruneDanglingResources(ctx context.Context, maxBuildAge time.Duration) error {
	if maxBuildAge <= 0 {
		maxBuildAge = 2 * time.Hour
	}

	var pruneErrs []error

	// 1. Prune old build directories in workDir
	if e.workDir != "" {
		entries, err := os.ReadDir(e.workDir)
		if err == nil {
			now := time.Now()
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				info, err := entry.Info()
				if err != nil {
					continue
				}
				if now.Sub(info.ModTime()) > maxBuildAge {
					dirPath := filepath.Join(e.workDir, entry.Name())
					if err := os.RemoveAll(dirPath); err != nil {
						slog.Warn("failed to remove expired build directory", "path", dirPath, "error", err)
						pruneErrs = append(pruneErrs, fmt.Errorf("failed to remove build dir %s: %w", dirPath, err))
					} else {
						slog.Info("pruned expired build directory", "path", dirPath, "age", now.Sub(info.ModTime()).String())
					}
				}
			}
		} else if !os.IsNotExist(err) {
			slog.Warn("failed to read build workDir for pruning", "path", e.workDir, "error", err)
			pruneErrs = append(pruneErrs, fmt.Errorf("failed to read workDir %s: %w", e.workDir, err))
		}
	}

	// 2. Prune dangling Docker images with exclusive maintenance synchronization
	if e.dockerClient != nil {
		if err := e.pruneDanglingImages(ctx); err != nil {
			pruneErrs = append(pruneErrs, err)
		}
	}

	if len(pruneErrs) > 0 {
		return errors.Join(pruneErrs...)
	}
	return nil
}

// pruneDanglingImages acquires the exclusive buildMaintenanceMu lock to ensure no Docker image builds
// are active or can start while dangling Docker images are being pruned.
func (e *Engine) pruneDanglingImages(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if !e.buildMaintenanceMu.TryLock() {
		slog.Info("docker image prune waiting for active builds")
		e.buildMaintenanceMu.Lock()
	}
	defer e.buildMaintenanceMu.Unlock()

	slog.Info("docker image prune started")
	pruneFilters := filters.NewArgs()
	pruneFilters.Add("dangling", "true")
	if e.pruneFilterLabel != "" {
		pruneFilters.Add("label", e.pruneFilterLabel)
	}

	report, err := e.dockerClient.ImagesPrune(ctx, pruneFilters)
	if err != nil {
		slog.Warn("failed to prune dangling docker images", "error", err)
		return fmt.Errorf("docker image prune failed: %w", err)
	}

	slog.Info("docker image prune completed", "deleted_count", len(report.ImagesDeleted), "space_reclaimed_bytes", report.SpaceReclaimed)
	return nil
}

// buildImage builds a Docker image from the given tar archive and options with proper resource lifecycle:
// 1. Creates a 15-minute build context.
// 2. Invokes ImageBuild with the active build context while tarArchive is open.
// 3. If ImageBuild fails, returns error with structured cleanup.
// 4. Reads the entire build response body via parseDockerStream while build context is active.
// 5. Closes buildResponse.Body.
// 6. Closes the source tarArchive.
// 7. Cancels build context.
// Defer-based cleanup guarantees response body, source stream, and build context
// are closed/cancelled exactly once across all success and error paths.
func (e *Engine) verifyDockerDaemon(ctx context.Context) error {
	if e.dockerClient == nil {
		return nil
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ping, err := e.dockerClient.Ping(pingCtx)
	if err != nil {
		return err
	}
	e.setCachedBuilderVersion(ping.BuilderVersion)
	return nil
}

func (e *Engine) buildImage(
	ctx context.Context,
	tarArchive io.ReadCloser,
	options types.ImageBuildOptions,
	onLogLine func(string),
) error {
	var currentArchive io.ReadCloser = tarArchive
	if currentArchive != nil {
		defer func() {
			_ = currentArchive.Close()
		}()
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	// 1. Acquire shared build-maintenance lock (read side).
	// Prevents Docker image pruning from running while any ImageBuild is active.
	if !e.buildMaintenanceMu.TryRLock() {
		slog.Info("docker build waiting for build-maintenance access")
		e.buildMaintenanceMu.RLock()
	}
	defer e.buildMaintenanceMu.RUnlock()

	// 2. Acquire Docker build slot under configurable concurrency limit (BuildSemaphore)
	if onLogLine != nil {
		onLogLine("Waiting for Docker build slot...")
	}
	releaseSlot, err := e.acquireBuildSlot(ctx)
	if err != nil {
		return fmt.Errorf("Docker build concurrency limit wait cancelled: %w", err)
	}
	defer releaseSlot()
	if onLogLine != nil {
		onLogLine("Docker build slot acquired.")
	}

	// 3. Stage and validate build context archive onto disk
	var validatedArchiveFile *os.File
	var stagedCleanup string

	if file, ok := currentArchive.(*os.File); ok {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("failed to rewind build context file: %w", err)
		}
		if err := tararchive.ValidateTarArchive(file, options.Dockerfile); err != nil {
			return fmt.Errorf("Build context validation failed: %w", err)
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("failed to rewind build context file after validation: %w", err)
		}
		validatedArchiveFile = file
	} else {
		if onLogLine != nil {
			onLogLine("Receiving and validating build context archive...")
		}
		stagedFile, err := tararchive.StageAndValidateArchiveStream(ctx, e.workDir, currentArchive, options.Dockerfile)
		if err != nil {
			if tracker, ok := currentArchive.(TarStreamTracker); ok {
				if streamErr := tracker.StreamError(); streamErr != nil {
					return fmt.Errorf("Build context generation failed: %w", streamErr)
				}
			}
			return fmt.Errorf("Build context generation failed: %w", err)
		}
		validatedArchiveFile = stagedFile
		stagedCleanup = stagedFile.Name()
		defer func() {
			_ = stagedFile.Close()
			if stagedCleanup != "" {
				_ = os.Remove(stagedCleanup)
			}
		}()
	}

	// 4. Inspect Dockerfile and resolve builder capability
	var dfContent []byte
	dfContent, _ = extractDockerfileFromTar(validatedArchiveFile, options.Dockerfile)
	_, _ = validatedArchiveFile.Seek(0, io.SeekStart)

	requiresBuildKit, _ := DetectDockerfileRequiresBuildKit(dfContent)
	_, hasCachedVer := e.getCachedBuilderVersion()

	if requiresBuildKit || hasCachedVer || options.Version != "" || (len(dfContent) > 0 && e.dockerClient != nil) || options.Version == "" {
		resolvedOptions, builderErr := e.resolveBuilder(ctx, dfContent, options, nil)
		if builderErr != nil {
			return builderErr
		}
		options = resolvedOptions
	}

	slog.Info("docker image build started", "tags", options.Tags, "builder_version", options.Version)

	buildCtx, buildCancel := context.WithTimeout(ctx, 15*time.Minute)
	defer buildCancel()

	if onLogLine != nil {
		onLogLine("Docker build started.")
	}

	buildResponse, err := e.dockerClient.ImageBuild(buildCtx, validatedArchiveFile, options)
	if err != nil {
		return fmt.Errorf("Docker build failed: %w", err)
	}
	defer buildResponse.Body.Close()

	if parseErr := e.parseDockerStream(buildResponse.Body, onLogLine); parseErr != nil {
		// If BuildKit was used and rejected the tar header, attempt fallback to standard builder
		if options.Version == types.BuilderBuildKit && strings.Contains(parseErr.Error(), "archive/tar: invalid tar header") {
			slog.Warn("buildkit rejected tar build context; retrying with standard builder", "error", parseErr)
			if onLogLine != nil {
				onLogLine("BuildKit rejected tar build context header. Retrying with standard Docker builder...")
			}
			if _, seekErr := validatedArchiveFile.Seek(0, io.SeekStart); seekErr == nil {
				fallbackOpts := options
				fallbackOpts.Version = ""
				retryCtx, retryCancel := context.WithTimeout(ctx, 15*time.Minute)
				defer retryCancel()
				retryResp, retryErr := e.dockerClient.ImageBuild(retryCtx, validatedArchiveFile, fallbackOpts)
				if retryErr == nil {
					defer retryResp.Body.Close()
					if retryParseErr := e.parseDockerStream(retryResp.Body, onLogLine); retryParseErr == nil {
						slog.Info("docker image build completed using fallback standard builder", "tags", fallbackOpts.Tags)
						return nil
					} else {
						return fmt.Errorf("Docker build error: %w", retryParseErr)
					}
				}
			}
		}
		return fmt.Errorf("Docker build error: %w", parseErr)
	}

	slog.Info("docker image build completed", "tags", options.Tags)
	return nil
}

func (e *Engine) parseDockerStream(r io.Reader, onLogLine func(string)) error {
	buf := make([]byte, 4096)
	var rawBuffer bytes.Buffer

	for {
		n, err := r.Read(buf)
		if n > 0 {
			rawBuffer.Write(buf[:n])
			lines := strings.Split(rawBuffer.String(), "\n")
			// Keep incomplete line in buffer
			rawBuffer.Reset()
			rawBuffer.WriteString(lines[len(lines)-1])

			for i := 0; i < len(lines)-1; i++ {
				line := lines[i]
				if strings.TrimSpace(line) == "" {
					continue
				}

				// Docker JSON output parsing
				var jsonMsg struct {
					Stream string `json:"stream"`
					Error  string `json:"error"`
				}
				if jsonErr := json.Unmarshal([]byte(line), &jsonMsg); jsonErr == nil {
					if jsonMsg.Error != "" {
						return errors.New(jsonMsg.Error)
					}
					if jsonMsg.Stream != "" {
						onLogLine(strings.TrimSuffix(jsonMsg.Stream, "\n"))
					}
				} else {
					onLogLine(line)
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func copyDirectory(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		}

		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(linkTarget, targetPath)
		}

		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()

		dstFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer dstFile.Close()

		_, err = io.Copy(dstFile, srcFile)
		return err
	})
}

// resolveAgentBaseURL dynamically finds the local agent URL, handling containerized backend setups
func resolveAgentBaseURL() string {
	return agent.ResolveBaseURL()
}

// fetchRecentContainerLogs retrieves the most recent stdout/stderr lines from a container for health diagnostics.
func (e *Engine) fetchRecentContainerLogs(ctx context.Context, containerID string, tailLines int) string {
	if e.dockerClient == nil {
		return ""
	}
	logCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	reader, err := e.dockerClient.ContainerLogs(logCtx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       fmt.Sprintf("%d", tailLines),
	})
	if err != nil {
		return ""
	}
	defer reader.Close()

	var lines []string
	_ = DemuxDockerStream(logCtx, reader, func(stream string, line string) {
		clean := strings.TrimSpace(line)
		if clean != "" {
			lines = append(lines, fmt.Sprintf("[%s] %s", stream, clean))
		}
	})
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}
	return strings.Join(lines, "\n")
}

// buildContainerExitDiagnostics collects rich diagnostic state from an exited container.
func (e *Engine) buildContainerExitDiagnostics(ctx context.Context, containerID string, state *types.ContainerState) string {
	if state == nil {
		return "state=unknown"
	}
	var parts []string
	parts = append(parts, fmt.Sprintf("exit_code=%d", state.ExitCode))
	parts = append(parts, fmt.Sprintf("status=%s", state.Status))
	parts = append(parts, fmt.Sprintf("oom_killed=%t", state.OOMKilled))
	if state.Error != "" {
		parts = append(parts, fmt.Sprintf("error=%q", state.Error))
	}
	if state.FinishedAt != "" {
		parts = append(parts, fmt.Sprintf("finished_at=%s", state.FinishedAt))
	}
	if state.Health != nil {
		parts = append(parts, fmt.Sprintf("health_status=%s", state.Health.Status))
		if len(state.Health.Log) > 0 {
			var logSnippets []string
			startIdx := 0
			if len(state.Health.Log) > 3 {
				startIdx = len(state.Health.Log) - 3
			}
			for _, entry := range state.Health.Log[startIdx:] {
				cleanOut := strings.TrimSpace(entry.Output)
				if len(cleanOut) > 120 {
					cleanOut = cleanOut[:120] + "..."
				}
				logSnippets = append(logSnippets, fmt.Sprintf("[exit=%d] %s", entry.ExitCode, cleanOut))
			}
			parts = append(parts, fmt.Sprintf("recent_health_logs=[%s]", strings.Join(logSnippets, "; ")))
		}
	}
	recentLogs := e.fetchRecentContainerLogs(ctx, containerID, 15)
	if recentLogs != "" {
		parts = append(parts, fmt.Sprintf("recent_logs=\n%s", recentLogs))
	}
	return strings.Join(parts, ", ")
}

// verifyServiceHealth performs health verification through the correct container/network path
// working seamlessly whether ForgeLAB runs directly on the host or inside Docker Compose.
func (e *Engine) verifyServiceHealth(
	ctx context.Context,
	containerID string,
	internalPort int,
	hostPort *int,
	healthStrategy string,
	healthPath string,
	logFn func(msg string),
	errLogFn func(msg string),
	hcConfigs ...*models.HealthCheckConfig,
) (bool, string) {
	if healthStrategy == models.HealthStrategyNone {
		cJSON, err := e.dockerClient.ContainerInspect(ctx, containerID)
		if err == nil && cJSON.State != nil && cJSON.State.Running {
			return true, "container verified running"
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return false, "health check cancelled"
		}
		return false, "container is not running"
	}

	if healthPath == "" {
		healthPath = "/"
	}

	var hcConfig *models.HealthCheckConfig
	if len(hcConfigs) > 0 && hcConfigs[0] != nil {
		hcConfig = hcConfigs[0]
	}

	// Calculate overall health check budget respecting Compose timing
	expectedBudget := 60 * time.Second
	pollInterval := 1500 * time.Millisecond

	if hcConfig != nil {
		startPeriod := time.Duration(hcConfig.StartPeriodSeconds) * time.Second
		interval := time.Duration(hcConfig.IntervalSeconds) * time.Second
		if interval <= 0 {
			interval = 5 * time.Second
		}
		timeout := time.Duration(hcConfig.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		retries := hcConfig.Retries
		if retries <= 0 {
			retries = 3
		}
		calcBudget := startPeriod + time.Duration(retries)*(interval+timeout) + 15*time.Second
		if calcBudget > expectedBudget {
			expectedBudget = calcBudget
		}
		if interval < pollInterval {
			pollInterval = interval
		}
	}

	httpClient := &http.Client{Timeout: 2 * time.Second}

	startTime := time.Now()

	for attempt := 1; ; attempt++ {
		// 1. Check context cancellation / timeout
		if errors.Is(ctx.Err(), context.Canceled) {
			errLogFn("Health check cancelled: deployment context cancelled")
			return false, "health check cancelled"
		}
		if time.Since(startTime) >= expectedBudget {
			errLogFn(fmt.Sprintf("Health check timed out after %v", expectedBudget))
			return false, fmt.Sprintf("health check timed out after %v", expectedBudget)
		}

		// 2. Inspect container with bounded timeout
		inspectCtx, inspectCancel := context.WithTimeout(ctx, 5*time.Second)
		cJSON, err := e.dockerClient.ContainerInspect(inspectCtx, containerID)
		inspectCancel()
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				errLogFn("Health check cancelled: deployment context cancelled")
				return false, "health check cancelled"
			}
			if time.Since(startTime) >= expectedBudget {
				errLogFn(fmt.Sprintf("Health check timed out after %v", expectedBudget))
				return false, fmt.Sprintf("health check timed out after %v", expectedBudget)
			}
			// Temporary Docker API / inspect error: do NOT report as container exit!
			logFn(fmt.Sprintf("Docker inspect attempt %d returned temporary error (will retry): %v", attempt, err))
			select {
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.Canceled) {
					errLogFn("Health check cancelled: deployment context cancelled")
					return false, "health check cancelled"
				}
				return false, "health check cancelled"
			case <-time.After(pollInterval):
				continue
			}
		}

		// 3. Check if container actually exited
		if cJSON.State == nil || !cJSON.State.Running {
			diag := e.buildContainerExitDiagnostics(ctx, containerID, cJSON.State)
			errLogFn(fmt.Sprintf("Container exited unexpectedly during health check: %s", diag))
			return false, fmt.Sprintf("container exited unexpectedly: %s", diag)
		}

		// Dynamically discover Docker native HealthConfig from cJSON if hcConfig was not supplied
		if hcConfig == nil && cJSON.Config != nil && cJSON.Config.Healthcheck != nil {
			ch := cJSON.Config.Healthcheck
			retries := ch.Retries
			if retries <= 0 {
				retries = 3
			}
			interval := ch.Interval
			if interval <= 0 {
				interval = 5 * time.Second
			}
			timeout := ch.Timeout
			if timeout <= 0 {
				timeout = 5 * time.Second
			}
			calcBudget := ch.StartPeriod + time.Duration(retries)*(interval+timeout) + 15*time.Second
			if calcBudget > expectedBudget {
				expectedBudget = calcBudget
			}
		}

		// 4. Check Docker-native health check
		hasNativeHealth := (cJSON.State.Health != nil) || (healthStrategy == models.HealthStrategyDocker)
		if hasNativeHealth {
			if cJSON.State.Health != nil {
				status := cJSON.State.Health.Status
				if status == "healthy" {
					logFn(fmt.Sprintf("Docker native health check reported healthy on attempt %d.", attempt))
					return true, "healthy"
				}
				if status == "unhealthy" {
					diag := ""
					if len(cJSON.State.Health.Log) > 0 {
						lastEntry := cJSON.State.Health.Log[len(cJSON.State.Health.Log)-1]
						diag = strings.TrimSpace(lastEntry.Output)
					}
					recentLogs := e.fetchRecentContainerLogs(ctx, containerID, 10)
					msg := fmt.Sprintf("Health verification failed: docker health status=unhealthy (attempt %d)", attempt)
					if diag != "" {
						msg = fmt.Sprintf("%s: %s", msg, diag)
					}
					if recentLogs != "" {
						msg = fmt.Sprintf("%s\nRecent container logs:\n%s", msg, recentLogs)
					}
					errLogFn(msg)
					return false, msg
				}
				// status == "starting" (or initializing): KEEP WAITING
				if attempt == 1 || attempt%5 == 0 {
					logFn(fmt.Sprintf("Docker native health check status is '%s' for %s (attempt %d)... waiting", status, containerID[:12], attempt))
				}
			} else {
				if attempt == 1 || attempt%5 == 0 {
					logFn(fmt.Sprintf("Waiting for Docker native health check monitor to initialize for %s (attempt %d)...", containerID[:12], attempt))
				}
			}

			// For Docker-native health check services, DO NOT fall through to TCP/HTTP!
			select {
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.Canceled) {
					errLogFn("Health check cancelled: deployment context cancelled")
					return false, "health check cancelled"
				}
				return false, "health check cancelled"
			case <-time.After(pollInterval):
				if time.Since(startTime) >= expectedBudget {
					msg := fmt.Sprintf("Health verification failed: docker health check timed out after %v while waiting to become healthy", expectedBudget)
					errLogFn(msg)
					return false, msg
				}
				continue
			}
		}

		// 5. Fallback TCP / HTTP verification for services WITHOUT a Docker-native health check
		var targets []string
		if cJSON.NetworkSettings != nil {
			for _, netSettings := range cJSON.NetworkSettings.Networks {
				if netSettings != nil && netSettings.IPAddress != "" && internalPort > 0 {
					targets = append(targets, fmt.Sprintf("%s:%d", netSettings.IPAddress, internalPort))
				}
			}
			if cJSON.NetworkSettings.IPAddress != "" && internalPort > 0 {
				targets = append(targets, fmt.Sprintf("%s:%d", cJSON.NetworkSettings.IPAddress, internalPort))
			}
		}

		if hostPort != nil && *hostPort > 0 {
			targets = append(targets, fmt.Sprintf("127.0.0.1:%d", *hostPort))
			targets = append(targets, fmt.Sprintf("localhost:%d", *hostPort))
			if os.Getenv("FORGELAB_IN_DOCKER") != "" || os.Getenv("DOCKER_CONTAINER") != "" {
				targets = append(targets, fmt.Sprintf("host.docker.internal:%d", *hostPort))
				if cJSON.NetworkSettings != nil && cJSON.NetworkSettings.Gateway != "" {
					targets = append(targets, fmt.Sprintf("%s:%d", cJSON.NetworkSettings.Gateway, *hostPort))
				}
			}
		}

		for _, target := range targets {
			if healthStrategy != models.HealthStrategyTCP {
				reqURL := fmt.Sprintf("http://%s%s", target, healthPath)
				reqCtx, reqCancel := context.WithTimeout(ctx, 2*time.Second)
				req, reqErr := http.NewRequestWithContext(reqCtx, http.MethodGet, reqURL, nil)
				if reqErr == nil {
					res, doErr := httpClient.Do(req)
					if doErr == nil {
						res.Body.Close()
						reqCancel()
						if res.StatusCode < 500 {
							logFn(fmt.Sprintf("Health check passed via %s (HTTP %d) on attempt %d.", target, res.StatusCode, attempt))
							return true, "healthy"
						}
					} else {
						reqCancel()
					}
				} else {
					reqCancel()
				}
			}

			conn, tcpErr := net.DialTimeout("tcp", target, 1*time.Second)
			if tcpErr == nil {
				conn.Close()
				logFn(fmt.Sprintf("TCP health check passed via %s on attempt %d.", target, attempt))
				return true, "healthy"
			}
		}

		// Internal service without public port and NO native health check
		if hostPort == nil && attempt >= 4 && cJSON.State.Running && cJSON.State.Health == nil {
			logFn(fmt.Sprintf("Internal service verified running stably (attempt %d).", attempt))
			return true, "healthy"
		}

		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				errLogFn("Health check cancelled: deployment context cancelled")
				return false, "health check cancelled"
			}
			errLogFn(fmt.Sprintf("Health verification timed out after %v", expectedBudget))
			return false, fmt.Sprintf("health check timed out after %v", expectedBudget)
		case <-time.After(pollInterval):
			if time.Since(startTime) >= expectedBudget {
				errLogFn(fmt.Sprintf("Health verification timed out after %v", expectedBudget))
				return false, fmt.Sprintf("health check timed out after %v", expectedBudget)
			}
			continue
		}
	}
}

// buildServiceDeploymentTiers partitions service deployments into dependency execution tiers (DAG)
func buildServiceDeploymentTiers(svcDeploys []*models.ServiceDeployment) [][]*models.ServiceDeployment {
	nameToDeploy := make(map[string]*models.ServiceDeployment)
	for _, sd := range svcDeploys {
		nameToDeploy[sd.ServiceName] = sd
	}

	deployed := make(map[string]bool)
	remaining := make(map[string]*models.ServiceDeployment)
	for _, sd := range svcDeploys {
		remaining[sd.ServiceName] = sd
	}

	var tiers [][]*models.ServiceDeployment
	for len(remaining) > 0 {
		var currentTier []*models.ServiceDeployment
		for _, sd := range remaining {
			depsSatisfied := true
			for _, dep := range sd.DependsOn {
				if _, exists := nameToDeploy[dep]; exists && !deployed[dep] {
					depsSatisfied = false
					break
				}
			}
			if depsSatisfied {
				currentTier = append(currentTier, sd)
			}
		}

		if len(currentTier) == 0 {
			// Circular dependency or unresolvable: group all remaining into a final tier
			var fallback []*models.ServiceDeployment
			for _, sd := range remaining {
				fallback = append(fallback, sd)
			}
			tiers = append(tiers, fallback)
			break
		}

		for _, sd := range currentTier {
			deployed[sd.ServiceName] = true
			delete(remaining, sd.ServiceName)
		}
		tiers = append(tiers, currentTier)
	}
	return tiers
}
