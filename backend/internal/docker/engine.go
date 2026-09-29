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
	dockerClient      *client.Client
	projectService    *services.ProjectService
	deploymentService *services.DeploymentService
	secretService     *services.SecretService
	sourceService     *services.SourceService
	githubService     *services.GitHubService
	serviceService    *services.ServiceService
	portManager       *network.PortManager
	pathValidator     *security.PathValidator
	wsHub             *ws.Hub
	workDir           string
	localBuildMode    string
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
		pathValidator:     pathValidator,
		wsHub:             wsHub,
		workDir:           workDir,
		localBuildMode:    "direct",
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

// ExecuteDeployment runs the complete deployment pipeline.
func (e *Engine) ExecuteDeployment(ctx context.Context, deploymentID uuid.UUID) error {
	deployment, err := e.deploymentService.GetDeployment(ctx, deploymentID)
	if err != nil {
		return fmt.Errorf("failed to get deployment %s: %w", deploymentID, err)
	}

	project, err := e.projectService.GetProject(ctx, deployment.ProjectID, deployment.ProjectID)
	if err != nil {
		// Fallback without ownership filter for background worker execution
		project, err = e.getProjectByID(ctx, deployment.ProjectID)
		if err != nil {
			return fmt.Errorf("failed to get project %s: %w", deployment.ProjectID, err)
		}
	}

	redactor := logging.NewLogRedactor()

	// Helper to log and publish status/log events
	emitServiceLog := func(serviceID *uuid.UUID, phase, stream, message string) {
		redactedMsg := redactor.Redact(message)
		persistedLog, err := e.deploymentService.AddDeploymentServiceLog(ctx, deployment.ID, serviceID, phase, stream, redactedMsg)
		if err != nil {
			slog.Error("failed to persist deployment log", "deployment_id", deployment.ID, "phase", phase, "error", err)
			return
		}
		slog.Debug("deployment log persisted", "deployment_id", deployment.ID, "log_id", persistedLog.ID, "phase", phase)
		if e.wsHub != nil {
			data := map[string]interface{}{
				"id":            persistedLog.ID,
				"deployment_id": deployment.ID.String(),
				"timestamp":     persistedLog.Timestamp.Format(time.RFC3339Nano),
				"phase":         persistedLog.Phase,
				"stream":        persistedLog.Stream,
				"message":       persistedLog.Message,
			}
			if serviceID != nil {
				data["service_id"] = serviceID.String()
				serviceChannel := fmt.Sprintf("deployment:%s:service:%s", deployment.ID.String(), serviceID.String())
				_ = e.wsHub.PublishEvent(serviceChannel, &ws.EventMessage{
					Type:    "log",
					Channel: serviceChannel,
					Data:    data,
				})
			}
			channel := "deployment:" + deployment.ID.String()
			err := e.wsHub.PublishEvent(channel, &ws.EventMessage{
				Type:    "log",
				Channel: channel,
				Data:    data,
			})
			if err != nil {
				slog.Error("failed to publish deployment log event", "deployment_id", deployment.ID, "channel", channel, "event_type", "log", "error", err)
			}
		}
	}

	emitLog := func(phase, stream, message string) {
		emitServiceLog(nil, phase, stream, message)
	}

	updateStatus := func(newStatus string, failureReason *string) {
		prevStatus := deployment.Status
		if err := e.deploymentService.UpdateDeploymentStatus(ctx, deployment.ID, newStatus, failureReason); err != nil {
			slog.Error("failed to update deployment status", "deployment_id", deployment.ID, "status", newStatus, "error", err)
		}
		deployment.Status = newStatus
		if e.wsHub != nil {
			channel := "deployment:" + deployment.ID.String()
			err := e.wsHub.PublishEvent(channel, &ws.EventMessage{
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
			if err != nil {
				slog.Error("failed to publish deployment status event", "deployment_id", deployment.ID, "channel", channel, "event_type", "status_change", "error", err)
			}
		}
	}

	// Fetch environment variables and configure secret redactor
	envMap, secrets, err := e.secretService.GetDecryptedEnvMap(ctx, project.ID)
	if err == nil {
		redactor.SetSecrets(secrets)
	}

	// 1. SOURCE ACQUISITION & RESOLUTION
	updateStatus(models.DeployStatusCloning, nil)

	var (
		buildSourceDir string
		cleanupDir     string
	)
	defer func() {
		if cleanupDir != "" {
			_ = os.RemoveAll(cleanupDir)
		}
	}()

	if project.SourceType == models.SourceTypeGitHub {
		snapshotDir := filepath.Join(e.workDir, deployment.ID.String())
		_ = os.MkdirAll(snapshotDir, 0755)
		cleanupDir = snapshotDir
		buildSourceDir = snapshotDir

		emitLog(models.LogPhaseSource, models.LogStreamSystem, fmt.Sprintf("Acquiring GitHub repository archive for '%s' (branch: %s)...", project.SourceReference, project.Branch))
		parts := strings.Split(project.SourceReference, "/")
		if len(parts) != 2 {
			reason := fmt.Sprintf("Invalid GitHub repository reference '%s'. Expected format 'owner/repo'", project.SourceReference)
			emitLog(models.LogPhaseSource, models.LogStreamStderr, reason)
			updateStatus(models.DeployStatusFailed, &reason)
			return errors.New(reason)
		}
		if e.githubService == nil {
			reason := "GitHub integration service is not available"
			emitLog(models.LogPhaseSource, models.LogStreamStderr, reason)
			updateStatus(models.DeployStatusFailed, &reason)
			return errors.New(reason)
		}
		if err := e.githubService.AcquireRepoTarball(ctx, project.OwnerID, parts[0], parts[1], project.Branch, snapshotDir); err != nil {
			reason := fmt.Sprintf("Failed to acquire GitHub repository archive: %v", err)
			emitLog(models.LogPhaseSource, models.LogStreamStderr, reason)
			updateStatus(models.DeployStatusFailed, &reason)
			return errors.New(reason)
		}
		emitLog(models.LogPhaseSource, models.LogStreamSystem, "GitHub repository archive acquired successfully.")

	} else if project.SourceType == models.SourceTypeLocalUpload || (project.SourceType == models.SourceTypeLocal && project.SourceReference != "") {
		sourceUUID, err := uuid.Parse(project.SourceReference)
		if err != nil {
			reason := fmt.Sprintf("Invalid local source upload ID '%s': %v", project.SourceReference, err)
			emitLog(models.LogPhaseSource, models.LogStreamStderr, reason)
			updateStatus(models.DeployStatusFailed, &reason)
			return errors.New(reason)
		}
		if e.sourceService == nil {
			reason := "Local source upload service is not available"
			emitLog(models.LogPhaseSource, models.LogStreamStderr, reason)
			updateStatus(models.DeployStatusFailed, &reason)
			return errors.New(reason)
		}
		p, err := e.sourceService.GetSourcePath(ctx, project.OwnerID, sourceUUID)
		if err != nil {
			reason := fmt.Sprintf("Failed to locate uploaded source files: %v", err)
			emitLog(models.LogPhaseSource, models.LogStreamStderr, reason)
			updateStatus(models.DeployStatusFailed, &reason)
			return errors.New(reason)
		}
		buildSourceDir = p
		emitLog(models.LogPhaseSource, models.LogStreamSystem, "Using isolated uploaded source workspace.")

	} else if project.SourceType == models.SourceTypeLocalDirectory || project.RepositoryPath != "" {
		canonicalSource, err := e.pathValidator.ValidateSourcePath(project.RepositoryPath)
		if err != nil {
			reason := fmt.Sprintf("Source path validation failed: %v", err)
			emitLog(models.LogPhaseSource, models.LogStreamStderr, reason)
			updateStatus(models.DeployStatusFailed, &reason)
			return errors.New(reason)
		}

		emitLog(models.LogPhaseSource, models.LogStreamSystem, fmt.Sprintf("Using local directory: %s", project.RepositoryPath))
		emitLog(models.LogPhaseSource, models.LogStreamSystem, "Validated local source directory.")

		if e.localBuildMode == "snapshot" {
			emitLog(models.LogPhaseSource, models.LogStreamSystem, "Using snapshot filesystem build mode.")
			snapshotDir := filepath.Join(e.workDir, deployment.ID.String())
			_ = os.MkdirAll(snapshotDir, 0755)
			cleanupDir = snapshotDir
			if err := copyDirectory(canonicalSource, snapshotDir); err != nil {
				reason := fmt.Sprintf("Failed to snapshot source files: %v", err)
				emitLog(models.LogPhaseSource, models.LogStreamStderr, reason)
				updateStatus(models.DeployStatusFailed, &reason)
				return errors.New(reason)
			}
			buildSourceDir = snapshotDir
		} else {
			emitLog(models.LogPhaseSource, models.LogStreamSystem, "Using direct filesystem build mode.")
			buildSourceDir = canonicalSource
		}

	} else if project.SourceType == models.SourceTypeLocalAgent {
		emitLog(models.LogPhaseSource, models.LogStreamSystem, fmt.Sprintf("Connected to ForgeLAB local agent (session: %s).", project.SourceReference))
		buildSourceDir = "" // Source streamed dynamically per service
	} else {
		reason := "No valid local directory, uploaded source, or repository configured"
		emitLog(models.LogPhaseSource, models.LogStreamStderr, reason)
		updateStatus(models.DeployStatusFailed, &reason)
		return errors.New(reason)
	}

	// Check if multi-service project
	var allProjectServices []*models.Service
	if e.serviceService != nil {
		allProjectServices, _ = e.serviceService.ListServices(ctx, project.ID)
	}
	if len(allProjectServices) == 0 && len(project.Services) > 0 {
		allProjectServices = project.Services
	}

	// Filter services to deploy based on this release's service_deployments records
	var servicesToDeploy []*models.Service
	svcDeploys, _ := e.deploymentService.ListServiceDeployments(ctx, deployment.ID)
	if len(svcDeploys) > 0 {
		targetMap := make(map[uuid.UUID]bool)
		for _, sd := range svcDeploys {
			targetMap[sd.ServiceID] = true
		}
		for _, s := range allProjectServices {
			if targetMap[s.ID] {
				servicesToDeploy = append(servicesToDeploy, s)
			}
		}
	} else {
		servicesToDeploy = allProjectServices
	}

	emitServiceStatusChange := func(svcID uuid.UUID, svcName, newStatus string, hostPort *int, failureReason *string) {
		if e.wsHub != nil {
			data := map[string]interface{}{
				"deployment_id": deployment.ID.String(),
				"project_id":    project.ID.String(),
				"service_id":    svcID.String(),
				"service_name":  svcName,
				"new_status":    newStatus,
				"timestamp":     time.Now().Format(time.RFC3339),
			}
			if hostPort != nil {
				data["host_port"] = *hostPort
			}
			if failureReason != nil {
				data["failure_reason"] = *failureReason
			}
			channel := "deployment:" + deployment.ID.String()
			_ = e.wsHub.PublishEvent(channel, &ws.EventMessage{
				Type:    "status_change",
				Channel: channel,
				Data:    data,
			})
			serviceChannel := fmt.Sprintf("deployment:%s:service:%s", deployment.ID.String(), svcID.String())
			_ = e.wsHub.PublishEvent(serviceChannel, &ws.EventMessage{
				Type:    "status_change",
				Channel: serviceChannel,
				Data:    data,
			})
		}
	}

	if len(servicesToDeploy) > 0 {
		updateStatus(models.DeployStatusBuilding, nil)
		emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Starting multi-service deployment pipeline for %d service(s)...", len(servicesToDeploy)))

		// Ensure isolated project Docker bridge network
		networkName := fmt.Sprintf("forgelab-net-%s", project.ID.String())
		_, netErr := e.dockerClient.NetworkInspect(ctx, networkName, dockernetwork.InspectOptions{})
		if netErr != nil {
			_, _ = e.dockerClient.NetworkCreate(ctx, networkName, dockernetwork.CreateOptions{
				Driver: "bridge",
			})
		}

		allHealthy := true
		anyHealthy := false
		var primaryPort int

		for _, svc := range servicesToDeploy {
			svcID := svc.ID
			emitServiceLog(&svcID, models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Starting build for service '%s' (role: %s, runtime: %s)...", svc.Name, svc.Role, svc.RuntimeType))
			_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, deployment.ID, svcID, models.DeployStatusBuilding, nil)
			_ = e.serviceService.UpdateServiceStatus(ctx, svcID, models.DeployStatusBuilding, nil, nil, nil)
			emitServiceStatusChange(svcID, svc.Name, models.DeployStatusBuilding, nil, nil)

			svcTag := fmt.Sprintf("forgelab/%s/%s:%d", project.ID, svc.Name, deployment.DeployNumber)
			relDockerPath := "Dockerfile"
			var tarArchive io.ReadCloser

			if project.SourceType == models.SourceTypeLocalAgent {
				// Stream build context directly from local agent without copying full repository
				baseURL := resolveAgentBaseURL()
				agentURL := fmt.Sprintf("%s/api/agent/sources/%s/stream-context?service_path=%s&runtime=%s&port=%d&start_cmd=%s",
					baseURL,
					project.SourceReference,
					url.QueryEscape(svc.SourcePath),
					url.QueryEscape(svc.RuntimeType),
					svc.InternalPort,
					url.QueryEscape(svc.StartCommand),
				)
				req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, agentURL, nil)
				if reqErr != nil {
					reason := fmt.Sprintf("Failed to request agent stream context: %v", reqErr)
					emitServiceLog(&svcID, models.LogPhaseBuild, models.LogStreamStderr, reason)
					_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, deployment.ID, svcID, models.DeployStatusFailed, &reason)
					_ = e.serviceService.UpdateServiceStatus(ctx, svcID, models.DeployStatusFailed, nil, nil, nil)
					emitServiceStatusChange(svcID, svc.Name, models.DeployStatusFailed, nil, &reason)
					allHealthy = false
					continue
				}
				resp, httpErr := http.DefaultClient.Do(req)
				if httpErr != nil || resp.StatusCode != http.StatusOK {
					reason := "Failed to stream source from local agent"
					if httpErr != nil {
						reason = httpErr.Error()
					} else if resp != nil {
						resp.Body.Close()
						if resp.StatusCode == http.StatusNotFound {
							reason = "Local agent source session expired or agent restarted. Please re-select the project folder."
						} else {
							reason = fmt.Sprintf("agent returned status %d", resp.StatusCode)
						}
					}
					emitServiceLog(&svcID, models.LogPhaseBuild, models.LogStreamStderr, reason)
					_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, deployment.ID, svcID, models.DeployStatusFailed, &reason)
					_ = e.serviceService.UpdateServiceStatus(ctx, svcID, models.DeployStatusFailed, nil, nil, nil)
					emitServiceStatusChange(svcID, svc.Name, models.DeployStatusFailed, nil, &reason)
					allHealthy = false
					continue
				}
				tarArchive = resp.Body
				if svc.BuildStrategy == models.BuildStrategyAuto {
					relDockerPath = "Dockerfile.forgelab"
				}
			} else {
				// Standard local or extracted archive directory
				svcContextDir := filepath.Join(buildSourceDir, svc.SourcePath)
				if _, err := os.Stat(svcContextDir); err != nil {
					svcContextDir = buildSourceDir
				}

				var virtualFiles map[string][]byte
				if svc.BuildStrategy == models.BuildStrategyAuto {
					intPort := svc.InternalPort
					if intPort <= 0 {
						intPort = 8080
					}
					generatedContent := detector.GenerateDockerfile(svc.RuntimeType, intPort, svc.StartCommand)
					relDockerPath = "Dockerfile.forgelab"
					virtualFiles = map[string][]byte{
						"Dockerfile.forgelab": []byte(generatedContent),
					}
				} else {
					if svc.DockerfilePath != "" {
						relDockerPath = svc.DockerfilePath
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
						emitServiceLog(&svcID, phase, stream, msg)
					},
				})
			}

			// Image Build
			emitServiceLog(&svcID, models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Building Docker image '%s'...", svcTag))
			buildResponse, err := e.dockerClient.ImageBuild(ctx, tarArchive, types.ImageBuildOptions{
				Tags:       []string{svcTag},
				Dockerfile: relDockerPath,
				Remove:     true,
			})
			tarArchive.Close()
			if err != nil {
				reason := fmt.Sprintf("Docker build failed: %v", err)
				emitServiceLog(&svcID, models.LogPhaseBuild, models.LogStreamStderr, reason)
				_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, deployment.ID, svcID, models.DeployStatusFailed, &reason)
				_ = e.serviceService.UpdateServiceStatus(ctx, svcID, models.DeployStatusFailed, nil, nil, nil)
				emitServiceStatusChange(svcID, svc.Name, models.DeployStatusFailed, nil, &reason)
				allHealthy = false
				continue
			}

			if err := e.parseDockerStream(buildResponse.Body, func(msg string) {
				emitServiceLog(&svcID, models.LogPhaseBuild, models.LogStreamStdout, msg)
			}); err != nil {
				buildResponse.Body.Close()
				reason := fmt.Sprintf("Docker build error: %v", err)
				emitServiceLog(&svcID, models.LogPhaseBuild, models.LogStreamStderr, reason)
				_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, deployment.ID, svcID, models.DeployStatusFailed, &reason)
				_ = e.serviceService.UpdateServiceStatus(ctx, svcID, models.DeployStatusFailed, nil, nil, nil)
				emitServiceStatusChange(svcID, svc.Name, models.DeployStatusFailed, nil, &reason)
				allHealthy = false
				continue
			}
			buildResponse.Body.Close()
			emitServiceLog(&svcID, models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Docker image '%s' built successfully.", svcTag))

			// Container Startup
			_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, deployment.ID, svcID, models.DeployStatusStarting, nil)
			_ = e.serviceService.UpdateServiceStatus(ctx, svcID, models.DeployStatusStarting, nil, &svcTag, nil)
			emitServiceStatusChange(svcID, svc.Name, models.DeployStatusStarting, nil, nil)
			emitServiceLog(&svcID, models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Creating container for service '%s'...", svc.Name))

			intPort := svc.InternalPort
			if intPort <= 0 {
				intPort = 8080
			}

			var hostPort *int
			var portBindings nat.PortMap
			targetPortStr := fmt.Sprintf("%d/tcp", intPort)

			// Allocate host port if public or frontend or single service
			if svc.PublicExposed || svc.Role == models.RoleFrontend || len(allProjectServices) == 1 {
				allocated, err := e.portManager.AllocatePort()
				if err == nil {
					hostPort = &allocated
					if primaryPort == 0 {
						primaryPort = allocated
					}
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

			// Container environment
			svcEnv := make([]string, 0, len(envMap)+len(allProjectServices)*3+2)
			for k, v := range envMap {
				svcEnv = append(svcEnv, fmt.Sprintf("%s=%s", k, v))
			}
			svcEnv = append(svcEnv, fmt.Sprintf("PORT=%d", intPort))
			svcEnv = append(svcEnv, fmt.Sprintf("SERVICE_NAME=%s", svc.Name))

			// Peer service discovery
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

			containerName := fmt.Sprintf("forgelab-app-%s-%s", deployment.ID.String()[:8], svc.Name)
			containerConfig := &container.Config{
				Image: svcTag,
				Env:   svcEnv,
				ExposedPorts: nat.PortSet{
					nat.Port(targetPortStr): struct{}{},
				},
				Labels: map[string]string{
					"forgelab.project_id":    project.ID.String(),
					"forgelab.deployment_id": deployment.ID.String(),
					"forgelab.service_id":    svcID.String(),
					"forgelab.service_name":  svc.Name,
				},
			}

			hostConfig := &container.HostConfig{
				PortBindings: portBindings,
				RestartPolicy: container.RestartPolicy{
					Name: "unless-stopped",
				},
			}

			netConfig := &dockernetwork.NetworkingConfig{
				EndpointsConfig: map[string]*dockernetwork.EndpointSettings{
					networkName: {
						Aliases: []string{svc.Name},
					},
				},
			}

			// Remove any existing container with same name
			_ = e.dockerClient.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true})

			resp, err := e.dockerClient.ContainerCreate(ctx, containerConfig, hostConfig, netConfig, nil, containerName)
			if err != nil {
				reason := fmt.Sprintf("Failed to create container for service '%s': %v", svc.Name, err)
				emitServiceLog(&svcID, models.LogPhaseStartup, models.LogStreamStderr, reason)
				_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, deployment.ID, svcID, models.DeployStatusFailed, &reason)
				_ = e.serviceService.UpdateServiceStatus(ctx, svcID, models.DeployStatusFailed, nil, nil, nil)
				emitServiceStatusChange(svcID, svc.Name, models.DeployStatusFailed, nil, &reason)
				allHealthy = false
				continue
			}

			containerID := resp.ID
			_ = e.deploymentService.UpdateServiceDeploymentContainer(ctx, deployment.ID, svcID, containerID, hostPort)
			_ = e.serviceService.UpdateServiceStatus(ctx, svcID, models.DeployStatusStarting, &containerID, &svcTag, hostPort)
			emitServiceStatusChange(svcID, svc.Name, models.DeployStatusStarting, hostPort, nil)

			if err := e.dockerClient.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
				reason := fmt.Sprintf("Failed to start container for service '%s': %v", svc.Name, err)
				emitServiceLog(&svcID, models.LogPhaseStartup, models.LogStreamStderr, reason)
				_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, deployment.ID, svcID, models.DeployStatusFailed, &reason)
				_ = e.serviceService.UpdateServiceStatus(ctx, svcID, models.DeployStatusFailed, nil, nil, nil)
				emitServiceStatusChange(svcID, svc.Name, models.DeployStatusFailed, nil, &reason)
				allHealthy = false
				continue
			}

			emitServiceLog(&svcID, models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Container %s started for service '%s'.", containerID[:12], svc.Name))

			// Health Checking
			_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, deployment.ID, svcID, models.DeployStatusHealthChecking, nil)
			_ = e.serviceService.UpdateServiceStatus(ctx, svcID, models.DeployStatusHealthChecking, &containerID, &svcTag, hostPort)
			emitServiceStatusChange(svcID, svc.Name, models.DeployStatusHealthChecking, hostPort, nil)
			emitServiceLog(&svcID, models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("Verifying health for service '%s'...", svc.Name))

			svcHealthy := false
			if hostPort != nil {
				healthURL := fmt.Sprintf("http://127.0.0.1:%d", *hostPort)
				if svc.HealthCheckPath != nil && *svc.HealthCheckPath != "" {
					healthURL += *svc.HealthCheckPath
				} else {
					healthURL += "/"
				}
				httpClient := &http.Client{Timeout: 2 * time.Second}
				for attempt := 1; attempt <= 10; attempt++ {
					time.Sleep(2 * time.Second)
					cJSON, err := e.dockerClient.ContainerInspect(ctx, containerID)
					if err != nil || (cJSON.State != nil && !cJSON.State.Running) {
						emitServiceLog(&svcID, models.LogPhaseHealth, models.LogStreamStderr, "Container exited unexpectedly")
						break
					}
					req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
					if err == nil {
						res, err := httpClient.Do(req)
						if err == nil {
							res.Body.Close()
							if res.StatusCode < 500 {
								svcHealthy = true
								emitServiceLog(&svcID, models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("Health check passed (HTTP %d) on attempt %d.", res.StatusCode, attempt))
								break
							}
						}
					}
					// TCP fallback
					conn, tcpErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", *hostPort), 1*time.Second)
					if tcpErr == nil {
						conn.Close()
						svcHealthy = true
						emitServiceLog(&svcID, models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("TCP health check passed on attempt %d.", attempt))
						break
					}
				}
			} else {
				// Container state check for internal/worker services without public port
				time.Sleep(2 * time.Second)
				cJSON, err := e.dockerClient.ContainerInspect(ctx, containerID)
				if err == nil && cJSON.State != nil && cJSON.State.Running {
					svcHealthy = true
					emitServiceLog(&svcID, models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("Service '%s' verified running on network '%s'.", svc.Name, networkName))
				}
			}

			if svcHealthy {
				// Clean up previous container for this service if different
				if svc.ContainerID != nil && *svc.ContainerID != "" && *svc.ContainerID != containerID {
					_ = e.dockerClient.ContainerStop(ctx, *svc.ContainerID, container.StopOptions{})
					_ = e.dockerClient.ContainerRemove(ctx, *svc.ContainerID, container.RemoveOptions{Force: true})
				}

				_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, deployment.ID, svcID, models.DeployStatusRunning, nil)
				_ = e.serviceService.UpdateServiceStatus(ctx, svcID, models.DeployStatusRunning, &containerID, &svcTag, hostPort)
				emitServiceStatusChange(svcID, svc.Name, models.DeployStatusRunning, hostPort, nil)
				emitServiceLog(&svcID, models.LogPhaseRuntime, models.LogStreamSystem, fmt.Sprintf("Service '%s' is RUNNING!", svc.Name))
				anyHealthy = true
			} else {
				// Stop and remove broken container
				_ = e.dockerClient.ContainerStop(ctx, containerID, container.StopOptions{})
				_ = e.dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
				if hostPort != nil {
					e.portManager.ReleasePort(*hostPort)
				}
				reason := "Health check failed after retries"
				emitServiceLog(&svcID, models.LogPhaseHealth, models.LogStreamStderr, reason)
				_ = e.deploymentService.UpdateServiceDeploymentStatus(ctx, deployment.ID, svcID, models.DeployStatusFailed, &reason)
				_ = e.serviceService.UpdateServiceStatus(ctx, svcID, models.DeployStatusFailed, nil, nil, nil)
				emitServiceStatusChange(svcID, svc.Name, models.DeployStatusFailed, nil, &reason)
				allHealthy = false
			}
		}

		// Overall Project & Deployment state calculation
		allCurrentServices, _ := e.serviceService.ListServices(ctx, project.ID)
		newProjectStatus := services.CalculateProjectStatus(allCurrentServices)
		_ = e.projectService.UpdateProjectStatus(ctx, project.ID, newProjectStatus)

		if allHealthy && anyHealthy {
			updateStatus(models.DeployStatusRunning, nil)
			_ = e.deploymentService.SetCurrentDeployment(ctx, project.ID, deployment.ID, newProjectStatus)
			if primaryPort > 0 {
				_, _ = e.projectService.UpdateProjectPort(ctx, project.ID, primaryPort)
			}
			emitLog(models.LogPhaseRuntime, models.LogStreamSystem, fmt.Sprintf("Release #%d successfully deployed!", deployment.DeployNumber))
			return nil
		} else if anyHealthy {
			updateStatus(models.ProjectStatusPartiallyRunning, nil)
			_ = e.deploymentService.SetCurrentDeployment(ctx, project.ID, deployment.ID, newProjectStatus)
			if primaryPort > 0 {
				_, _ = e.projectService.UpdateProjectPort(ctx, project.ID, primaryPort)
			}
			emitLog(models.LogPhaseRuntime, models.LogStreamSystem, fmt.Sprintf("Release #%d partially running (some services degraded).", deployment.DeployNumber))
			return nil
		} else {
			reason := "All target services failed health check or deployment"
			updateStatus(models.DeployStatusFailed, &reason)
			emitLog(models.LogPhaseHealth, models.LogStreamStderr, reason)
			return errors.New(reason)
		}
	}

	// 2. BUILDING DOCKER IMAGE
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
		emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Using Dockerfile build strategy with '%s'...", dockerfilePath))
	} else {
		// Automatic Build Strategy: generate container image specification in memory (no disk modification)
		runtimeType := deployment.RuntimeType
		if runtimeType == "" {
			runtimeType = project.RuntimeType
		}
		intPort := deployment.InternalPort
		if intPort <= 0 {
			intPort = project.InternalPort
		}
		if intPort <= 0 {
			intPort = 8080
		}
		startCmd := deployment.StartCommand
		if startCmd == "" {
			startCmd = project.StartCommand
		}

		emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Generating automatic build recipe for runtime: %s (internal port: %d)...", runtimeType, intPort))
		generatedContent := detector.GenerateDockerfile(runtimeType, intPort, startCmd)
		relDockerPath = "Dockerfile.forgelab"
		virtualFiles = map[string][]byte{
			"Dockerfile.forgelab": []byte(generatedContent),
		}
	}

	// Load and apply .dockerignore
	emitLog(models.LogPhaseBuild, models.LogStreamSystem, "Applying .dockerignore...")
	matcher, err := LoadDockerignore(buildContextDir)
	if err != nil {
		emitLog(models.LogPhaseBuild, models.LogStreamStderr, fmt.Sprintf("Warning reading .dockerignore: %v. Using default filters.", err))
		matcher = NewDockerignoreMatcher(DefaultIgnorePatterns)
	}

	// Stream build context concurrently without materializing tar in RAM
	emitLog(models.LogPhaseBuild, models.LogStreamSystem, "Streaming Docker build context...")
	emitLog(models.LogPhaseBuild, models.LogStreamSystem, "Docker build started.")

	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()

	tarArchive := StreamBuildContext(streamCtx, TarStreamerOptions{
		BuildContextDir: buildContextDir,
		Matcher:         matcher,
		VirtualFiles:    virtualFiles,
		EmitLog:         emitLog,
	})
	defer tarArchive.Close()

	buildResponse, err := e.dockerClient.ImageBuild(ctx, tarArchive, types.ImageBuildOptions{
		Tags:       []string{*deployment.ImageTag},
		Dockerfile: relDockerPath,
		Remove:     true,
	})
	if err != nil {
		reason := fmt.Sprintf("Docker build initialization failed: %v", err)
		emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
		updateStatus(models.DeployStatusFailed, &reason)
		return errors.New(reason)
	}
	defer buildResponse.Body.Close()

	// Parse build logs line by line
	if err := e.parseDockerStream(buildResponse.Body, func(msg string) {
		emitLog(models.LogPhaseBuild, models.LogStreamStdout, msg)
	}); err != nil {
		reason := fmt.Sprintf("Docker image build failed: %v", err)
		emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
		updateStatus(models.DeployStatusFailed, &reason)
		return errors.New(reason)
	}

	emitLog(models.LogPhaseBuild, models.LogStreamSystem, "Docker build completed.")

	// 3. STARTING CONTAINER
	updateStatus(models.DeployStatusStarting, nil)
	emitLog(models.LogPhaseStartup, models.LogStreamSystem, "Creating application container...")

	// Allocate host port
	allocatedPort, err := e.portManager.AllocatePort()
	if err != nil {
		reason := fmt.Sprintf("Failed to allocate host port: %v", err)
		emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
		updateStatus(models.DeployStatusFailed, &reason)
		return errors.New(reason)
	}

	// Internal application port
	intPort := deployment.InternalPort
	if intPort <= 0 {
		intPort = project.InternalPort
	}
	if intPort <= 0 {
		intPort = 8080
	}

	// Prepare container env vars
	envSlice := make([]string, 0, len(envMap)+1)
	for k, v := range envMap {
		envSlice = append(envSlice, fmt.Sprintf("%s=%s", k, v))
	}
	envSlice = append(envSlice, fmt.Sprintf("PORT=%d", intPort))

	containerName := fmt.Sprintf("forgelab-app-%s", deployment.ID.String())
	targetPortStr := fmt.Sprintf("%d/tcp", intPort)

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
	}

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
		e.portManager.ReleasePort(allocatedPort)
		reason := fmt.Sprintf("Failed to start container: %v", err)
		emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
		updateStatus(models.DeployStatusFailed, &reason)
		return errors.New(reason)
	}

	emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Container %s started (host port %d -> internal port %d).", containerID[:12], allocatedPort, intPort))

	// 4. HEALTH CHECKING
	updateStatus(models.DeployStatusHealthChecking, nil)

	healthStrategy := deployment.HealthStrategy
	if healthStrategy == "" {
		healthStrategy = project.HealthStrategy
	}
	if healthStrategy == "" {
		healthStrategy = models.HealthStrategyAuto
	}

	healthPath := "/"
	if project.HealthCheckPath != nil && *project.HealthCheckPath != "" {
		healthPath = *project.HealthCheckPath
	}

	emitLog(models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("Performing health check (strategy: %s, port: %d, path: %s)...", healthStrategy, allocatedPort, healthPath))

	healthy := false

	if healthStrategy == models.HealthStrategyNone {
		// Verify container is alive
		cJSON, err := e.dockerClient.ContainerInspect(ctx, containerID)
		if err == nil && cJSON.State != nil && cJSON.State.Running {
			healthy = true
			emitLog(models.LogPhaseHealth, models.LogStreamSystem, "Health strategy 'none': container verified running.")
		}
	} else if healthStrategy == models.HealthStrategyTCP {
		for attempt := 1; attempt <= 10; attempt++ {
			time.Sleep(2 * time.Second)
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", allocatedPort), 2*time.Second)
			if err == nil {
				conn.Close()
				healthy = true
				emitLog(models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("TCP health check passed on attempt %d.", attempt))
				break
			}
			emitLog(models.LogPhaseHealth, models.LogStreamStderr, fmt.Sprintf("TCP health check attempt %d failed: %v", attempt, err))
		}
	} else {
		// HTTP or Auto
		healthURL := fmt.Sprintf("http://127.0.0.1:%d%s", allocatedPort, healthPath)
		httpClient := &http.Client{Timeout: 3 * time.Second}

		for attempt := 1; attempt <= 10; attempt++ {
			time.Sleep(2 * time.Second)

			// Verify container is still running
			cJSON, err := e.dockerClient.ContainerInspect(ctx, containerID)
			if err != nil || (cJSON.State != nil && !cJSON.State.Running) {
				emitLog(models.LogPhaseHealth, models.LogStreamStderr, "Container exited unexpectedly during health check")
				break
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
			if err == nil {
				res, err := httpClient.Do(req)
				if err == nil {
					res.Body.Close()
					if res.StatusCode >= 200 && res.StatusCode < 400 {
						healthy = true
						emitLog(models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("Health check passed (HTTP %d) on attempt %d.", res.StatusCode, attempt))
						break
					} else if res.StatusCode >= 500 {
						// 5xx server error is never healthy under any strategy
						emitLog(models.LogPhaseHealth, models.LogStreamStderr, fmt.Sprintf("Health check attempt %d returned HTTP %d server error", attempt, res.StatusCode))
					} else {
						// 4xx client status codes (e.g. 404 Not Found on unmapped path or 401 Unauthorized)
						if healthStrategy == models.HealthStrategyAuto {
							conn, tcpErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", allocatedPort), 1*time.Second)
							if tcpErr == nil {
								conn.Close()
								healthy = true
								emitLog(models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("Health check auto-detected active server (HTTP %d) on attempt %d.", res.StatusCode, attempt))
								break
							}
						}
						emitLog(models.LogPhaseHealth, models.LogStreamStderr, fmt.Sprintf("Health check attempt %d returned HTTP %d", attempt, res.StatusCode))
					}
				} else {
					if healthStrategy == models.HealthStrategyAuto {
						conn, tcpErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", allocatedPort), 1*time.Second)
						if tcpErr == nil {
							conn.Close()
							healthy = true
							emitLog(models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("Health check auto-detected active TCP socket on attempt %d.", attempt))
							break
						}
					}
					emitLog(models.LogPhaseHealth, models.LogStreamStderr, fmt.Sprintf("Health check attempt %d failed: %v", attempt, err))
				}
			}
		}
	}

	// 5. PROMOTION OR SAFETY FAILURE INVARIANT
	if !healthy {
		// Stop broken container
		_ = e.dockerClient.ContainerStop(ctx, containerID, container.StopOptions{})
		_ = e.dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
		e.portManager.ReleasePort(allocatedPort)

		reason := "Health check failed after 10 retries"
		emitLog(models.LogPhaseHealth, models.LogStreamStderr, reason)
		updateStatus(models.DeployStatusFailed, &reason)

		// Deployment Safety Invariant:
		// project.current_deployment_id remains unchanged! Previous healthy deployment survives.
		emitLog(models.LogPhaseHealth, models.LogStreamSystem, "SAFETY INVARIANT ENFORCED: New deployment failed health check. Previous healthy release remains active!")
		return errors.New(reason)
	}

	// Health check passed! Promote new deployment to current
	updateStatus(models.DeployStatusRunning, nil)

	// Clean up previous container if exists
	oldDeploymentID := project.CurrentDeploymentID
	_ = e.deploymentService.SetCurrentDeployment(ctx, project.ID, deployment.ID, models.ProjectStatusRunning)

	// Update project port
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
	if svc.ContainerID != nil && *svc.ContainerID != "" {
		_ = e.dockerClient.ContainerStop(ctx, *svc.ContainerID, container.StopOptions{})
	}
	_ = e.serviceService.UpdateServiceStatus(ctx, serviceID, models.ProjectStatusStopped, nil, nil, nil)
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
	_ = e.serviceService.UpdateServiceStatus(ctx, serviceID, models.ProjectStatusRunning, nil, nil, nil)
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
