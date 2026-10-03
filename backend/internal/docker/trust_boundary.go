package docker

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
)

var (
	ErrDockerSocketMountRejected  = errors.New("security violation: mounting host Docker socket into deployed containers is strictly forbidden")
	ErrSensitiveHostMountRejected = errors.New("security violation: mounting sensitive host filesystem paths into deployed containers is forbidden")
	ErrPrivilegedModeRejected     = errors.New("security violation: running deployed containers in privileged mode is forbidden")
	ErrHostNetworkingRejected     = errors.New("security violation: host networking mode for deployed containers is forbidden")
)

// Sensitive host paths that must never be mounted into tenant containers.
var restrictedHostMounts = []string{
	"/var/run/docker.sock",
	"/var/run",
	"/run/docker.sock",
	"/run",
	"/proc",
	"/sys",
	"/dev",
	"/etc",
	"/root",
	"\\\\.\\pipe\\docker_engine", // Windows named pipe
}

// ContainerSecurityOptions parameters for creating a strictly sandboxed container.
type ContainerSecurityOptions struct {
	PortBindings        nat.PortMap
	TargetCpuMillicores int
	TargetMemoryMB      int
	TargetPidsLimit     int
	Binds               []string
	NetworkMode         string
}

// ValidateAndBuildSecureHostConfig creates a centralized, strictly validated Docker HostConfig
// that enforces the platform trust boundary invariants.
func ValidateAndBuildSecureHostConfig(opts ContainerSecurityOptions) (*container.HostConfig, error) {
	// 1. Validate NetworkMode: Reject host networking
	if strings.EqualFold(opts.NetworkMode, "host") {
		return nil, ErrHostNetworkingRejected
	}

	// 2. Validate Binds: Ensure zero access to docker socket or sensitive host paths
	validatedBinds := make([]string, 0, len(opts.Binds))
	for _, bind := range opts.Binds {
		parts := strings.Split(bind, ":")
		if len(parts) < 2 {
			continue
		}
		hostPath := filepath.Clean(parts[0])
		lowerHostPath := strings.ToLower(hostPath)

		// Check against Docker socket (Unix socket and Windows named pipe)
		if strings.Contains(lowerHostPath, "docker.sock") || strings.Contains(lowerHostPath, "docker_engine") {
			return nil, ErrDockerSocketMountRejected
		}

		// Check against sensitive paths
		for _, restricted := range restrictedHostMounts {
			if lowerHostPath == strings.ToLower(filepath.Clean(restricted)) || strings.HasPrefix(lowerHostPath, strings.ToLower(filepath.Clean(restricted))+string(filepath.Separator)) {
				return nil, fmt.Errorf("%w: %s", ErrSensitiveHostMountRejected, hostPath)
			}
		}

		validatedBinds = append(validatedBinds, bind)
	}

	// 3. Centralize and clamp resource limits
	cpuMilli := opts.TargetCpuMillicores
	if cpuMilli <= 0 {
		cpuMilli = 1000 // default 1 CPU core
	} else if cpuMilli < 100 {
		cpuMilli = 100 // clamp minimum to 100 millicores (0.1 core)
	} else if cpuMilli > 16000 {
		cpuMilli = 16000 // cap at 16 cores
	}

	memMB := opts.TargetMemoryMB
	if memMB <= 0 {
		memMB = 1024 // default 1 GB
	} else if memMB < 32 {
		memMB = 32 // clamp minimum to 32 MB
	} else if memMB > 32768 {
		memMB = 32768 // cap at 32 GB
	}

	pidsLimitVal := opts.TargetPidsLimit
	if pidsLimitVal <= 0 {
		pidsLimitVal = 256 // default 256
	} else if pidsLimitVal < 16 {
		pidsLimitVal = 16 // clamp minimum to 16
	} else if pidsLimitVal > 4096 {
		pidsLimitVal = 4096 // cap at 4096
	}

	cpuNano := int64(cpuMilli) * 1_000_000
	memBytes := int64(memMB) * 1024 * 1024
	pidsLimit := int64(pidsLimitVal)

	// 4. Construct secure HostConfig
	hostConfig := &container.HostConfig{
		PortBindings: opts.PortBindings,
		Binds:        validatedBinds,
		Privileged:   false, // NEVER privileged
		SecurityOpt:  []string{"no-new-privileges:true"},
		CapDrop:      []string{"ALL"},
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
		Resources: container.Resources{
			Memory:    memBytes,
			NanoCPUs:  cpuNano,
			PidsLimit: &pidsLimit,
		},
	}

	return hostConfig, nil
}
