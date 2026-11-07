package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/logging"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/security"
	ws "github.com/forgelab/backend/internal/websocket"
)

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
