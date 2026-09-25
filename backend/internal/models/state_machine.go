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
		DeployStatusCloning: true,
		DeployStatusFailed:  true,
	},
	DeployStatusCloning: {
		DeployStatusBuilding: true,
		DeployStatusFailed:   true,
	},
	DeployStatusBuilding: {
		DeployStatusStarting: true,
		DeployStatusFailed:   true,
	},
	DeployStatusStarting: {
		DeployStatusHealthChecking: true,
		DeployStatusFailed:         true,
	},
	DeployStatusHealthChecking: {
		DeployStatusRunning: true,
		DeployStatusFailed:  true,
	},
	DeployStatusRunning: {
		DeployStatusStopped: true,
		DeployStatusCrashed: true,
		DeployStatusFailed:  true,
	},
	DeployStatusStopped: {
		DeployStatusRunning: true,
		DeployStatusFailed:  true,
	},
	DeployStatusCrashed: {
		DeployStatusRunning: true,
		DeployStatusFailed:  true,
	},
	DeployStatusFailed: {}, // Terminal
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
