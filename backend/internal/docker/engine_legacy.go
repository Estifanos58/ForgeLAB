package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/agent"
	"github.com/forgelab/backend/internal/detector"
	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/logging"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/tararchive"
	ws "github.com/forgelab/backend/internal/websocket"
)

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
	var runtimeEnvMap map[string]string

	if e.secretService != nil {
		var envErr error
		if len(deployment.EnvSnapshot) > 0 {
			runtimeEnvMap, _, envErr = e.secretService.GetEnvMapFromSnapshot(deployment.EnvSnapshot, models.EnvScopeRuntime)
			if envErr != nil {
				failReason := fmt.Sprintf("failed to decrypt environment snapshot: %v", envErr)
				emitLog("init", "stderr", failReason)
				updateStatus(models.DeployStatusFailed, &failReason)
				return fmt.Errorf("failed to get env map from snapshot: %w", envErr)
			}
		} else {
			runtimeEnvMap, _, envErr = e.secretService.GetDecryptedEnvMap(ctx, project.ID, nil, models.EnvScopeRuntime)
			if envErr != nil {
				failReason := fmt.Sprintf("failed to decrypt environment variables: %v", envErr)
				emitLog("init", "stderr", failReason)
				updateStatus(models.DeployStatusFailed, &failReason)
				return fmt.Errorf("failed to get decrypted env map: %w", envErr)
			}
		}
		normalArgs, secretVars, allSecrets, buildVarErr := e.secretService.GetBuildVariables(ctx, project.ID, nil, deployment.EnvSnapshot)
		if buildVarErr != nil {
			failReason := fmt.Sprintf("failed to process build environment variables: %v", buildVarErr)
			emitLog("init", "stderr", failReason)
			updateStatus(models.DeployStatusFailed, &failReason)
			return fmt.Errorf("failed to get build variables: %w", buildVarErr)
		}
		normalBuildArgs = normalArgs
		secretBuildVars = secretVars
		redactor.SetSecrets(allSecrets)
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
				var prismaInfo *discovery.PrismaStagingInfo
				if buildContextDir != "" {
					prismaInfo, _ = detector.DetectPrismaFromDir(buildContextDir)
				}
				seenArgKeys := make(map[string]bool)
				var buildArgKeys []string
				for k := range normalBuildArgs {
					if !seenArgKeys[k] {
						seenArgKeys[k] = true
						buildArgKeys = append(buildArgKeys, k)
					}
				}
				for k := range secretBuildVars {
					if !seenArgKeys[k] {
						seenArgKeys[k] = true
						buildArgKeys = append(buildArgKeys, k)
					}
				}
				if deployment.RuntimeType == "nextjs" {
					for k := range runtimeEnvMap {
						if !seenArgKeys[k] {
							seenArgKeys[k] = true
							buildArgKeys = append(buildArgKeys, k)
						}
					}
				}
				sort.Strings(buildArgKeys)
				dockerfileContent := detector.GenerateDockerfileWithOptions(discovery.DockerfileOptions{
					Runtime:        deployment.RuntimeType,
					Port:           port,
					StartCmd:       startCmd,
					PackageManager: pkgManager,
					Prisma:         prismaInfo,
					BuildArgs:      buildArgKeys,
				})
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
		for k, v := range secretBuildVars {
			val := v
			buildArgs[k] = &val
		}
		if deployment.RuntimeType == "nextjs" {
			for k, v := range runtimeEnvMap {
				if _, exists := buildArgs[k]; !exists {
					val := v
					buildArgs[k] = &val
				}
			}
		}
		if len(buildArgs) > 0 {
			emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Injected %d build argument(s) into builder stage.", len(buildArgs)))
		}

		buildOpts := types.ImageBuildOptions{
			Tags:        []string{*deployment.ImageTag},
			Dockerfile:  relDockerPath,
			BuildArgs:   buildArgs,
			Remove:      true,
			ForceRemove: true,
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

	envMap := runtimeEnvMap
	if envMap == nil && e.secretService != nil && len(deployment.EnvSnapshot) == 0 {
		envMap, _, _ = e.secretService.GetDecryptedEnvMap(ctx, project.ID, nil, models.EnvScopeRuntime)
	}

	if len(deployment.EnvSnapshot) == 0 && len(envMap) == 0 && e.projectService != nil && project.SourceType == models.SourceTypeLocalAgent {
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
	if len(project.Services) > 0 && strings.TrimSpace(project.Services[0].User) != "" {
		containerConfig.User = strings.TrimSpace(project.Services[0].User)
	}

	hostConfig, err := ValidateAndBuildSecureHostConfig(ContainerSecurityOptions{
		PortBindings: nat.PortMap{
			nat.Port(targetPortStr): []nat.PortBinding{
				{
					HostIP:   e.GetAppHostIP(),
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
						nat.Port(targetPortStr): []nat.PortBinding{{HostIP: e.GetAppHostIP(), HostPort: fmt.Sprintf("%d", allocatedPort)}},
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
						nat.Port(targetPortStr): []nat.PortBinding{{HostIP: e.GetAppHostIP(), HostPort: fmt.Sprintf("%d", allocatedPort)}},
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
