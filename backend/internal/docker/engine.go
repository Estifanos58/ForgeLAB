package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"

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
	portManager       *network.PortManager
	pathValidator     *security.PathValidator
	wsHub             *ws.Hub
	workDir           string
}

func NewEngine(
	dockerClient *client.Client,
	projectService *services.ProjectService,
	deploymentService *services.DeploymentService,
	secretService *services.SecretService,
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
		portManager:       portManager,
		pathValidator:     pathValidator,
		wsHub:             wsHub,
		workDir:           workDir,
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
	emitLog := func(phase, stream, message string) {
		redactedMsg := redactor.Redact(message)
		_ = e.deploymentService.AddDeploymentLog(ctx, deployment.ID, phase, stream, redactedMsg)
		if e.wsHub != nil {
			_ = e.wsHub.PublishEvent("deployment:"+deployment.ID.String(), &ws.EventMessage{
				Type:    "log",
				Channel: "deployment:" + deployment.ID.String(),
				Data: map[string]interface{}{
					"timestamp": time.Now().Format(time.RFC3339),
					"phase":     phase,
					"stream":    stream,
					"message":   redactedMsg,
				},
			})
		}
	}

	updateStatus := func(newStatus string, failureReason *string) {
		prevStatus := deployment.Status
		_ = e.deploymentService.UpdateDeploymentStatus(ctx, deployment.ID, newStatus, failureReason)
		deployment.Status = newStatus
		if e.wsHub != nil {
			_ = e.wsHub.PublishEvent("deployment:"+deployment.ID.String(), &ws.EventMessage{
				Type:    "status_change",
				Channel: "deployment:" + deployment.ID.String(),
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

	// Fetch environment variables and configure secret redactor
	envMap, secrets, err := e.secretService.GetDecryptedEnvMap(ctx, project.ID)
	if err == nil {
		redactor.SetSecrets(secrets)
	}

	// 1. CLONING / SOURCE ACQUISITION
	updateStatus(models.DeployStatusCloning, nil)
	emitLog(models.LogPhaseSource, models.LogStreamSystem, fmt.Sprintf("Acquiring source snapshot for project '%s'...", project.Name))

	// Validate path and create snapshot
	canonicalSource, err := e.pathValidator.ValidateSourcePath(project.RepositoryPath)
	if err != nil {
		reason := fmt.Sprintf("Source path validation failed: %v", err)
		emitLog(models.LogPhaseSource, models.LogStreamStderr, reason)
		updateStatus(models.DeployStatusFailed, &reason)
		return errors.New(reason)
	}

	snapshotDir := filepath.Join(e.workDir, deployment.ID.String())
	if err := copyDirectory(canonicalSource, snapshotDir); err != nil {
		reason := fmt.Sprintf("Failed to snapshot source files: %v", err)
		emitLog(models.LogPhaseSource, models.LogStreamStderr, reason)
		updateStatus(models.DeployStatusFailed, &reason)
		return errors.New(reason)
	}
	defer os.RemoveAll(snapshotDir) // Clean up snapshot dir after build completes

	emitLog(models.LogPhaseSource, models.LogStreamSystem, "Source snapshot acquired successfully.")

	// 2. BUILDING DOCKER IMAGE
	updateStatus(models.DeployStatusBuilding, nil)
	emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Building Docker image '%s'...", *deployment.ImageTag))

	buildContextDir, _, err := e.pathValidator.ValidateBuildContextAndDockerfile(snapshotDir, project.BuildContext, project.DockerfilePath)
	if err != nil {
		reason := fmt.Sprintf("Build context/Dockerfile validation failed: %v", err)
		emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
		updateStatus(models.DeployStatusFailed, &reason)
		return errors.New(reason)
	}

	tarArchive, err := createTarArchive(buildContextDir)
	if err != nil {
		reason := fmt.Sprintf("Failed to pack build context: %v", err)
		emitLog(models.LogPhaseBuild, models.LogStreamStderr, reason)
		updateStatus(models.DeployStatusFailed, &reason)
		return errors.New(reason)
	}

	relDockerPath, _ := filepath.Rel(buildContextDir, filepath.Join(snapshotDir, project.BuildContext, project.DockerfilePath))
	if relDockerPath == "" {
		relDockerPath = "Dockerfile"
	}

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

	emitLog(models.LogPhaseBuild, models.LogStreamSystem, "Docker image build completed successfully.")

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

	// Prepare container env vars
	envSlice := make([]string, 0, len(envMap)+1)
	for k, v := range envMap {
		envSlice = append(envSlice, fmt.Sprintf("%s=%s", k, v))
	}
	envSlice = append(envSlice, fmt.Sprintf("PORT=%d", 8080)) // Internal default port

	containerName := fmt.Sprintf("forgelab-app-%s", deployment.ID.String())

	// Detect target container port or default 8080/3000/80
	targetPortStr := "8080/tcp"

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

	emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Container %s started on host port %d.", containerID[:12], allocatedPort))

	// 4. HEALTH CHECKING
	updateStatus(models.DeployStatusHealthChecking, nil)
	healthPath := "/health"
	if project.HealthCheckPath != nil && *project.HealthCheckPath != "" {
		healthPath = *project.HealthCheckPath
	}

	emitLog(models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("Performing health check on http://127.0.0.1:%d%s...", allocatedPort, healthPath))

	healthy := false
	healthURL := fmt.Sprintf("http://127.0.0.1:%d%s", allocatedPort, healthPath)
	httpClient := &http.Client{Timeout: 3 * time.Second}

	for attempt := 1; attempt <= 10; attempt++ {
		time.Sleep(2 * time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err == nil {
			res, err := httpClient.Do(req)
			if err == nil {
				res.Body.Close()
				if res.StatusCode >= 200 && res.StatusCode < 400 {
					healthy = true
					emitLog(models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("Health check passed (HTTP %d) on attempt %d.", res.StatusCode, attempt))
					break
				} else {
					emitLog(models.LogPhaseHealth, models.LogStreamStderr, fmt.Sprintf("Health check attempt %d returned HTTP %d", attempt, res.StatusCode))
				}
			} else {
				emitLog(models.LogPhaseHealth, models.LogStreamStderr, fmt.Sprintf("Health check attempt %d failed: %v", attempt, err))
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

func createTarArchive(srcDir string) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relPath)

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			_, err = io.Copy(tw, file)
			return err
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}

	return &buf, nil
}
