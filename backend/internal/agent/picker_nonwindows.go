//go:build !windows

package agent

import (
	"context"
	"errors"
)

// InspectInteractiveSession reports interactive capability on non-Windows hosts.
func InspectInteractiveSession() InteractiveSessionInfo {
	return InteractiveSessionInfo{
		SessionID:            1,
		Interactive:          true,
		WindowStationVisible: true,
	}
}

func runDefaultWindowsPicker(_ context.Context, _ string) (string, error) {
	return "", errors.New("Windows folder picker is unavailable on non-Windows platforms")
}
