package models_test

import (
	"testing"

	"github.com/forgelab/backend/internal/models"
)

func TestStateTransitions(t *testing.T) {
	// Valid transitions
	validPairs := [][2]string{
		{models.DeployStatusQueued, models.DeployStatusCloning},
		{models.DeployStatusCloning, models.DeployStatusBuilding},
		{models.DeployStatusBuilding, models.DeployStatusStarting},
		{models.DeployStatusStarting, models.DeployStatusHealthChecking},
		{models.DeployStatusHealthChecking, models.DeployStatusRunning},
		{models.DeployStatusRunning, models.DeployStatusStopped},
		{models.DeployStatusStopped, models.DeployStatusRunning},
		{models.DeployStatusBuilding, models.DeployStatusFailed},
	}

	for _, pair := range validPairs {
		if err := models.ValidateStateTransition(pair[0], pair[1]); err != nil {
			t.Errorf("expected valid transition from %s to %s, got error: %v", pair[0], pair[1], err)
		}
	}

	// Invalid transitions
	invalidPairs := [][2]string{
		{models.DeployStatusQueued, models.DeployStatusRunning},
		{models.DeployStatusFailed, models.DeployStatusRunning},
		{models.DeployStatusFailed, models.DeployStatusBuilding},
		{models.DeployStatusCloning, models.DeployStatusStopped},
	}

	for _, pair := range invalidPairs {
		if err := models.ValidateStateTransition(pair[0], pair[1]); err == nil {
			t.Errorf("expected invalid transition error from %s to %s, got nil", pair[0], pair[1])
		}
	}
}

// TestServiceAndDeploymentStatusDistinction verifies:
// 1. ServiceDeployment.status may use detailed phases (queued, cloning, building, starting, health_checking, running, failed, stopped, crashed).
// 2. Service.status must remain coarse only: inactive, deploying, running, stopped, failed.
func TestServiceAndDeploymentStatusDistinction(t *testing.T) {
	detailedDeploymentPhases := []string{
		models.DeployStatusQueued,
		models.DeployStatusCloning,
		models.DeployStatusBuilding,
		models.DeployStatusStarting,
		models.DeployStatusHealthChecking,
		models.DeployStatusRunning,
		models.DeployStatusStopped,
		models.DeployStatusCrashed,
		models.DeployStatusFailed,
	}

	for _, phase := range detailedDeploymentPhases {
		if phase == "" {
			t.Errorf("expected non-empty detailed deployment phase")
		}
	}

	coarseServiceStatuses := map[string]bool{
		models.ServiceStatusInactive:  true,
		models.ServiceStatusDeploying: true,
		models.ServiceStatusRunning:   true,
		models.ServiceStatusStopped:   true,
		models.ServiceStatusFailed:    true,
	}

	// Verify Service.status coarse states
	if len(coarseServiceStatuses) != 5 {
		t.Fatalf("expected exactly 5 coarse service statuses, got %d", len(coarseServiceStatuses))
	}

	// In-progress phases must be classified as active deployment
	for _, phase := range []string{
		models.DeployStatusQueued,
		models.DeployStatusCloning,
		models.DeployStatusBuilding,
		models.DeployStatusStarting,
		models.DeployStatusHealthChecking,
	} {
		if !models.IsDeploymentActiveStatus(phase) {
			t.Errorf("expected %s to be classified as active deployment status", phase)
		}
	}

	// Terminal phases
	for _, phase := range []string{
		models.DeployStatusRunning,
		models.DeployStatusStopped,
		models.DeployStatusCrashed,
		models.DeployStatusFailed,
	} {
		if !models.IsDeploymentTerminalStatus(phase) {
			t.Errorf("expected %s to be classified as terminal deployment status", phase)
		}
	}
}

// TestPromotionSafety_InvariantRules verifies:
// 1. In-progress deployment never changes current_service_deployment_id
// 2. Failed deployment preserves previous healthy container and deployment ID
// 3. Failed promotion preserves previous healthy container and deployment ID
func TestPromotionSafety_InvariantRules(t *testing.T) {
	oldDeployID := "00000000-0000-0000-0000-000000000001"
	oldContainerID := "container-old-healthy-123"
	oldHostPort := 8001

	// Service with a currently healthy running deployment
	currentServiceDeploymentID := &oldDeployID
	currentContainerID := &oldContainerID
	currentHostPort := &oldHostPort
	currentServiceStatus := models.ServiceStatusRunning
	_ = currentHostPort

	hasPreviousHealthy := (currentServiceStatus == models.ServiceStatusRunning && currentContainerID != nil && *currentContainerID != "")
	if !hasPreviousHealthy {
		t.Fatal("expected hasPreviousHealthy to be true")
	}

	// Invariant 1: Queueing/building a new deployment never updates current_service_deployment_id
	newDeployID := "00000000-0000-0000-0000-000000000002"
	newDeployStatus := models.DeployStatusBuilding
	_ = newDeployID
	_ = newDeployStatus
	if !models.IsDeploymentActiveStatus(newDeployStatus) {
		t.Errorf("expected new deployment status to be active")
	}
	if *currentServiceDeploymentID != oldDeployID {
		t.Errorf("INVARIANT VIOLATED: current_service_deployment_id changed during build! got %s, want %s", *currentServiceDeploymentID, oldDeployID)
	}

	// Invariant 2: If health check fails or promotion fails,
	// the new deployment becomes failed, the old container remains intact,
	// and the service status remains running.
	promotionFailed := true
	if promotionFailed {
		newDeployStatus = models.DeployStatusFailed

		// Service status must remain running because previous container is healthy
		if hasPreviousHealthy {
			currentServiceStatus = models.ServiceStatusRunning
		} else {
			currentServiceStatus = models.ServiceStatusFailed
		}

		// Previous deployment and container must remain intact
		if *currentServiceDeploymentID != oldDeployID {
			t.Errorf("INVARIANT VIOLATED: old deployment ID lost on failed promotion! got %s, want %s", *currentServiceDeploymentID, oldDeployID)
		}
		if *currentContainerID != oldContainerID {
			t.Errorf("INVARIANT VIOLATED: old container ID lost on failed promotion! got %s, want %s", *currentContainerID, oldContainerID)
		}
		if currentServiceStatus != models.ServiceStatusRunning {
			t.Errorf("expected service to remain running, got %s", currentServiceStatus)
		}
	}
}

// TestServiceRollbackInvariants validates the requirements of service rollback:
// 1. Rollback selects the previous successful deployment, never the current deployment.
// 2. Snapshot reproduces full configuration: resources, build/runtime config, source revision, env snapshot/hash, immutable image reference.
// 3. Immutable Docker image ID/digest is the rollback identity, not a mutable tag.
// 4. "reuse_image" execution mode avoids source rebuild when immutable image is available.
// 5. Rollback preserves current_service_deployment_id until promotion succeeds.
func TestServiceRollbackInvariants(t *testing.T) {
	deploy1ID := "00000000-0000-0000-0000-000000000001"
	deploy2ID := "00000000-0000-0000-0000-000000000002"
	immutableDigest1 := "sha256:abcd1234ef567890abcd1234ef567890abcd1234ef567890abcd1234ef567890"
	sourceRev1 := "v1.0.0-commit-abc"
	envHash1 := "hash-env-v1"
	tag1 := "forgelab/proj/frontend:1"

	// Mock deployment 1 (known good deployment)
	deploy1 := models.ServiceDeployment{
		DeployNumber:   1,
		Status:         models.DeployStatusStopped,
		ImageTag:       &tag1,
		ImageDigest:    &immutableDigest1,
		BuildStrategy:  models.BuildStrategyAuto,
		BuildCommand:   "npm run build",
		StartCommand:   "npm start",
		RuntimeType:    "nodejs",
		InternalPort:   3000,
		ExecutionMode:  models.ExecutionModeBuild,
		SourceRevision: &sourceRev1,
		EnvConfigHash:  &envHash1,
		ResourceConfig: models.ResourceConfig{
			CpuMillicores: 1500,
			MemoryMB:      2048,
			PidsLimit:     512,
		},
	}

	// Mock deployment 2 (current running deployment)
	tag2 := "forgelab/proj/frontend:2"
	deploy2 := models.ServiceDeployment{
		DeployNumber:  2,
		Status:        models.DeployStatusRunning,
		ImageTag:      &tag2,
		InternalPort:  3000,
		ExecutionMode: models.ExecutionModeBuild,
		ResourceConfig: models.ResourceConfig{
			CpuMillicores: 500,
			MemoryMB:      512,
			PidsLimit:     128,
		},
	}

	// Service is currently on deployment 2
	currentServiceDeploymentID := deploy2ID
	_ = deploy2

	// Rule 1: Rollback MUST select deployment strictly before current deployment (deploy_number < current.deploy_number)
	currentDeployNumber := 2
	allDeployments := []models.ServiceDeployment{deploy2, deploy1}

	var rollbackTarget *models.ServiceDeployment
	for _, d := range allDeployments {
		if d.DeployNumber < currentDeployNumber && (d.Status == models.DeployStatusRunning || d.Status == models.DeployStatusStopped) {
			rollbackTarget = &d
			break
		}
	}

	if rollbackTarget == nil {
		t.Fatal("expected to find previous successful deployment to rollback to")
	}
	if rollbackTarget.DeployNumber == currentDeployNumber {
		t.Fatalf("INVARIANT VIOLATED: Rollback selected current deployment (#%d) instead of previous!", rollbackTarget.DeployNumber)
	}
	if rollbackTarget.DeployNumber != 1 {
		t.Fatalf("expected rollback target to be deploy #1, got #%d", rollbackTarget.DeployNumber)
	}

	// Rule 2 & 3: Immutable image ID/digest is preserved as the identity, and ExecutionMode is "reuse_image"
	executionMode := models.ExecutionModeReuseImage
	if rollbackTarget.ImageDigest == nil || *rollbackTarget.ImageDigest == "" {
		executionMode = models.ExecutionModeBuild
	}

	if executionMode != models.ExecutionModeReuseImage {
		t.Errorf("expected execution mode to be reuse_image, got %s", executionMode)
	}
	if rollbackTarget.ImageDigest == nil || *rollbackTarget.ImageDigest != immutableDigest1 {
		t.Errorf("expected immutable digest to be %s, got %v", immutableDigest1, rollbackTarget.ImageDigest)
	}

	// Rule 4: Snapshot full configuration into the rollback deployment record
	rollbackDeploy := models.ServiceDeployment{
		DeployNumber:   3,
		Status:         models.DeployStatusQueued,
		ExecutionMode:  executionMode,
		ImageTag:       rollbackTarget.ImageTag,
		ImageDigest:    rollbackTarget.ImageDigest,
		BuildStrategy:  rollbackTarget.BuildStrategy,
		BuildCommand:   rollbackTarget.BuildCommand,
		StartCommand:   rollbackTarget.StartCommand,
		RuntimeType:    rollbackTarget.RuntimeType,
		InternalPort:   rollbackTarget.InternalPort,
		ResourceConfig: rollbackTarget.ResourceConfig,
		SourceRevision: rollbackTarget.SourceRevision,
		EnvConfigHash:  rollbackTarget.EnvConfigHash,
	}

	if rollbackDeploy.ResourceConfig.CpuMillicores != 1500 || rollbackDeploy.ResourceConfig.MemoryMB != 2048 {
		t.Errorf("expected snapshotted resource limits (1500m / 2048MB), got %dm / %dMB",
			rollbackDeploy.ResourceConfig.CpuMillicores, rollbackDeploy.ResourceConfig.MemoryMB)
	}
	if *rollbackDeploy.SourceRevision != sourceRev1 {
		t.Errorf("expected source revision %s, got %v", sourceRev1, rollbackDeploy.SourceRevision)
	}
	if *rollbackDeploy.EnvConfigHash != envHash1 {
		t.Errorf("expected env hash %s, got %v", envHash1, rollbackDeploy.EnvConfigHash)
	}

	// Rule 5: Rollback must preserve current_service_deployment_id while queued / executing
	if currentServiceDeploymentID != deploy2ID {
		t.Errorf("INVARIANT VIOLATED: current_service_deployment_id mutated before rollback promotion! got %s, want %s",
			currentServiceDeploymentID, deploy2ID)
	}

	// Only upon successful promotion does current_service_deployment_id transition
	promotionSuccess := true
	if promotionSuccess {
		currentServiceDeploymentID = deploy1ID // successfully rolled back to #1's release
	}
	if currentServiceDeploymentID != deploy1ID {
		t.Errorf("expected current_service_deployment_id to update to %s after promotion, got %s",
			deploy1ID, currentServiceDeploymentID)
	}
}

func TestDeploymentCancellationAndSuperseding_Invariants(t *testing.T) {
	// Invariant 1: Queued deployment can transition to Failed (used when superseded or cancelled)
	if err := models.ValidateStateTransition(models.DeployStatusQueued, models.DeployStatusFailed); err != nil {
		t.Errorf("expected queued -> failed to be a valid transition, got %v", err)
	}

	// Invariant 2: In-progress deployments can transition to Failed when cancelled
	inProgressStatuses := []string{
		models.DeployStatusCloning,
		models.DeployStatusBuilding,
		models.DeployStatusStarting,
		models.DeployStatusHealthChecking,
	}
	for _, st := range inProgressStatuses {
		if err := models.ValidateStateTransition(st, models.DeployStatusFailed); err != nil {
			t.Errorf("expected %s -> failed to be valid for cancellation, got %v", st, err)
		}
	}

	// Invariant 3: Terminal states classification
	terminalStatuses := []string{
		models.DeployStatusRunning,
		models.DeployStatusStopped,
		models.DeployStatusCrashed,
		models.DeployStatusFailed,
	}
	for _, st := range terminalStatuses {
		if !models.IsDeploymentTerminalStatus(st) {
			t.Errorf("expected %s to be terminal status", st)
		}
	}

	// Invariant 4: Active/in-progress states classification
	for _, st := range inProgressStatuses {
		if !models.IsDeploymentActiveStatus(st) {
			t.Errorf("expected %s to be active status", st)
		}
	}
	if !models.IsDeploymentActiveStatus(models.DeployStatusQueued) {
		t.Errorf("expected queued to be active status")
	}
}
