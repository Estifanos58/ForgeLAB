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
