package models

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidStateTransition = errors.New("invalid deployment state transition")
)

// AllowedTransitions maps each deployment status to its allowed next statuses.
var AllowedTransitions = map[string]map[string]bool{
	DeployStatusQueued: {
		DeployStatusCloning:  true,
		DeployStatusBuilding: true, // For release deployments or builds skipping clone
		DeployStatusStarting: true, // For infrastructure / pre-built images skipping clone and build
		DeployStatusFailed:   true,
		DeployStatusSkipped:  true,
	},
	DeployStatusCloning: {
		DeployStatusBuilding: true,
		DeployStatusStarting: true, // For repos with pre-built artifacts
		DeployStatusFailed:   true,
		DeployStatusSkipped:  true,
	},
	DeployStatusBuilding: {
		DeployStatusStarting: true,
		DeployStatusRunning:  true, // For release deployments after all services have completed
		DeployStatusStopped:  true, // For build-only or compile jobs
		DeployStatusFailed:   true,
		DeployStatusSkipped:  true,
	},
	DeployStatusStarting: {
		DeployStatusHealthChecking: true,
		DeployStatusRunning:        true, // For services without health checks
		DeployStatusStopped:        true, // For one-shot jobs (e.g. database migrations) that exit cleanly
		DeployStatusFailed:         true,
	},
	DeployStatusHealthChecking: {
		DeployStatusRunning: true,
		DeployStatusStopped: true, // If service is stopped during health check
		DeployStatusFailed:  true,
	},
	DeployStatusRunning: {
		DeployStatusStopped: true,
		DeployStatusCrashed: true,
		DeployStatusFailed:  true,
	},
	DeployStatusStopped: {
		DeployStatusStarting: true,
		DeployStatusRunning:  true,
		DeployStatusFailed:   true,
	},
	DeployStatusCrashed: {
		DeployStatusStarting: true,
		DeployStatusRunning:  true,
		DeployStatusFailed:   true,
	},
	DeployStatusFailed:  {}, // Terminal
	DeployStatusSkipped: {}, // Terminal
}

// ValidateStateTransition checks whether transitioning from currentStatus to nextStatus is valid.
func ValidateStateTransition(currentStatus, nextStatus string) error {
	if currentStatus == nextStatus {
		return nil
	}

	allowed, exists := AllowedTransitions[currentStatus]
	if !exists || !allowed[nextStatus] {
		return fmt.Errorf("%w: cannot transition from %s to %s", ErrInvalidStateTransition, currentStatus, nextStatus)
	}

	return nil
}
