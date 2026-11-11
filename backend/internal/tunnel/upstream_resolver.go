package tunnel

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	dockerclient "github.com/moby/moby/client"

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
	// Control-plane isolation: We strictly DO NOT attach the backend container to tenant networks.
	// Privileged control plane services (Docker socket, Postgres, Redis, API) must never be exposed
	// to deployed tenant workloads.
	if r.dockerCli != nil && service.ContainerID != nil && *service.ContainerID != "" {
		inspectCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		cRes, err := r.dockerCli.ContainerInspect(inspectCtx, *service.ContainerID, dockerclient.ContainerInspectOptions{})
		cancel()
		cJSON := cRes.Container
		if err == nil && cJSON.NetworkSettings != nil {
			for _, netSettings := range cJSON.NetworkSettings.Networks {
				if netSettings != nil && netSettings.IPAddress.IsValid() && !netSettings.IPAddress.IsUnspecified() {
					candidates = append(candidates, "http://"+net.JoinHostPort(netSettings.IPAddress.String(), strconv.Itoa(intPort)))
				}
			}

			// Add bridge network gateway candidates for loopback-published ports in Docker Compose
			if service.HostPort != nil && *service.HostPort > 0 {
				hp := *service.HostPort
				for _, netSettings := range cJSON.NetworkSettings.Networks {
					if netSettings != nil && netSettings.Gateway.IsValid() && !netSettings.Gateway.IsUnspecified() {
						candidates = append(candidates, "http://"+net.JoinHostPort(netSettings.Gateway.String(), strconv.Itoa(hp)))
					}
				}
			}

			// Inspect published host port bindings if HostPort was unassigned or differently mapped
			if cJSON.NetworkSettings.Ports != nil {
				for _, bindings := range cJSON.NetworkSettings.Ports {
					for _, b := range bindings {
						if b.HostPort != "" {
							if isDockerEnv {
								candidates = append(candidates, fmt.Sprintf("http://host.docker.internal:%s", b.HostPort))
							}
							if b.HostIP.IsValid() && !b.HostIP.IsUnspecified() {
								candidates = append(candidates, "http://"+net.JoinHostPort(b.HostIP.String(), b.HostPort))
							}
							candidates = append(candidates, fmt.Sprintf("http://127.0.0.1:%s", b.HostPort))
						}
					}
				}
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
	for attempt := 0; attempt < 2; attempt++ {
		for _, candidate := range candidates {
			hostPort := stringsTrimHTTP(candidate)
			conn, err := net.DialTimeout("tcp", hostPort, 500*time.Millisecond)
			if err == nil {
				conn.Close()
				return candidate, nil
			}
		}
		if attempt == 0 {
			time.Sleep(150 * time.Millisecond)
		}
	}

	return "", fmt.Errorf("service %q is unreachable: no candidate endpoints (%s) answered TCP probe",
		service.Name, strings.Join(candidates, ", "))
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
