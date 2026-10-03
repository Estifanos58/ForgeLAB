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
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/detector"
	"github.com/forgelab/backend/internal/logging"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/network"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
	ws "github.com/forgelab/backend/internal/websocket"
)

type Engine struct {
	dockerClient       *client.Client
	projectService     *services.ProjectService
	deploymentService  *services.DeploymentService
	secretService      *services.SecretService
	sourceService      *services.SourceService
	githubService      *services.GitHubService
	serviceService     *services.ServiceService
	portManager        *network.PortManager
	pathValidator      *security.PathValidator
	wsHub              *ws.Hub
	workDir             string
	localBuildMode      string
	maxConcurrentBuilds int
	buildSemaphore      chan struct{}
	activeLogCollectors sync.Map // map[string]context.CancelFunc — tracks running log collector goroutines
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
		dockerClient:      dockerClient,
		projectService:    projectService,
		deploymentService: deploymentService,
		secretService:     secretService,
		sourceService:     sourceService,
		githubService:     githubService,
		serviceService:    services.NewServiceService(nil),
		portManager:       portManager,
		pathValidator:       pathValidator,
		wsHub:               wsHub,
		workDir:             workDir,
		localBuildMode:      "direct",
		maxConcurrentBuilds: 4,
		buildSemaphore:      make(chan struct{}, 4),
	}
}

// SetMaxConcurrentBuilds configures the global Docker build concurrency limit.
func (e *Engine) SetMaxConcurrentBuilds(n int) {
	if n > 0 {
		e.maxConcurrentBuilds = n
		e.buildSemaphore = make(chan struct{}, n)
	}
}

// GetMaxConcurrentBuilds returns the current global Docker build concurrency limit.
func (e *Engine) GetMaxConcurrentBuilds() int {
	return e.maxConcurrentBuilds
}

// acquireBuildSlot acquires a slot under the global Docker build concurrency limit.
func (e *Engine) acquireBuildSlot(ctx context.Context) (func(), error) {
	if e.buildSemaphore == nil {
		return func() {}, nil
	}
	select {
	case e.buildSemaphore <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() {
				<-e.buildSemaphore
			})
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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
func (e *Engine) resolveSourceDirectory(ctx context.Context, project *models.Project, execID string, emitLog func(phase, stream, message string)) (buildSourceDir string, cleanupDir string, err error) {
	if project.SourceType == models.SourceTypeGitHub {
		snapshotDir := filepath.Join(e.workDir, execID)
		_ = os.MkdirAll(snapshotDir, 0755)
		cleanupDir = snapshotDir
		buildSourceDir = snapshotDir

		emitLog(models.LogPhaseSource, models.LogStreamSystem, fmt.Sprintf("Acquiring GitHub repository archive for '%s' (branch: %s)...", project.SourceReference, project.Branch))
		parts := strings.Split(project.SourceReference, "/")
		if len(parts) != 2 {
			return "", cleanupDir, fmt.Errorf("invalid GitHub repository reference '%s'. Expected format 'owner/repo'", project.SourceReference)
		}
		if e.githubService == nil {
			return "", cleanupDir, errors.New("GitHub integration service is not available")
		}
		if err := e.githubService.AcquireRepoTarball(ctx, project.OwnerID, parts[0], parts[1], project.Branch, snapshotDir); err != nil {
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

		if e.localBuildMode == "snapshot" {
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
	releaseSlot, err := e.acquireBuildSlot(ctx)
	if err != nil {
		return fmt.Errorf("concurrency limit wait cancelled: %w", err)
	}
	defer releaseSlot()

	serviceDeploy, err := e.deploymentService.GetServiceDeployment(ctx, serviceDeploymentID)
	if err != nil {
		return fmt.Errorf("failed to get service deployment %s: %w", serviceDeploymentID, err)
	}

	service, err := e.serviceService.GetService(ctx, serviceDeploy.ServiceID)
	if err != nil {
		return fmt.Errorf("failed to get service %s: %w", serviceDeploy.ServiceID, err)
	}

	project, err := e.getProjectByID(ctx, service.ProjectID)
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
	var buildEnvMap map[string]string

	if len(serviceDeploy.EnvSnapshot) > 0 && e.secretService != nil {
		runtimeEnvMap, _, _ = e.secretService.GetEnvMapFromSnapshot(serviceDeploy.EnvSnapshot, models.EnvScopeRuntime)
		buildEnvMap, _, _ = e.secretService.GetEnvMapFromSnapshot(serviceDeploy.EnvSnapshot, models.EnvScopeBuild)
		_, allSecrets, _ := e.secretService.GetEnvMapFromSnapshot(serviceDeploy.EnvSnapshot, "")
		if len(allSecrets) > 0 {
			redactor.SetSecrets(allSecrets)
		}
	} else if e.secretService != nil {
		runtimeEnvMap, _, _ = e.secretService.GetDecryptedEnvMap(ctx, project.ID, &service.ID, models.EnvScopeRuntime)
		buildEnvMap, _, _ = e.secretService.GetDecryptedEnvMap(ctx, project.ID, &service.ID, models.EnvScopeBuild)
		_, secrets, err := e.secretService.GetDecryptedEnvMap(ctx, project.ID, &service.ID, "")
		if err == nil {
			redactor.SetSecrets(secrets)
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
			if inspect, _, err := e.dockerClient.ImageInspectWithRaw(ctx, candidateImage); err == nil {
				reusingExistingImage = true
				runImage = candidateImage
				idShort := inspect.ID
				if len(idShort) > 12 {
					idShort = idShort[:12]
				}
				emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Execution path: reusing existing immutable image '%s' (image ID: %s). Skipping source cloning and image build.", candidateImage, idShort))
				_ = e.dockerClient.ImageTag(ctx, candidateImage, svcTag)
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

	if !reusingExistingImage {
		// 1. Source acquisition & resolution
		emitStatus(models.DeployStatusCloning, nil, nil)
		buildSourceDir, cleanupDir, err := e.resolveSourceDirectory(ctx, project, serviceDeploy.ID.String(), emitLog)
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
		relDockerPath := "Dockerfile"
		var tarArchive io.ReadCloser

		if project.SourceType == models.SourceTypeLocalAgent {
			baseURL := resolveAgentBaseURL()
			agentToken := ""
			if e.sourceService != nil {
				if srcUUID, err := uuid.Parse(project.SourceReference); err == nil {
					tok, err := e.sourceService.GetDecryptedAgentToken(ctx, project.OwnerID, srcUUID)
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
			agentURL := fmt.Sprintf("%s/api/agent/sources/%s/stream-context?service_path=%s&runtime=%s&port=%d&start_cmd=%s",
				baseURL,
				project.SourceReference,
				url.QueryEscape(service.SourcePath),
				url.QueryEscape(service.RuntimeType),
				service.InternalPort,
				url.QueryEscape(service.StartCommand),
			)
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, agentURL, nil)
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
			}
		} else {
			svcContextDir := filepath.Join(buildSourceDir, service.SourcePath)
			if _, err := os.Stat(svcContextDir); err != nil {
				svcContextDir = buildSourceDir
			}

			var virtualFiles map[string][]byte
			if service.BuildStrategy == models.BuildStrategyAuto {
				intPort := service.InternalPort
				if intPort <= 0 {
					intPort = 8080
				}
				generatedContent := detector.GenerateDockerfile(service.RuntimeType, intPort, service.StartCommand)
				relDockerPath = "Dockerfile.forgelab"
				virtualFiles = map[string][]byte{
					"Dockerfile.forgelab": []byte(generatedContent),
				}
			} else {
				if service.DockerfilePath != "" {
					relDockerPath = service.DockerfilePath
				} else {
					relDockerPath = "Dockerfile"
				}
			}

			matcher, _ := LoadDockerignore(svcContextDir)
			if matcher == nil {
				matcher = NewDockerignoreMatcher(DefaultIgnorePatterns)
			}

			tarArchive = StreamBuildContext(ctx, TarStreamerOptions{
				BuildContextDir: svcContextDir,
				Matcher:         matcher,
				VirtualFiles:    virtualFiles,
				EmitLog: func(phase, stream, msg string) {
					emitLog(phase, stream, msg)
				},
			})
		}

		emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Building Docker image '%s'...", svcTag))

		buildArgs := make(map[string]*string)
		for k, v := range buildEnvMap {
			val := v
			buildArgs[k] = &val
		}
		if len(buildArgs) > 0 {
			emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Injected %d build-time arguments (runtime-only secrets safely excluded).", len(buildArgs)))
		}

		buildCtx, buildCancel := context.WithTimeout(ctx, 15*time.Minute)
		buildResponse, err := e.dockerClient.ImageBuild(buildCtx, tarArchive, types.ImageBuildOptions{
			Tags:       []string{svcTag},
			Dockerfile: relDockerPath,
			BuildArgs:  buildArgs,
			Remove:     true,
		})
		tarArchive.Close()
		buildCancel()
		if err != nil {
			reason := fmt.Sprintf("Docker build failed: %v", err)
			emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return errors.New(reason)
		}

		if err := e.parseDockerStream(buildResponse.Body, func(msg string) {
			emitLog(models.LogPhaseBuild, models.LogStreamStdout, msg)
		}); err != nil {
			buildResponse.Body.Close()
			reason := fmt.Sprintf("Docker build error: %v", err)
			emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return errors.New(reason)
		}
		buildResponse.Body.Close()
		emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Docker image '%s' built successfully.", svcTag))

		// Inspect image to record immutable digest for rollback / reproducibility
		if inspect, _, err := e.dockerClient.ImageInspectWithRaw(ctx, svcTag); err == nil {
			var digest string
			if len(inspect.RepoDigests) > 0 {
				digest = inspect.RepoDigests[0]
			} else if inspect.ID != "" {
				digest = inspect.ID
			}
			if digest != "" && e.deploymentService != nil {
				_ = e.deploymentService.UpdateServiceDeploymentImageDigest(ctx, serviceDeploy.ID, digest)
			}
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

	allProjectServices, _ := e.serviceService.ListServices(ctx, project.ID)
	if service.PublicExposed || service.Role == models.RoleFrontend || len(allProjectServices) <= 1 {
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

	networkName := fmt.Sprintf("forgelab-net-%s", project.ID.String())
	_, netErr := e.dockerClient.NetworkInspect(ctx, networkName, dockernetwork.InspectOptions{})
	if netErr != nil {
		_, _ = e.dockerClient.NetworkCreate(ctx, networkName, dockernetwork.CreateOptions{
			Driver: "bridge",
		})
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

	cpuNano := int64(targetCpuMillicores) * 1_000_000 // millicores -> nanocores
	memBytes := int64(targetMemoryMB) * 1024 * 1024    // MB -> bytes
	pidsLimit := int64(targetPidsLimit)
	if cpuNano <= 0 {
		cpuNano = 1_000_000_000
	}
	if memBytes <= 0 {
		memBytes = 1024 * 1024 * 1024
	}
	if pidsLimit <= 0 {
		pidsLimit = 256
	}
	hostConfig := &container.HostConfig{
		PortBindings: portBindings,
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
		Resources: container.Resources{
			Memory:    memBytes,
			NanoCPUs:  cpuNano,
			PidsLimit: &pidsLimit,
		},
	}

	netConfig := &dockernetwork.NetworkingConfig{
		EndpointsConfig: map[string]*dockernetwork.EndpointSettings{
			networkName: {
				Aliases: []string{service.Name},
			},
		},
	}

	_ = e.dockerClient.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true})

	resp, err := e.dockerClient.ContainerCreate(ctx, containerConfig, hostConfig, netConfig, nil, containerName)
	if err != nil {
		if hostPort != nil {
			e.portManager.ReleasePort(*hostPort)
		}
		reason := fmt.Sprintf("Failed to create container for service '%s': %v", service.Name, err)
		emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
		emitStatus(models.DeployStatusFailed, nil, &reason)
		return errors.New(reason)
	}

	containerID := resp.ID
	if err := e.dockerClient.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		_ = e.dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
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
	e.startRuntimeLogCollector(ctx, containerID, serviceDeploy.ID, service.ID, project.ID, redactor, emitLog)

	// 4. Health Checking
	emitStatus(models.DeployStatusHealthChecking, hostPort, nil)
	emitLog(models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("Verifying health for service '%s'...", service.Name))

	healthStrat := service.HealthStrategy
	if healthStrat == "" {
		healthStrat = models.HealthStrategyAuto
	}
	healthPath := "/"
	if service.HealthCheckPath != nil && *service.HealthCheckPath != "" {
		healthPath = *service.HealthCheckPath
	}

	svcHealthy, _ := e.verifyServiceHealth(
		ctx,
		containerID,
		intPort,
		hostPort,
		healthStrat,
		healthPath,
		func(msg string) { emitLog(models.LogPhaseHealth, models.LogStreamSystem, msg) },
		func(msg string) { emitLog(models.LogPhaseHealth, models.LogStreamStderr, msg) },
	)

	if !svcHealthy {
		// Health check failed - ENFORCE SAFETY INVARIANT:
		// Destroy newly spawned broken container and preserve previously healthy container intact!
		e.StopLogCollector(serviceDeploy.ID)
		_ = e.dockerClient.ContainerStop(ctx, containerID, container.StopOptions{})
		_ = e.dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
		if hostPort != nil {
			e.portManager.ReleasePort(*hostPort)
		}

		reason := fmt.Sprintf("Health check failed for service '%s' after retries", service.Name)
		emitLog(models.LogPhaseHealth, models.LogStreamStderr, reason)
		if hasPreviousHealthy {
			emitLog(models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("SAFETY INVARIANT ENFORCED: New deployment for '%s' failed health check. Previous healthy container remains active!", service.Name))
		}
		emitStatus(models.DeployStatusFailed, nil, &reason)
		return errors.New(reason)
	}

	// 5. Promote service
	err = e.serviceService.PromoteServiceDeployment(ctx, service.ID, serviceDeploy.ID, containerID, svcTag, hostPort)
	if err != nil {
		// Promotion failed - ENFORCE SAFETY INVARIANT:
		// The new container must not replace the previous current deployment,
		// the new deployment must become failed, and the old container must remain intact.
		e.StopLogCollector(serviceDeploy.ID)
		_ = e.dockerClient.ContainerStop(ctx, containerID, container.StopOptions{})
		_ = e.dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
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

	_ = e.deploymentService.UpdateServiceDeploymentContainer(ctx, serviceDeploy.ID, containerID, hostPort)
	_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, serviceDeploy.ID, models.DeployStatusRunning, nil)
	emitStatus(models.DeployStatusRunning, hostPort, nil)

	// 6. Cleanup previous container for this service if different
	if previousContainerID != nil && *previousContainerID != "" && *previousContainerID != containerID {
		emitLog(models.LogPhaseRuntime, models.LogStreamSystem, fmt.Sprintf("Stopping previous container %s for service '%s'...", (*previousContainerID)[:12], service.Name))
		if previousDeploymentID != nil {
			e.StopLogCollector(*previousDeploymentID)
		}
		_ = e.dockerClient.ContainerStop(ctx, *previousContainerID, container.StopOptions{})
		_ = e.dockerClient.ContainerRemove(ctx, *previousContainerID, container.RemoveOptions{Force: true})
	}

	// Update project port if this service is public or frontend
	if hostPort != nil && (service.Role == models.RoleFrontend || service.PublicExposed) {
		_, _ = e.projectService.UpdateProjectPort(ctx, project.ID, *hostPort)
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

	project, err := e.getProjectByID(ctx, deployment.ProjectID)
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

	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		allHealthy = true
		anyHealthy = false
		errList    []string
	)

	maxConcurrent := 4
	sem := make(chan struct{}, maxConcurrent)

	for _, sd := range svcDeploys {
		wg.Add(1)
		go func(sdItem *models.ServiceDeployment) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if execErr := e.ExecuteServiceDeployment(ctx, sdItem.ID); execErr != nil {
				mu.Lock()
				allHealthy = false
				errList = append(errList, fmt.Sprintf("%s: %v", sdItem.ServiceName, execErr))
				mu.Unlock()
			} else {
				mu.Lock()
				anyHealthy = true
				mu.Unlock()
			}
		}(sd)
	}
	wg.Wait()

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
	releaseSlot, err := e.acquireBuildSlot(ctx)
	if err != nil {
		return fmt.Errorf("concurrency limit wait cancelled: %w", err)
	}
	defer releaseSlot()

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

	if e.secretService != nil {
		_, secrets, err := e.secretService.GetDecryptedEnvMap(ctx, project.ID, nil, "")
		if err == nil {
			redactor.SetSecrets(secrets)
		}
	}

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
	emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Preparing build for image '%s'...", *deployment.ImageTag))

	buildContextDir := filepath.Join(buildSourceDir, project.BuildContext)
	if _, err := os.Stat(buildContextDir); err != nil {
		reason := fmt.Sprintf("Build context directory '%s' does not exist in source", project.BuildContext)
		emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
		updateStatus(models.DeployStatusFailed, &reason)
		return errors.New(reason)
	}

	buildStrategy := deployment.BuildStrategy
	if buildStrategy == "" {
		buildStrategy = project.BuildStrategy
	}
	if buildStrategy == "" {
		buildStrategy = models.BuildStrategyAuto
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

	relDockerPath := dockerfilePath
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

	matcher, _ := LoadDockerignore(buildContextDir)
	if matcher == nil {
		matcher = NewDockerignoreMatcher(DefaultIgnorePatterns)
	}

	tarStream := StreamBuildContext(ctx, TarStreamerOptions{
		BuildContextDir: buildContextDir,
		Matcher:         matcher,
		VirtualFiles:    virtualFiles,
		EmitLog: func(phase, stream, msg string) {
			emitLog(phase, stream, msg)
		},
	})

	buildCtx, buildCancel := context.WithTimeout(ctx, 15*time.Minute)
	defer buildCancel()

	buildArgs := make(map[string]*string)
	if e.secretService != nil {
		buildEnv, _, _ := e.secretService.GetDecryptedEnvMap(ctx, project.ID, nil, models.EnvScopeBuild)
		for k, v := range buildEnv {
			val := v
			buildArgs[k] = &val
		}
	}

	buildResponse, err := e.dockerClient.ImageBuild(buildCtx, tarStream, types.ImageBuildOptions{
		Tags:       []string{*deployment.ImageTag},
		Dockerfile: relDockerPath,
		BuildArgs:  buildArgs,
		Remove:     true,
	})
	if err != nil {
		tarStream.Close()
		reason := fmt.Sprintf("Docker build failed: %v", err)
		emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
		updateStatus(models.DeployStatusFailed, &reason)
		return errors.New(reason)
	}

	if err := e.parseDockerStream(buildResponse.Body, func(msg string) {
		emitLog(models.LogPhaseBuild, models.LogStreamStdout, msg)
	}); err != nil {
		buildResponse.Body.Close()
		tarStream.Close()
		reason := fmt.Sprintf("Build error: %v", err)
		emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
		updateStatus(models.DeployStatusFailed, &reason)
		return errors.New(reason)
	}
	buildResponse.Body.Close()
	tarStream.Close()

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
		Image: *deployment.ImageTag,
		Env:   envSlice,
		ExposedPorts: nat.PortSet{
			nat.Port(targetPortStr): struct{}{},
		},
		Labels: map[string]string{
			"forgelab.project_id":    project.ID.String(),
			"forgelab.deployment_id": deployment.ID.String(),
		},
	}

	pidsLimit := int64(256)
	hostConfig := &container.HostConfig{
		PortBindings: nat.PortMap{
			nat.Port(targetPortStr): []nat.PortBinding{
				{
					HostIP:   "0.0.0.0",
					HostPort: fmt.Sprintf("%d", allocatedPort),
				},
			},
		},
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
		Resources: container.Resources{
			Memory:    1024 * 1024 * 1024,
			NanoCPUs:  1_000_000_000,
			PidsLimit: &pidsLimit,
		},
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

	healthy, _ := e.verifyServiceHealth(
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
		_ = e.dockerClient.ContainerStop(ctx, containerID, container.StopOptions{})
		_ = e.dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
		e.portManager.ReleasePort(allocatedPort)
		reason := "Health check failed after 10 retries"
		emitLog(models.LogPhaseHealth, models.LogStreamStderr, reason)
		updateStatus(models.DeployStatusFailed, &reason)
		emitLog(models.LogPhaseHealth, models.LogStreamSystem, "SAFETY INVARIANT ENFORCED: New deployment failed health check. Previous healthy release remains active!")
		return errors.New(reason)
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
	deployments, err := e.deploymentService.ListDeployments(ctx, projectID)
	if err != nil {
		return
	}

	for _, d := range deployments {
		if d.ContainerID != nil && *d.ContainerID != "" {
			_ = e.dockerClient.ContainerStop(ctx, *d.ContainerID, container.StopOptions{})
			_ = e.dockerClient.ContainerRemove(ctx, *d.ContainerID, container.RemoveOptions{Force: true})
		}
	}
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
	if u := os.Getenv("FORGELAB_AGENT_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	if h := os.Getenv("FORGELAB_AGENT_HOST"); h != "" {
		return fmt.Sprintf("http://%s", h)
	}
	// Try 127.0.0.1:4142 first (for local non-docker backend)
	conn, err := net.DialTimeout("tcp", "127.0.0.1:4142", 200*time.Millisecond)
	if err == nil {
		conn.Close()
		return "http://127.0.0.1:4142"
	}
	// Fallback to host.docker.internal:4142 (for containerized backend accessing host agent)
	return "http://host.docker.internal:4142"
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
) (bool, string) {
	if healthStrategy == models.HealthStrategyNone {
		cJSON, err := e.dockerClient.ContainerInspect(ctx, containerID)
		if err == nil && cJSON.State != nil && cJSON.State.Running {
			return true, "container verified running"
		}
		return false, "container is not running"
	}

	if healthPath == "" {
		healthPath = "/"
	}

	httpClient := &http.Client{Timeout: 2 * time.Second}

	// Bounded 60-second startup/health-check timeout limit
	checkCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	for attempt := 1; attempt <= 15; attempt++ {
		select {
		case <-checkCtx.Done():
			return false, "health check timed out after 60s"
		case <-time.After(2 * time.Second):
		}

		cJSON, err := e.dockerClient.ContainerInspect(checkCtx, containerID)
		if err != nil || (cJSON.State != nil && !cJSON.State.Running) {
			errLogFn("Container exited unexpectedly during health check")
			return false, "container exited unexpectedly"
		}

		// Check Docker native HEALTHCHECK if configured and healthy
		if cJSON.State != nil && cJSON.State.Health != nil {
			if cJSON.State.Health.Status == "healthy" {
				logFn(fmt.Sprintf("Docker native health check reported healthy on attempt %d.", attempt))
				return true, "healthy"
			}
			if cJSON.State.Health.Status == "unhealthy" {
				errLogFn(fmt.Sprintf("Docker native health check reported unhealthy on attempt %d.", attempt))
			}
		}

		// Discover reachable network targets in order of priority:
		// 1. Container internal network IP (works when ForgeLAB is inside Docker or on Linux bridge)
		// 2. Host port on 127.0.0.1 / localhost (works when ForgeLAB runs on host)
		// 3. host.docker.internal / Docker gateway (works when ForgeLAB is in Docker Compose accessing published host port)
		var targets []string

		// 1. Container IP on attached networks
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

		// 2. Host Port on host loopback
		if hostPort != nil && *hostPort > 0 {
			targets = append(targets, fmt.Sprintf("127.0.0.1:%d", *hostPort))
			targets = append(targets, fmt.Sprintf("localhost:%d", *hostPort))

			// 3. Host Port on Docker host/gateway
			if os.Getenv("FORGELAB_IN_DOCKER") != "" || os.Getenv("DOCKER_CONTAINER") != "" {
				targets = append(targets, fmt.Sprintf("host.docker.internal:%d", *hostPort))
				if cJSON.NetworkSettings != nil && cJSON.NetworkSettings.Gateway != "" {
					targets = append(targets, fmt.Sprintf("%s:%d", cJSON.NetworkSettings.Gateway, *hostPort))
				}
			}
		}

		// Try HTTP / TCP verification on discovered targets
		for _, target := range targets {
			if healthStrategy != models.HealthStrategyTCP {
				reqURL := fmt.Sprintf("http://%s%s", target, healthPath)
				req, reqErr := http.NewRequestWithContext(checkCtx, http.MethodGet, reqURL, nil)
				if reqErr == nil {
					res, doErr := httpClient.Do(req)
					if doErr == nil {
						res.Body.Close()
						if res.StatusCode < 500 {
							logFn(fmt.Sprintf("Health check passed via %s (HTTP %d) on attempt %d.", target, res.StatusCode, attempt))
							return true, "healthy"
						}
					}
				}
			}

			// TCP check
			conn, tcpErr := net.DialTimeout("tcp", target, 1*time.Second)
			if tcpErr == nil {
				conn.Close()
				logFn(fmt.Sprintf("TCP health check passed via %s on attempt %d.", target, attempt))
				return true, "healthy"
			}
		}

		// For internal services without public port, if container has been running cleanly for > 4 attempts, accept as healthy
		if hostPort == nil && attempt >= 4 && cJSON.State != nil && cJSON.State.Running {
			logFn(fmt.Sprintf("Internal service verified running stably (attempt %d).", attempt))
			return true, "healthy"
		}
	}

	return false, "health check failed after all attempts"
}
