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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/forgelab/backend/internal/agent"
	"github.com/forgelab/backend/internal/detector"
	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/logging"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/proxy"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/tararchive"
	ws "github.com/forgelab/backend/internal/websocket"
)

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
			if resetErr := e.deploymentService.ResetInterruptedServiceDeployment(ctx, serviceDeploymentID); resetErr != nil {
				return fmt.Errorf("failed to reset interrupted service deployment %s: %w", serviceDeploymentID, resetErr)
			}
			serviceDeploy.Status = models.DeployStatusQueued
			serviceDeploy.FailureReason = nil
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
	previousHostPort := service.HostPort
	hasPreviousHealthy := ((previousServiceStatus == models.ServiceStatusRunning || previousServiceStatus == models.ServiceStatusDegraded) && previousContainerID != nil && *previousContainerID != "")

	deployMode := strings.ToLower(strings.TrimSpace(service.DeploymentMode))
	if deployMode == "" && project != nil {
		deployMode = strings.ToLower(strings.TrimSpace(project.DeploymentMode))
	}
	if deployMode == "" {
		deployMode = models.DeploymentModeStandard
	}
	isBlueGreen := deployMode == models.DeploymentModeBlueGreen

	redactor := logging.NewLogRedactor()

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
		if err := e.serviceService.UpdateServiceStatus(ctx, service.ID, coarseStatus, nil, nil, nil); err != nil {
			slog.Warn("failed to update coarse service status", "service_id", service.ID, "status", coarseStatus, "error", err)
		}
		newProjStatus, err := e.recalculateAndUpdateProjectStatus(ctx, project.ID)
		if err != nil {
			slog.Warn("failed to recalculate project status", "project_id", project.ID, "error", err)
		}

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

	var runtimeEnvMap map[string]string
	var normalBuildArgs map[string]string
	var secretBuildVars map[string]string

	if e.secretService != nil {
		var envErr error
		if len(serviceDeploy.EnvSnapshot) > 0 {
			runtimeEnvMap, _, envErr = e.secretService.GetEnvMapFromSnapshot(serviceDeploy.EnvSnapshot, models.EnvScopeRuntime)
			if envErr != nil {
				failReason := fmt.Sprintf("failed to decrypt environment snapshot: %v", envErr)
				emitLog("init", "stderr", failReason)
				emitStatus(models.DeployStatusFailed, nil, &failReason)
				return fmt.Errorf("failed to get env map from snapshot: %w", envErr)
			}
		} else {
			runtimeEnvMap, _, envErr = e.secretService.GetDecryptedEnvMap(execCtx, project.ID, &service.ID, models.EnvScopeRuntime)
			if envErr != nil {
				failReason := fmt.Sprintf("failed to decrypt environment variables: %v", envErr)
				emitLog("init", "stderr", failReason)
				emitStatus(models.DeployStatusFailed, nil, &failReason)
				return fmt.Errorf("failed to get decrypted env map: %w", envErr)
			}
		}
		normalArgs, secretVars, allSecrets, buildVarErr := e.secretService.GetBuildVariables(execCtx, project.ID, &service.ID, serviceDeploy.EnvSnapshot)
		if buildVarErr != nil {
			failReason := fmt.Sprintf("failed to process build environment variables: %v", buildVarErr)
			emitLog("init", "stderr", failReason)
			emitStatus(models.DeployStatusFailed, nil, &failReason)
			return fmt.Errorf("failed to get build variables: %w", buildVarErr)
		}
		normalBuildArgs = normalArgs
		secretBuildVars = secretVars
		redactor.SetSecrets(allSecrets)
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
		reason := fmt.Sprintf("Failed to load services for project '%s': %v", project.Name, err)
		emitLog(models.LogPhaseSource, models.LogStreamStderr, reason)
		emitStatus(models.DeployStatusFailed, nil, &reason)
		return fmt.Errorf("failed to list services for project %s: %w", project.ID, err)
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
			if inspect, err := e.dockerClient.ImageInspect(execCtx, candidateImage); err == nil {
				reusingExistingImage = true
				runImage = candidateImage
				idShort := inspect.ID
				if len(idShort) > 12 {
					idShort = idShort[:12]
				}
				emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Execution path: reusing existing immutable image '%s' (image ID: %s). Skipping source cloning and image build.", candidateImage, idShort))
				_, _ = e.dockerClient.ImageTag(execCtx, client.ImageTagOptions{
					Source: candidateImage,
					Target: svcTag,
				})
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
			reader, pullErr := e.dockerClient.ImagePull(execCtx, targetImage, client.ImagePullOptions{})
			if pullErr == nil && reader != nil {
				_, _ = io.Copy(io.Discard, reader)
				_ = reader.Close()
				emitLog(models.LogPhaseBuild, models.LogStreamSystem, fmt.Sprintf("Image '%s' ready. Skipping source build.", targetImage))
			} else {
				// Image pull failed: check if image is available in local cache
				_, inspectErr := e.dockerClient.ImageInspect(execCtx, targetImage)
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
			for k, v := range secretBuildVars {
				val := v
				buildArgsMap[k] = &val
			}
			if service.RuntimeType == "nextjs" {
				for k, v := range runtimeEnvMap {
					if _, exists := buildArgsMap[k]; !exists {
						val := v
						buildArgsMap[k] = &val
					}
				}
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
							_, _ = e.dockerClient.ImageTag(execCtx, client.ImageTagOptions{
								Source: tag,
								Target: svcTag,
							})
							runImage = svcTag
							if service.Image != "" {
								_, _ = e.dockerClient.ImageTag(execCtx, client.ImageTagOptions{
									Source: tag,
									Target: service.Image,
								})
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
		if bv, ok := e.getCachedBuilderVersion(); ok && bv == build.BuilderBuildKit {
			emitLog(models.LogPhaseBuild, models.LogStreamSystem, "Builder version: 2 / BuildKit")
		} else if bv, ok := e.getCachedBuilderVersion(); ok && bv == build.BuilderV1 {
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
				var prismaInfo *discovery.PrismaStagingInfo
				if svcContextDir != "" {
					prismaInfo, _ = detector.DetectPrismaFromDir(svcContextDir)
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
				if service.RuntimeType == "nextjs" {
					for k := range runtimeEnvMap {
						if !seenArgKeys[k] {
							seenArgKeys[k] = true
							buildArgKeys = append(buildArgKeys, k)
						}
					}
				}
				sort.Strings(buildArgKeys)
				generatedContent := detector.GenerateDockerfileWithOptions(discovery.DockerfileOptions{
					Runtime:        service.RuntimeType,
					Port:           intPort,
					StartCmd:       service.StartCommand,
					PackageManager: pkgManager,
					Prisma:         prismaInfo,
					BuildArgs:      buildArgKeys,
				})
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
		for k, v := range secretBuildVars {
			val := v
			buildArgs[k] = &val
		}
		if service.RuntimeType == "nextjs" {
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

		buildOpts := client.ImageBuildOptions{
			Tags:        []string{svcTag},
			Dockerfile:  relDockerPath,
			BuildArgs:   buildArgs,
			Remove:      true,
			ForceRemove: true,
			Labels:      buildLabels,
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
			if inspect, err := e.dockerClient.ImageInspect(ctx, svcTag); err == nil {
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
			_, _ = e.dockerClient.ImageTag(ctx, client.ImageTagOptions{
				Source: svcTag,
				Target: service.Image,
			})
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
	var portBindings dockernetwork.PortMap
	targetPortStr := fmt.Sprintf("%d/tcp", intPort)
	targetPort := dockernetwork.MustParsePort(targetPortStr)

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
		if e.portManager == nil {
			reason := fmt.Sprintf("Failed to allocate host port for exposed service '%s': port manager is not configured", service.Name)
			emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return errors.New(reason)
		}
		allocated, err := e.portManager.AllocatePort()
		if err != nil {
			reason := fmt.Sprintf("Failed to allocate host port for exposed service '%s': %v", service.Name, err)
			emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return errors.New(reason)
		}
		hostPort = &allocated
		portBindings = dockernetwork.PortMap{
			targetPort: []dockernetwork.PortBinding{
				{
					HostIP:   parseAppHostIP(e.GetAppHostIP()),
					HostPort: fmt.Sprintf("%d", allocated),
				},
			},
		}
	}

	// Auto-heal missing environment variables for local agent projects when no snapshot exists:
	if len(serviceDeploy.EnvSnapshot) == 0 && len(runtimeEnvMap) == 0 && e.projectService != nil && project.SourceType == models.SourceTypeLocalAgent {
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
		ExposedPorts: dockernetwork.PortSet{
			targetPort: struct{}{},
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

	runUser := strings.TrimSpace(serviceDeploy.User)
	if runUser == "" && service != nil {
		runUser = strings.TrimSpace(service.User)
	}
	if runUser != "" {
		containerConfig.User = runUser
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
				_, _ = e.dockerClient.VolumeCreate(execCtx, client.VolumeCreateOptions{
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
	_, _ = e.dockerClient.ContainerRemove(cleanupCtx, containerName, client.ContainerRemoveOptions{Force: true})
	cleanupCancel()

	hasWritableVolume := false
	for _, v := range service.Volumes {
		if !v.ReadOnly {
			hasWritableVolume = true
			break
		}
	}
	if !hasWritableVolume {
		for _, v := range serviceDeploy.Volumes {
			if !v.ReadOnly {
				hasWritableVolume = true
				break
			}
		}
	}
	isStateful := hasWritableVolume || len(binds) > 0 || service.Classification == models.ClassificationInfrastructure

	if isBlueGreen && hasPreviousHealthy && e.trafficRouter != nil && previousHostPort != nil && *previousHostPort > 0 && !isInternalService {
		_, _, _ = e.trafficRouter.EnsureRoute(service.ID, project.ID, service.Name, e.GetAppHostIP(), *previousHostPort, *previousContainerID)
	}

	var stoppedPreviousForStateful bool
	if isStateful && hasPreviousHealthy && previousContainerID != nil && *previousContainerID != "" {
		if isBlueGreen {
			emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("BLUE-GREEN: Service '%s' has writable storage/infrastructure state; using safe stop-before-start fallback with automatic restore-on-failure.", service.Name))
		}
		emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Stopping previous container %s for stateful service '%s' to release volume locks...", (*previousContainerID)[:min(12, len(*previousContainerID))], service.Name))
		if previousDeploymentID != nil {
			e.StopLogCollector(*previousDeploymentID)
		}
		stopTimeoutSec := 15
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
		_, _ = e.dockerClient.ContainerStop(stopCtx, *previousContainerID, client.ContainerStopOptions{Timeout: &stopTimeoutSec})
		stopCancel()
		stoppedPreviousForStateful = true
	} else if isBlueGreen && hasPreviousHealthy && !isStateful && previousContainerID != nil && *previousContainerID != "" {
		emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("BLUE-GREEN: Keeping previous Blue container %s for service '%s' live while provisioning Green replacement (%s)...", (*previousContainerID)[:min(12, len(*previousContainerID))], service.Name, containerName))
	}

	restorePreviousIfNeeded := func(failureReason, phase string) string {
		if stoppedPreviousForStateful && previousContainerID != nil && *previousContainerID != "" {
			res := e.RestorePreviousHealthyContainer(ctx, service, previousContainerID, previousDeploymentID, emitLog)
			if !res.Restored {
				hasPreviousHealthy = false
				return fmt.Sprintf("%s; RESTORATION FAILED: previous container could not be restored (%v)", failureReason, res.Error)
			}
		} else if hasPreviousHealthy {
			emitLog(phase, models.LogStreamSystem, fmt.Sprintf("SAFETY INVARIANT ENFORCED: New deployment for '%s' failed, but previous healthy container remains active!", service.Name))
		}
		return failureReason
	}

	var containerID string
	maxPortRetries := 3
	for attempt := 0; attempt <= maxPortRetries; attempt++ {
		resp, err := e.dockerClient.ContainerCreate(execCtx, client.ContainerCreateOptions{
			Config:           containerConfig,
			HostConfig:       hostConfig,
			NetworkingConfig: netConfig,
			Name:             containerName,
		})
		if err != nil {
			if isPortConflictError(err) && hostPort != nil && attempt < maxPortRetries {
				emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Host port %d conflict during container create: %v. Retrying with a new port (attempt %d/%d)...", *hostPort, err, attempt+1, maxPortRetries))
				e.portManager.RegisterUsedPort(*hostPort)
				newPort, allocErr := e.portManager.AllocatePort()
				if allocErr == nil {
					hostPort = &newPort
					portBindings[targetPort] = []dockernetwork.PortBinding{{HostIP: parseAppHostIP(e.GetAppHostIP()), HostPort: fmt.Sprintf("%d", newPort)}}
					hostConfig.PortBindings = portBindings
					continue
				}
			}
			if hostPort != nil {
				e.portManager.ReleasePort(*hostPort)
			}
			reason := fmt.Sprintf("Failed to create container for service '%s': %v", service.Name, err)
			reason = restorePreviousIfNeeded(reason, models.LogPhaseStartup)
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

				if _, netConnErr := e.dockerClient.NetworkConnect(execCtx, secNet, client.NetworkConnectOptions{
					Container: resp.ID,
					EndpointConfig: &dockernetwork.EndpointSettings{
						Aliases: secAliases,
					},
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
			_, _ = e.dockerClient.ContainerRemove(cleanupCtx, resp.ID, client.ContainerRemoveOptions{Force: true})
			cleanupCancel()
			reason := secNetErr.Error()
			reason = restorePreviousIfNeeded(reason, models.LogPhaseStartup)
			emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return errors.New(reason)
		}

		if _, err := e.dockerClient.ContainerStart(execCtx, containerID, client.ContainerStartOptions{}); err != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, _ = e.dockerClient.ContainerRemove(cleanupCtx, containerID, client.ContainerRemoveOptions{Force: true})
			cleanupCancel()

			if isPortConflictError(err) && hostPort != nil && attempt < maxPortRetries {
				emitLog(models.LogPhaseStartup, models.LogStreamSystem, fmt.Sprintf("Host port %d conflict during container start: %v. Retrying with a new port (attempt %d/%d)...", *hostPort, err, attempt+1, maxPortRetries))
				e.portManager.RegisterUsedPort(*hostPort)
				newPort, allocErr := e.portManager.AllocatePort()
				if allocErr == nil {
					hostPort = &newPort
					portBindings[targetPort] = []dockernetwork.PortBinding{{HostIP: parseAppHostIP(e.GetAppHostIP()), HostPort: fmt.Sprintf("%d", newPort)}}
					hostConfig.PortBindings = portBindings
					continue
				}
			}

			if hostPort != nil {
				e.portManager.ReleasePort(*hostPort)
			}
			reason := fmt.Sprintf("Failed to start container for service '%s': %v", service.Name, err)
			reason = restorePreviousIfNeeded(reason, models.LogPhaseStartup)
			emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
			emitStatus(models.DeployStatusFailed, nil, &reason)
			return errors.New(reason)
		}

		break
	}

	e.trackActiveContainer(containerID)
	defer e.untrackActiveContainer(containerID)
	if err := e.deploymentService.UpdateServiceDeploymentContainer(execCtx, serviceDeploy.ID, containerID, hostPort); err != nil {
		slog.Warn("failed to record service deployment container metadata", "service_deployment_id", serviceDeploy.ID, "container_id", containerID, "error", err)
	}

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
			waitRes := e.dockerClient.ContainerWait(execCtx, containerID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
			select {
			case err := <-waitRes.Error:
				if err != nil {
					e.StopLogCollector(serviceDeploy.ID)
					e.StopContainerLogCollector(containerID)
					reason := fmt.Sprintf("Failed waiting for job container '%s': %v", service.Name, err)
					emitLog(models.LogPhaseStartup, models.LogStreamStderr, reason)
					emitStatus(models.DeployStatusFailed, nil, &reason)
					return errors.New(reason)
				}
			case status := <-waitRes.Result:
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
		if err := e.deploymentService.UpdateServiceDeploymentContainer(ctx, serviceDeploy.ID, containerID, nil); err != nil {
			slog.Warn("failed to record job service deployment container", "service_deployment_id", serviceDeploy.ID, "error", err)
		}
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
		if isBlueGreen && hasPreviousHealthy && !isStateful {
			emitLog(models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("BLUE-GREEN ABORTED: Green replacement for '%s' failed health verification; proxy traffic remains safely routed to Blue.", service.Name))
		}

		// Destroy newly spawned broken container and preserve previously healthy container intact!
		e.StopLogCollector(serviceDeploy.ID)
		e.StopContainerLogCollector(containerID)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _ = e.dockerClient.ContainerStop(cleanupCtx, containerID, client.ContainerStopOptions{})
		_, _ = e.dockerClient.ContainerRemove(cleanupCtx, containerID, client.ContainerRemoveOptions{Force: true})
		cleanupCancel()
		if hostPort != nil {
			e.portManager.ReleasePort(*hostPort)
		}

		detailedReason = restorePreviousIfNeeded(detailedReason, models.LogPhaseHealth)
		emitStatus(models.DeployStatusFailed, nil, &detailedReason)
		return errors.New(detailedReason)
	}

	// Guard against stale worker whose lease expired or context was cancelled
	if execCtx.Err() != nil {
		reason := fmt.Sprintf("Service deployment #%d aborted: worker context cancelled or lease lost", serviceDeploy.DeployNumber)
		emitStatus(models.DeployStatusFailed, nil, &reason)
		return errors.New(reason)
	}

	// 5. Safe Traffic Switch & Promote Service
	var prevRoute proxy.UpstreamRoute
	var hadPrevRoute bool
	var switchedTraffic bool
	if e.trafficRouter != nil && hostPort != nil && *hostPort > 0 && !isInternalService {
		prevRoute, hadPrevRoute = e.trafficRouter.GetRoute(service.ID)
		newRoute, switchErr := e.trafficRouter.SwitchTraffic(service.ID, project.ID, service.Name, e.GetAppHostIP(), *hostPort, containerID)
		if switchErr != nil {
			slog.Warn("failed to switch traffic router upstream", "service_id", service.ID, "error", switchErr)
		} else {
			switchedTraffic = true
			if isBlueGreen {
				emitLog(models.LogPhaseHealth, models.LogStreamSystem, fmt.Sprintf("BLUE-GREEN CUTOVER: Switched active proxy slot to '%s' (container %s on host port %d).", newRoute.ActiveSlot, containerID[:min(12, len(containerID))], *hostPort))
			}
		}
	}

	err = e.serviceService.PromoteServiceDeployment(execCtx, service.ID, serviceDeploy.ID, containerID, svcTag, hostPort)
	if err != nil {
		// Promotion failed - ENFORCE SAFETY INVARIANT:
		// Revert any traffic router switch, destroy new container, preserve previous container intact.
		if switchedTraffic && e.trafficRouter != nil {
			if hadPrevRoute {
				_ = e.trafficRouter.RestoreRoute(prevRoute)
			} else {
				e.trafficRouter.RemoveRoute(service.ID)
			}
		}
		e.StopLogCollector(serviceDeploy.ID)
		e.StopContainerLogCollector(containerID)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _ = e.dockerClient.ContainerStop(cleanupCtx, containerID, client.ContainerStopOptions{})
		_, _ = e.dockerClient.ContainerRemove(cleanupCtx, containerID, client.ContainerRemoveOptions{Force: true})
		cleanupCancel()
		if hostPort != nil {
			e.portManager.ReleasePort(*hostPort)
		}

		reason := fmt.Sprintf("Failed to promote service deployment '%s': %v", service.Name, err)
		emitLog(models.LogPhaseHealth, models.LogStreamStderr, reason)
		reason = restorePreviousIfNeeded(reason, models.LogPhaseHealth)
		emitStatus(models.DeployStatusFailed, nil, &reason)
		return fmt.Errorf("failed to promote service deployment: %w", err)
	}

	if err := e.deploymentService.UpdateServiceDeploymentContainer(execCtx, serviceDeploy.ID, containerID, hostPort); err != nil {
		slog.Warn("failed to update service deployment container on promotion", "service_deployment_id", serviceDeploy.ID, "error", err)
	}
	emitStatus(models.DeployStatusRunning, hostPort, nil)

	// 6. Cleanup previous container for this service if different
	if previousContainerID != nil && *previousContainerID != "" && *previousContainerID != containerID {
		if serviceDeploy.DeploymentID == nil {
			// Single service deployment: safe to clean up immediately upon promotion and traffic cutover
			if !stoppedPreviousForStateful {
				if isBlueGreen && hasPreviousHealthy {
					emitLog(models.LogPhaseRuntime, models.LogStreamSystem, fmt.Sprintf("BLUE-GREEN: Draining active connections before decommissioning previous Blue container %s for service '%s'...", (*previousContainerID)[:min(12, len(*previousContainerID))], service.Name))
					time.Sleep(50 * time.Millisecond)
				} else {
					emitLog(models.LogPhaseRuntime, models.LogStreamSystem, fmt.Sprintf("Stopping previous container %s for service '%s'...", (*previousContainerID)[:min(12, len(*previousContainerID))], service.Name))
				}
				if previousDeploymentID != nil {
					e.StopLogCollector(*previousDeploymentID)
				}
				e.StopContainerLogCollector(*previousContainerID)
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
				_, _ = e.dockerClient.ContainerStop(cleanupCtx, *previousContainerID, client.ContainerStopOptions{})
				cleanupCancel()
			} else {
				e.StopContainerLogCollector(*previousContainerID)
			}
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, _ = e.dockerClient.ContainerRemove(cleanupCtx, *previousContainerID, client.ContainerRemoveOptions{Force: true})
			cleanupCancel()
			if e.portManager != nil && previousHostPort != nil && *previousHostPort > 0 && (hostPort == nil || *previousHostPort != *hostPort) {
				e.portManager.ReleasePort(*previousHostPort)
			}
		} else {
			// Release deployment: preserve previous healthy container until full release promotion verification!
			emitLog(models.LogPhaseRuntime, models.LogStreamSystem, fmt.Sprintf("Preserving previous container %s for service '%s' until full release promotion verification.", (*previousContainerID)[:min(12, len(*previousContainerID))], service.Name))
		}
	}

	// Update project port if this service is public or frontend
	if hostPort != nil && (service.Role == models.RoleFrontend || service.PublicExposed) {
		if _, err := e.projectService.UpdateProjectPort(execCtx, project.ID, *hostPort); err != nil {
			slog.Warn("failed to update project port after service promotion", "project_id", project.ID, "host_port", *hostPort, "error", err)
		}
	}

	if e.onRouteChange != nil {
		e.onRouteChange(execCtx)
	}

	emitLog(models.LogPhaseRuntime, models.LogStreamSystem, fmt.Sprintf("Service '%s' deployment #%d is now RUNNING and live!", service.Name, serviceDeploy.DeployNumber))
	return nil
}
