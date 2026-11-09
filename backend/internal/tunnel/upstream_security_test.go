package tunnel

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCentralizedUpstreamSecurityValidation(t *testing.T) {
	// Forbidden infrastructure targets
	forbiddenTargets := []string{
		"http://127.0.0.1:8080/api/projects",
		"http://localhost:5432",
		"http://127.0.0.1:6379",
		"http://127.0.0.1:4142/api/agent/status",
		"http://forgelab-backend:8080/api/projects",
		"http://forgelab-frontend:3000",
		"http://forgelab-postgres:5432",
		"http://forgelab-redis:6379",
		"http://0.0.0.0:8080",
		"http://::1:8080",
		"http://host.docker.internal:8080",
	}

	for _, target := range forbiddenTargets {
		t.Run("Forbidden:"+target, func(t *testing.T) {
			assert.True(t, IsForbiddenUpstream(target), "expected %s to be forbidden", target)
			err := ValidateUpstreamSecurity(target)
			assert.ErrorIs(t, err, ErrForbiddenUpstream)
		})
	}

	// Allowed tenant application targets
	allowedTargets := []string{
		"http://127.0.0.1:10001",
		"http://172.19.0.5:8080",
		"http://10.0.1.25:8080",
		"http://192.168.1.10:5000",
		"http://custom-app-container:80",
	}

	for _, target := range allowedTargets {
		t.Run("Allowed:"+target, func(t *testing.T) {
			assert.False(t, IsForbiddenUpstream(target), "expected %s to be allowed", target)
			assert.NoError(t, ValidateUpstreamSecurity(target))
		})
	}

	// Empty and invalid inputs
	assert.Error(t, ValidateUpstreamSecurity(""))
	assert.True(t, IsForbiddenUpstream(":::invalid-url:::"))
}
