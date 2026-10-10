package docker

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

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

	MinNofileLimit = 1024
	MaxNofileLimit = 65536
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
	ErrNofileOutOfRange             = fmt.Errorf("nofile limits must be between %d and %d, with soft <= hard", MinNofileLimit, MaxNofileLimit)
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
	TargetNofileSoft     int
	TargetNofileHard     int
	Binds                []string
	NetworkMode          string
	AllowedMountPrefixes []string
	AllowDockerSocket    bool
	CapAdd               []string
}

func splitDockerBind(bind string) (hostPath, containerPath, mode string) {
	s := strings.TrimSpace(bind)
	if s == "" {
		return "", "", ""
	}

	start := 0
	if len(s) >= 3 && unicode.IsLetter(rune(s[0])) && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		start = 2
	}

	firstColon := strings.Index(s[start:], ":")
	if firstColon == -1 {
		return s, "", ""
	}
	actualFirstColon := start + firstColon

	hostPart := s[:actualFirstColon]
	rest := s[actualFirstColon+1:]

	secondColon := strings.Index(rest, ":")
	if secondColon == -1 {
		return hostPart, rest, ""
	}

	return hostPart, rest[:secondColon], rest[secondColon+1:]
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
		host, containerPath, _ := splitDockerBind(bind)
		if host == "" || containerPath == "" {
			continue
		}
		hostPath := filepath.Clean(host)
		lowerHostPath := strings.ToLower(hostPath)

		// Check against Docker socket (Unix socket and Windows named pipe)
		if !opts.AllowDockerSocket && (strings.Contains(lowerHostPath, "docker.sock") || strings.Contains(lowerHostPath, "docker_engine")) {
			return nil, ErrDockerSocketMountRejected
		}

		normHostPath := strings.ReplaceAll(lowerHostPath, "\\", "/")

		// Check against sensitive paths
		for _, restricted := range restrictedHostMounts {
			if opts.AllowDockerSocket && (strings.Contains(restricted, "docker.sock") || strings.Contains(restricted, "docker_engine")) {
				continue
			}
			cleanRestricted := strings.ToLower(filepath.Clean(restricted))
			normRestricted := strings.ReplaceAll(cleanRestricted, "\\", "/")
			if normHostPath == normRestricted || strings.HasPrefix(normHostPath, normRestricted+"/") ||
				lowerHostPath == cleanRestricted || strings.HasPrefix(lowerHostPath, cleanRestricted+string(filepath.Separator)) || strings.HasPrefix(lowerHostPath, cleanRestricted+"/") {
				return nil, fmt.Errorf("%w: %s", ErrSensitiveHostMountRejected, hostPath)
			}
		}

		// Check against allowlist if configured (only for host bind paths, not Docker named volumes)
		isNamedVolume := !filepath.IsAbs(host) && !strings.Contains(host, "/") && !strings.Contains(host, "\\") && !strings.HasPrefix(host, ".")
		if !isNamedVolume && len(opts.AllowedMountPrefixes) > 0 {
			allowed := false
			for _, prefix := range opts.AllowedMountPrefixes {
				if strings.TrimSpace(prefix) == "" {
					continue
				}
				cleanPrefix := strings.ToLower(filepath.Clean(prefix))
				normPrefix := strings.ReplaceAll(cleanPrefix, "\\", "/")
				if normHostPath == normPrefix || strings.HasPrefix(normHostPath, normPrefix+"/") ||
					lowerHostPath == cleanPrefix || strings.HasPrefix(lowerHostPath, cleanPrefix+string(filepath.Separator)) || strings.HasPrefix(lowerHostPath, cleanPrefix+"/") {
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
	// Standard safe capabilities required by unprivileged container switch tools (e.g. gosu, su-exec, setpriv)
	// used by official daemon images like postgres:16-alpine and redis:7-alpine to drop root privileges safely.
	safeCaps := []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID"}
	if len(opts.CapAdd) > 0 {
		dangerousCaps := map[string]bool{
			"SYS_ADMIN": true, "NET_ADMIN": true, "SYS_PTRACE": true,
			"SYS_RAWIO": true, "SYS_MODULE": true, "DAC_READ_SEARCH": true,
		}
		for _, c := range opts.CapAdd {
			upper := strings.ToUpper(strings.TrimSpace(c))
			if upper != "" && !dangerousCaps[upper] {
				found := false
				for _, sc := range safeCaps {
					if sc == upper {
						found = true
						break
					}
				}
				if !found {
					safeCaps = append(safeCaps, upper)
				}
			}
		}
	}

	var ulimits []*container.Ulimit
	if opts.TargetNofileSoft > 0 || opts.TargetNofileHard > 0 {
		soft := opts.TargetNofileSoft
		hard := opts.TargetNofileHard
		if soft == 0 {
			soft = hard
		}
		if hard == 0 {
			hard = soft
		}
		if soft < MinNofileLimit || soft > MaxNofileLimit || hard < MinNofileLimit || hard > MaxNofileLimit || soft > hard {
			return nil, ErrNofileOutOfRange
		}
		ulimits = []*container.Ulimit{
			{
				Name: "nofile",
				Soft: int64(soft),
				Hard: int64(hard),
			},
		}
	}

	hostConfig := &container.HostConfig{
		PortBindings: opts.PortBindings,
		Binds:        validatedBinds,
		Privileged:   false, // NEVER privileged
		SecurityOpt:  []string{"no-new-privileges:true"},
		CapDrop:      []string{"ALL"},
		CapAdd:       safeCaps,
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
		Resources: container.Resources{
			Memory:    memBytes,
			NanoCPUs:  cpuNano,
			PidsLimit: &pidsLimit,
			Ulimits:   ulimits,
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
