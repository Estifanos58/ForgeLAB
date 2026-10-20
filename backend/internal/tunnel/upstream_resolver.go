package tunnel

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	dockerclient "github.com/docker/docker/client"

	"github.com/forgelab/backend/internal/models"
)

// DockerUpstreamResolver discovers reachable HTTP targets for running services.
type DockerUpstreamResolver struct {
	dockerCli *dockerclient.Client
}

// NewDockerUpstreamResolver creates a new DockerUpstreamResolver.
func NewDockerUpstreamResolver(cli *dockerclient.Client) *DockerUpstreamResolver {
	return &DockerUpstreamResolver{dockerCli: cli}
}

// ResolveUpstream identifies a reachable HTTP address for the target service.
func (r *DockerUpstreamResolver) ResolveUpstream(ctx context.Context, service *models.Service) (string, error) {
	if service == nil {
		return "", fmt.Errorf("service is nil")
	}

	intPort := service.InternalPort
	if intPort <= 0 {
		intPort = 8080
	}

	var candidates []string

	isDockerEnv := os.Getenv("FORGELAB_IN_DOCKER") != ""
	if _, err := os.Stat("/.dockerenv"); err == nil {
		isDockerEnv = true
	}

	// 1. Inspect container network settings if Docker client is available
	if r.dockerCli != nil && service.ContainerID != nil && *service.ContainerID != "" {
		inspectCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		cJSON, err := r.dockerCli.ContainerInspect(inspectCtx, *service.ContainerID)
		cancel()
		if err == nil && cJSON.NetworkSettings != nil {
			for _, netSettings := range cJSON.NetworkSettings.Networks {
				if netSettings != nil && netSettings.IPAddress != "" {
					candidates = append(candidates, fmt.Sprintf("http://%s:%d", netSettings.IPAddress, intPort))
				}
			}
			if cJSON.NetworkSettings.IPAddress != "" {
				candidates = append(candidates, fmt.Sprintf("http://%s:%d", cJSON.NetworkSettings.IPAddress, intPort))
			}
		}
	}

	// 2. Add HostPort candidates
	if service.HostPort != nil && *service.HostPort > 0 {
		hp := *service.HostPort
		if isDockerEnv {
			candidates = append(candidates, fmt.Sprintf("http://host.docker.internal:%d", hp))
		}
		candidates = append(candidates, fmt.Sprintf("http://127.0.0.1:%d", hp))
		candidates = append(candidates, fmt.Sprintf("http://localhost:%d", hp))
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("no upstream candidates found for service '%s' (internal_port=%d, host_port=%v)", service.Name, intPort, service.HostPort)
	}

	// 3. Fast connectivity probe: pick the first responsive candidate
	for _, candidate := range candidates {
		hostPort := stringsTrimHTTP(candidate)
		conn, err := net.DialTimeout("tcp", hostPort, 400*time.Millisecond)
		if err == nil {
			conn.Close()
			return candidate, nil
		}
	}

	// If no candidate affirmatively answered TCP probe, return the first one as fallback
	return candidates[0], nil
}

func stringsTrimHTTP(s string) string {
	s = trimPrefixFold(s, "http://")
	s = trimPrefixFold(s, "https://")
	return s
}

func trimPrefixFold(s, prefix string) string {
	if len(s) >= len(prefix) && (s[:len(prefix)] == prefix || s[:len(prefix)] == "HTTP://" || s[:len(prefix)] == "HTTPS://") {
		return s[len(prefix):]
	}
	return s
}
