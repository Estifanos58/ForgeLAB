package docker

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsPortConflictError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "unrelated error",
			err:      errors.New("connection refused"),
			expected: false,
		},
		{
			name:     "port is already allocated",
			err:      errors.New("Bind for 0.0.0.0:8080 failed: port is already allocated"),
			expected: true,
		},
		{
			name:     "address already in use",
			err:      errors.New("listen tcp :8080: bind: address already in use"),
			expected: true,
		},
		{
			name:     "port has already been allocated",
			err:      errors.New("failed to set up container: port has already been allocated"),
			expected: true,
		},
		{
			name:     "failed to allocate gateway",
			err:      errors.New("failed to allocate gateway: conflict"),
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, isPortConflictError(tc.err))
		})
	}
}

func TestEngine_PublicHost(t *testing.T) {
	engine := &Engine{}
	assert.Equal(t, "localhost", engine.GetPublicHost())

	engine.SetPublicHost("app.example.com")
	assert.Equal(t, "app.example.com", engine.GetPublicHost())

	engine.SetPublicHost("")
	assert.Equal(t, "app.example.com", engine.GetPublicHost(), "Empty string should not override existing public host")
}
