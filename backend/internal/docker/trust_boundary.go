package docker

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
)

const (
	MinCpuMillicores = 100
	MaxCpuMillicores = 16000 // 16 cores

	MinMemoryMB = 64
	MaxMemoryMB = 32768 // 32 GB

	MinPidsLimit = 16
	MaxPidsLimit = 4096
)

var (
	ErrDockerSocketMountRejected    = errors.New("security violation: mounting host Docker socket into deployed containers is strictly forbidden")
	ErrSensitiveHostMountRejected   = errors.New("security violation: mounting sensitive host filesystem paths into deployed containers is forbidden")
	ErrPrivilegedModeRejected       = errors.New("security violation: running deployed containers in privileged mode is forbidden")
	ErrHostNetworkingRejected       = errors.New("security violation: host networking mode for deployed containers is forbidden")
	ErrHostMountNotAllowed          = errors.New("security violation: host mount path is not within the approved ForgeLAB mount allowlist")
	ErrCpuOutOfRange                = fmt.Errorf("cpu_millicores must be between %d and %d (0.1 to 16 cores)", MinCpuMillicores, MaxCpuMillicores)
	ErrMemoryOutOfRange             = fmt.Errorf("memory_mb must be between %d and %d (64MB to 32GB)", MinMemoryMB, MaxMemoryMB)
	ErrPidsOutOfRange               = fmt.Errorf("pids_limit must be between %d and %d", MinPidsLimit, MaxPidsLimit)
	ErrEphemeralStorageNotSupported = errors.New("ephemeral_storage_mb is not supported on this host environment; remove or leave empty")
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
	PortBindings         nat.PortMap
	TargetCpuMillicores  int
	TargetMemoryMB       int
	TargetPidsLimit      int
	Binds                []string
	NetworkMode          string
	AllowedMountPrefixes []string
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
			cleanRestricted := strings.ToLower(filepath.Clean(restricted))
			if lowerHostPath == cleanRestricted || strings.HasPrefix(lowerHostPath, cleanRestricted+string(filepath.Separator)) {
				return nil, fmt.Errorf("%w: %s", ErrSensitiveHostMountRejected, hostPath)
			}
		}

		// Check against allowlist if configured
		if len(opts.AllowedMountPrefixes) > 0 {
			allowed := false
			for _, prefix := range opts.AllowedMountPrefixes {
				cleanPrefix := strings.ToLower(filepath.Clean(prefix))
				if lowerHostPath == cleanPrefix || strings.HasPrefix(lowerHostPath, cleanPrefix+string(filepath.Separator)) {
					allowed = true
					break
				}
			}
			if !allowed {
				return nil, fmt.Errorf("%w: %s", ErrHostMountNotAllowed, hostPath)
			}
		}

		validatedBinds = append(validatedBinds, bind)
	}

	// 3. Truthful resource limits validation (no silent clamping)
	cpuMilli := opts.TargetCpuMillicores
	if cpuMilli == 0 {
		cpuMilli = 1000 // default 1 CPU core
	} else if cpuMilli < MinCpuMillicores || cpuMilli > MaxCpuMillicores {
		return nil, ErrCpuOutOfRange
	}

	memMB := opts.TargetMemoryMB
	if memMB == 0 {
		memMB = 1024 // default 1 GB
	} else if memMB < MinMemoryMB || memMB > MaxMemoryMB {
		return nil, ErrMemoryOutOfRange
	}

	pidsLimitVal := opts.TargetPidsLimit
	if pidsLimitVal == 0 {
		pidsLimitVal = 256 // default 256
	} else if pidsLimitVal < MinPidsLimit || pidsLimitVal > MaxPidsLimit {
		return nil, ErrPidsOutOfRange
	}

	cpuNano := int64(cpuMilli) * 1_000_000
	memBytes := int64(memMB) * 1024 * 1024
	pidsLimit := int64(pidsLimitVal)

	// 4. Construct secure HostConfig with defense-in-depth isolation
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
			Ulimits: []*container.Ulimit{
				{
					Name: "nofile",
					Soft: 1024,
					Hard: 2048,
				},
			},
		},
		Tmpfs: map[string]string{
			"/tmp": "rw,noexec,nosuid,size=64m",
		},
		LogConfig: container.LogConfig{
			Type: "json-file",
			Config: map[string]string{
				"max-size": "10m",
				"max-file": "3",
			},
		},
	}

	return hostConfig, nil
}
