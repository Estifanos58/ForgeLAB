package security

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/forgelab/backend/internal/models"
)

func TestIsDockerSocketMount(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"/var/run/docker.sock", true},
		{"/run/docker.sock", true},
		{`\\.\pipe\docker_engine`, true},
		{"/var/run/docker_engine", true},
		{"C:\\ProgramData\\docker.sock", true},
		{"", false},
		{"/app/data", false},
		{"forgelab_pgdata", false},
		{"./local_dir", false},
	}

	for _, tt := range tests {
		actual := IsDockerSocketMount(tt.path)
		assert.Equal(t, tt.expected, actual, "failed for path: %s", tt.path)
	}
}

func TestSanitizeServiceVolumes(t *testing.T) {
	vols := []models.VolumeMountConfig{
		{
			Source: "/var/run/docker.sock",
			Target: "/var/run/docker.sock",
			Type:   "bind",
		},
		{
			Source: "forgelab_builds",
			Target: "/app/data/builds",
			Type:   "volume",
		},
		{
			Source: `\\.\pipe\docker_engine`,
			Target: "/docker.sock",
			Type:   "bind",
		},
		{
			Source: "./src",
			Target: "/app/src",
			Type:   "bind",
		},
	}

	safe, stripped := SanitizeServiceVolumes(vols)
	assert.True(t, stripped, "expected forbidden socket mounts to be stripped")
	assert.Len(t, safe, 2, "expected exactly 2 safe mounts remaining")
	assert.Equal(t, "forgelab_builds", safe[0].Source)
	assert.Equal(t, "./src", safe[1].Source)

	// Test with already clean volumes
	cleanVols := []models.VolumeMountConfig{
		{Source: "data_vol", Target: "/data", Type: "volume"},
	}
	safe2, stripped2 := SanitizeServiceVolumes(cleanVols)
	assert.False(t, stripped2)
	assert.Len(t, safe2, 1)

	// Test with empty volumes
	safe3, stripped3 := SanitizeServiceVolumes(nil)
	assert.False(t, stripped3)
	assert.Empty(t, safe3)
}
