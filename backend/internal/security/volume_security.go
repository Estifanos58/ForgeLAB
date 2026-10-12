package security

import (
	"path/filepath"
	"strings"

	"github.com/forgelab/backend/internal/models"
)

// IsDockerSocketMount returns true if the candidate path points to a Docker socket
// (Unix domain socket or Windows named pipe).
func IsDockerSocketMount(p string) bool {
	if strings.TrimSpace(p) == "" {
		return false
	}
	clean := filepath.Clean(p)
	lower := strings.ToLower(clean)
	norm := strings.ReplaceAll(lower, "\\", "/")
	return strings.Contains(norm, "docker.sock") || strings.Contains(norm, "docker_engine")
}

// SanitizeServiceVolumes inspects volume mounts for a tenant container and strips
// any forbidden host Docker socket mounts. It returns the safe list of mounts and
// a boolean indicating whether any forbidden socket mounts were stripped.
func SanitizeServiceVolumes(volumes []models.VolumeMountConfig) ([]models.VolumeMountConfig, bool) {
	if len(volumes) == 0 {
		return []models.VolumeMountConfig{}, false
	}
	var safe []models.VolumeMountConfig
	stripped := false
	for _, v := range volumes {
		if IsDockerSocketMount(v.Source) || IsDockerSocketMount(v.Target) {
			stripped = true
			continue
		}
		safe = append(safe, v)
	}
	if safe == nil {
		safe = []models.VolumeMountConfig{}
	}
	return safe, stripped
}
