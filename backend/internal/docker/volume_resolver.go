package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/agent"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
)

// ResolvedVolumeMount encapsulates the normalized parameters for a Docker volume mount across all three namespaces:
// 1. AgentPath: The canonical path reported by the local agent on the host
// 2. BackendPath: The path accessible within the backend container environment for validation/stat
// 3. DaemonPath: The path acceptable to the Docker Engine daemon for host bind mounts
type ResolvedVolumeMount struct {
	IsNamedVolume  bool
	VolumeName     string
	AgentPath      string // Canonical path reported by agent/host (e.g. C:\Users\estif\Desktop\ForgeLAB)
	BackendPath    string // Path accessible within backend container (e.g. /host-projects)
	DaemonPath     string // Path acceptable to Docker daemon for bind mounts (e.g. C:\Users\estif\Desktop\ForgeLAB)
	HostPath       string // Same as DaemonPath for backwards compatibility with Docker bind callers
	ContainerPath  string // Container target path (e.g. /host-projects)
	ReadOnly       bool
	IsDockerSocket bool
}

// ProjectSourcePaths encapsulates the paths for a project across the three distinct namespaces.
type ProjectSourcePaths struct {
	AgentPath   string // Canonical path reported by agent/host (e.g. C:\Users\estif\Desktop\ForgeLAB)
	BackendPath string // Path accessible from backend process/container (e.g. /host-projects)
	DaemonPath  string // Path accessible from Docker daemon (e.g. C:\Users\estif\Desktop\ForgeLAB)
}

// ContainerMountInfo describes a host mount point detected on the running backend container.
type ContainerMountInfo struct {
	ContainerID     string
	ContainerName   string
	DaemonSource    string // The host path visible to Docker daemon, e.g. C:\Users\estif\Desktop\ForgeLAB
	ContainerTarget string // The mount destination inside container, e.g. /host-projects
}

func isRelativePath(p string) bool {
	if filepath.IsAbs(p) {
		return false
	}
	if len(p) >= 2 && unicode.IsLetter(rune(p[0])) && p[1] == ':' {
		return false
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") {
		return false
	}
	return true
}

func isNamedVolumeSource(source, volType string) bool {
	if volType == models.VolumeTypeNamed || volType == "volume" {
		return true
	}
	if volType == models.VolumeTypeBind || volType == "bind" {
		return false
	}
	if strings.HasPrefix(source, ".") || strings.HasPrefix(source, "/") || strings.HasPrefix(source, "\\") || strings.HasPrefix(source, "~") {
		return false
	}
	if len(source) >= 2 && unicode.IsLetter(rune(source[0])) && source[1] == ':' {
		return false
	}
	if strings.Contains(source, "/") || strings.Contains(source, "\\") {
		return false
	}
	return true
}

// normalizePathForComparison cleans a path and normalizes separators to '/' in lowercase for robust comparison.
func normalizePathForComparison(p string) string {
	s := strings.TrimSpace(p)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\\", "/")
	s = path.Clean(s)
	s = strings.TrimRight(s, "/")
	return strings.ToLower(s)
}

// getRelativePathCrossPlatform calculates the relative subpath of target beneath root, handling mixed Windows and POSIX formats.
func getRelativePathCrossPlatform(root, target string) (string, bool) {
	normRoot := normalizePathForComparison(root)
	normTarget := normalizePathForComparison(target)

	if normRoot == "" || normTarget == "" {
		return "", false
	}

	if normTarget == normRoot {
		return ".", true
	}

	prefix := normRoot + "/"
	if strings.HasPrefix(normTarget, prefix) {
		rel := normTarget[len(prefix):]
		cleanRel := path.Clean(rel)
		if strings.HasPrefix(cleanRel, "..") || cleanRel == ".." {
			return "", false
		}
		// Extract original relative path if possible
		targetSlash := strings.ReplaceAll(strings.TrimSpace(target), "\\", "/")
		if len(targetSlash) > len(prefix) {
			origRel := path.Clean(targetSlash[len(prefix):])
			if !strings.HasPrefix(origRel, "..") && origRel != ".." {
				return origRel, true
			}
		}
		return cleanRel, true
	}

	return "", false
}

// joinHostPath joins a base host path with a relative path, preserving Windows backslashes if base is a Windows path.
func joinHostPath(base, rel string) string {
	cleanRel := strings.TrimPrefix(filepath.ToSlash(rel), "/")
	cleanRel = path.Clean(cleanRel)
	if cleanRel == "" || cleanRel == "." {
		return base
	}
	cleanRel = strings.TrimPrefix(cleanRel, "/")

	if strings.Contains(base, "\\") || (len(base) >= 2 && unicode.IsLetter(rune(base[0])) && base[1] == ':') {
		baseClean := strings.TrimRight(strings.ReplaceAll(base, "/", "\\"), "\\")
		relClean := strings.ReplaceAll(cleanRel, "/", "\\")
		return baseClean + "\\" + relClean
	}

	baseClean := strings.TrimRight(filepath.ToSlash(base), "/")
	return baseClean + "/" + cleanRel
}

// joinContainerPath joins a container directory with a relative path in POSIX format.
func joinContainerPath(base, rel string) string {
	cleanRel := path.Clean(filepath.ToSlash(rel))
	if cleanRel == "" || cleanRel == "." {
		return filepath.Clean(base)
	}
	cleanRel = strings.TrimPrefix(cleanRel, "/")
	return filepath.Clean(filepath.Join(base, filepath.FromSlash(cleanRel)))
}

// DetectBackendContainerMount queries the Docker daemon to locate the host mount corresponding
// to containerTarget (defaulting to /host-projects) on the running ForgeLAB backend container.
func DetectBackendContainerMount(ctx context.Context, cli client.CommonAPIClient, containerTarget string) (*ContainerMountInfo, error) {
	if cli == nil {
		return nil, errors.New("docker client is nil")
	}

	target := strings.TrimSpace(containerTarget)
	if target == "" {
		target = "/host-projects"
	}
	normTarget := normalizePathForComparison(target)

	checkMounts := func(inspect types.ContainerJSON) *ContainerMountInfo {
		for _, m := range inspect.Mounts {
			normDest := normalizePathForComparison(m.Destination)
			if normDest == normTarget || strings.EqualFold(filepath.ToSlash(m.Destination), filepath.ToSlash(target)) {
				return &ContainerMountInfo{
					ContainerID:     inspect.ID,
					ContainerName:   inspect.Name,
					DaemonSource:    m.Source,
					ContainerTarget: m.Destination,
				}
			}
		}
		return nil
	}

	// 1. Try container hostname (Docker sets the container hostname to short container ID by default)
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		if inspect, err := cli.ContainerInspect(ctx, hostname); err == nil {
			if info := checkMounts(inspect); info != nil {
				return info, nil
			}
		}
	}

	// 2. Try explicit container names defined by docker-compose.yml
	for _, name := range []string{"forgelab-backend", "/forgelab-backend"} {
		if inspect, err := cli.ContainerInspect(ctx, name); err == nil {
			if info := checkMounts(inspect); info != nil {
				return info, nil
			}
		}
	}

	// 3. Fallback: inspect running containers matching backend service name or label
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: false})
	if err == nil {
		for _, c := range containers {
			isCandidate := false
			if c.Labels != nil && c.Labels["com.docker.compose.service"] == "backend" {
				isCandidate = true
			}
			if !isCandidate {
				for _, n := range c.Names {
					if strings.Contains(n, "backend") || strings.Contains(n, "forgelab") {
						isCandidate = true
						break
					}
				}
			}
			if isCandidate {
				if inspect, err := cli.ContainerInspect(ctx, c.ID); err == nil {
					if info := checkMounts(inspect); info != nil {
						return info, nil
					}
				}
			}
		}
	}

	return nil, fmt.Errorf("could not find running ForgeLAB backend container mount for '%s'", target)
}

// ResolveProjectSourcePaths determines the authoritative paths across the three namespaces:
// 1. AgentPath: The canonical path reported by the local agent on the host
// 2. BackendPath: The path accessible inside the running ForgeLAB backend container
// 3. DaemonPath: The path acceptable to the Docker Engine daemon for host bind mounts
func ResolveProjectSourcePaths(
	ctx context.Context,
	project *models.Project,
	sourceService *services.SourceService,
	pathValidator *security.PathValidator,
	dockerCli ...client.CommonAPIClient,
) (ProjectSourcePaths, error) {
	if project == nil {
		return ProjectSourcePaths{}, nil
	}

	var cli client.CommonAPIClient
	if len(dockerCli) > 0 && dockerCli[0] != nil {
		cli = dockerCli[0]
	}

	// Dynamically resolve backend container mount if hostSourceRoot is not yet populated
	if pathValidator != nil && pathValidator.HostSourceRoot() == "" && cli != nil {
		if mountInfo, err := DetectBackendContainerMount(ctx, cli, pathValidator.ContainerSourceRoot()); err == nil && mountInfo != nil {
			pathValidator.SetHostSourceRoot(mountInfo.DaemonSource)
			pathValidator.SetDaemonSourceRoot(mountInfo.DaemonSource)
		}
	}

	switch project.SourceType {
	case models.SourceTypeLocalAgent:
		var sourcePath string

		// 1. Resolve source ID from SourceID or SourceReference
		var targetSourceUUID uuid.UUID
		if project.SourceID != nil {
			targetSourceUUID = *project.SourceID
		} else if project.SourceReference != "" {
			if parsed, parseErr := uuid.Parse(project.SourceReference); parseErr == nil {
				targetSourceUUID = parsed
			}
		}

		// 2. Query in-memory session manager, local sessions, DB sources, or agent daemon
		if targetSourceUUID != uuid.Nil {
			// In-memory active local source session
			if locSess, ok := agent.LookupLocalSourceSession(targetSourceUUID); ok && locSess.CanonicalPath != "" {
				sourcePath = locSess.CanonicalPath
			}

			// Global session manager
			if sourcePath == "" {
				if sm := agent.GetGlobalSessionManager(); sm != nil {
					if sess, sErr := sm.FindSessionBySourceID(targetSourceUUID); sErr == nil && sess != nil && sess.SourcePath != "" {
						sourcePath = sess.SourcePath
					}
				}
			}

			// Sources table in database
			if sourcePath == "" && sourceService != nil {
				if src, sErr := sourceService.GetSourceByID(ctx, targetSourceUUID); sErr == nil && src != nil && src.Metadata != nil {
					if sp, ok := src.Metadata["source_path"].(string); ok && strings.TrimSpace(sp) != "" {
						sourcePath = strings.TrimSpace(sp)
					}
				}
			}

			// Active agent daemon query
			if sourcePath == "" {
				var token string
				if sourceService != nil {
					token, _ = sourceService.GetAgentSessionToken(ctx, project.OwnerID, targetSourceUUID)
				}
				if p, fErr := agent.FetchSourcePath(ctx, "", targetSourceUUID, token); fErr == nil && strings.TrimSpace(p) != "" {
					sourcePath = strings.TrimSpace(p)
				}
			}
		}

		if strings.TrimSpace(sourcePath) == "" {
			return ProjectSourcePaths{}, fmt.Errorf("unable to determine local-agent host source directory for project '%s'. Ensure agent session is active.", project.Name)
		}

		agentPath := strings.TrimSpace(sourcePath)
		daemonPath := agentPath
		backendPath := agentPath

		if pathValidator != nil {
			hostRoot := pathValidator.HostSourceRoot()
			containerRoot := pathValidator.ContainerSourceRoot()
			daemonRoot := pathValidator.DaemonSourceRoot()
			if daemonRoot == "" {
				daemonRoot = hostRoot
			}

			if hostRoot != "" {
				rel, matches := getRelativePathCrossPlatform(hostRoot, agentPath)
				if matches {
					backendPath = joinContainerPath(containerRoot, rel)
					daemonPath = joinHostPath(daemonRoot, rel)
				} else {
					// Project is outside the configured host source root
					// Check if agentPath directly exists in container filesystem
					if _, err := os.Stat(agentPath); err != nil {
						// Not directly accessible in container
						backendPath = ""
					}
				}
			} else {
				// No host root translation configured: if agentPath doesn't exist on disk, check containerRoot
				if _, err := os.Stat(agentPath); err != nil {
					if containerRoot != "" {
						if _, cErr := os.Stat(containerRoot); cErr == nil {
							backendPath = containerRoot
							if cli != nil {
								if mInfo, mErr := DetectBackendContainerMount(ctx, cli, containerRoot); mErr == nil && mInfo != nil {
									daemonPath = mInfo.DaemonSource
								}
							}
						}
					}
				}
			}
		}

		return ProjectSourcePaths{
			AgentPath:   agentPath,
			BackendPath: backendPath,
			DaemonPath:  daemonPath,
		}, nil

	case models.SourceTypeLocalDirectory, models.SourceTypeLocal:
		rawPath := strings.TrimSpace(project.RepositoryPath)
		if rawPath == "" {
			return ProjectSourcePaths{}, errors.New("repository_path is required for local directory projects")
		}

		agentPath := rawPath
		backendPath := rawPath
		daemonPath := rawPath

		if pathValidator != nil {
			if h, tErr := pathValidator.TranslateContainerToHost(rawPath); tErr == nil && h != "" {
				agentPath = h
				daemonPath = h
			}
			if c, tErr := pathValidator.TranslateHostToContainer(rawPath); tErr == nil && c != "" {
				backendPath = c
			}
			if pathValidator.DaemonSourceRoot() != "" {
				if rel, ok := getRelativePathCrossPlatform(pathValidator.HostSourceRoot(), daemonPath); ok {
					daemonPath = joinHostPath(pathValidator.DaemonSourceRoot(), rel)
				}
			}
		}

		return ProjectSourcePaths{
			AgentPath:   agentPath,
			BackendPath: backendPath,
			DaemonPath:  daemonPath,
		}, nil

	case models.SourceTypeLocalUpload:
		if sourceService != nil && project.SourceReference != "" {
			if srcUUID, parseErr := uuid.Parse(project.SourceReference); parseErr == nil {
				if p, pErr := sourceService.GetSourcePath(ctx, project.OwnerID, srcUUID); pErr == nil && p != "" {
					cleanP := filepath.Clean(p)
					return ProjectSourcePaths{
						AgentPath:   cleanP,
						BackendPath: cleanP,
						DaemonPath:  cleanP,
					}, nil
				}
			}
		}
		return ProjectSourcePaths{}, fmt.Errorf("source files for uploaded archive project '%s' are unavailable", project.Name)

	case models.SourceTypeGitHub:
		// GitHub repos are cloned temporarily for builds and do not provide persistent runtime host directories
		return ProjectSourcePaths{}, nil

	default:
		return ProjectSourcePaths{}, nil
	}
}

// ResolveProjectSourceRoot determines the authoritative host and container source root paths for a project.
func ResolveProjectSourceRoot(ctx context.Context, project *models.Project, sourceService *services.SourceService, pathValidator *security.PathValidator) (hostSourceRoot string, containerSourceRoot string, err error) {
	paths, err := ResolveProjectSourcePaths(ctx, project, sourceService, pathValidator)
	if err != nil {
		return "", "", err
	}
	daemonRoot := paths.DaemonPath
	if daemonRoot == "" {
		daemonRoot = paths.AgentPath
	}
	backendRoot := paths.BackendPath
	if backendRoot == "" {
		backendRoot = paths.AgentPath
	}
	return daemonRoot, backendRoot, nil
}

// ResolveAndValidateVolumeMount resolves and validates a volume mount configuration consistently for both preflight and runtime engine.
func ResolveAndValidateVolumeMount(
	ctx context.Context,
	project *models.Project,
	vol models.VolumeMountConfig,
	sourceService *services.SourceService,
	pathValidator *security.PathValidator,
	dockerCli ...client.CommonAPIClient,
) (*ResolvedVolumeMount, error) {
	// Container target must be an absolute path starting with '/'
	cleanTarget := filepath.ToSlash(vol.Target)
	if !strings.HasPrefix(cleanTarget, "/") {
		return nil, fmt.Errorf("container volume target '%s' must be an absolute path starting with '/'", vol.Target)
	}

	// 1. Named volume classification
	if isNamedVolumeSource(vol.Source, vol.Type) {
		return &ResolvedVolumeMount{
			IsNamedVolume: true,
			VolumeName:    vol.Source,
			ContainerPath: cleanTarget,
			ReadOnly:      vol.ReadOnly,
		}, nil
	}

	// 2. Docker socket detection
	if vol.Source == "/var/run/docker.sock" || vol.Source == `\\.\pipe\docker_engine` || vol.Source == "/run/docker.sock" {
		return &ResolvedVolumeMount{
			IsDockerSocket: true,
			HostPath:       vol.Source,
			DaemonPath:     vol.Source,
			ContainerPath:  cleanTarget,
			ReadOnly:       vol.ReadOnly,
		}, nil
	}

	// 3. Bind mount validation
	if project == nil {
		return nil, errors.New("cannot resolve volume mount without project context")
	}

	// Reject relative mounts for source types that cannot safely provide persistent host paths
	if isRelativePath(vol.Source) {
		if project.SourceType == models.SourceTypeGitHub {
			return nil, fmt.Errorf("relative bind mount '%s' is not supported for remote source type '%s'. Use a named volume or absolute host path.", vol.Source, project.SourceType)
		}
		if project.SourceType == models.SourceTypeLocalUpload {
			return nil, fmt.Errorf("relative bind mount '%s' is not supported for temporary uploaded archive source type '%s'. Use a named volume or absolute host path.", vol.Source, project.SourceType)
		}
	}

	paths, err := ResolveProjectSourcePaths(ctx, project, sourceService, pathValidator, dockerCli...)
	if err != nil {
		return nil, err
	}

	if isRelativePath(vol.Source) {
		cleanRel := path.Clean(filepath.ToSlash(vol.Source))
		cleanRel = strings.TrimPrefix(cleanRel, "./")
		if cleanRel == "" || cleanRel == "/" {
			cleanRel = "."
		}

		if strings.HasPrefix(cleanRel, "..") || cleanRel == ".." {
			return nil, fmt.Errorf("bind mount source '%s' escapes project source root boundary", vol.Source)
		}

		resolvedAgent := paths.AgentPath
		resolvedBackend := paths.BackendPath
		resolvedDaemon := paths.DaemonPath

		if cleanRel != "." {
			resolvedAgent = joinHostPath(paths.AgentPath, cleanRel)
			if paths.BackendPath != "" {
				resolvedBackend = joinContainerPath(paths.BackendPath, cleanRel)
			}
			resolvedDaemon = joinHostPath(paths.DaemonPath, cleanRel)
		}

		// Boundary check on backend path if available
		if paths.BackendPath != "" {
			cleanBackendRoot := filepath.Clean(paths.BackendPath)
			relCheck, rErr := filepath.Rel(cleanBackendRoot, filepath.Clean(resolvedBackend))
			if rErr != nil || strings.HasPrefix(relCheck, "..") || relCheck == ".." {
				return nil, fmt.Errorf("bind mount source '%s' escapes project source root boundary", vol.Source)
			}
		}

		// Boundary check on daemon path
		if paths.DaemonPath != "" {
			if _, ok := getRelativePathCrossPlatform(paths.DaemonPath, resolvedDaemon); !ok {
				return nil, fmt.Errorf("bind mount source '%s' escapes project source root boundary", vol.Source)
			}
		}

		// Boundary check on agent path
		if paths.AgentPath != "" {
			if _, ok := getRelativePathCrossPlatform(paths.AgentPath, resolvedAgent); !ok {
				return nil, fmt.Errorf("bind mount source '%s' escapes project source root boundary", vol.Source)
			}
		}

		// Verify accessibility on backend-visible path, or agent host path if backend path is not separate
		accessible := false
		if resolvedBackend != "" {
			if fi, sErr := os.Stat(resolvedBackend); sErr == nil {
				// Prevent symlink escape within backend container
				if eval, eErr := filepath.EvalSymlinks(resolvedBackend); eErr == nil {
					cleanEval := filepath.Clean(eval)
					cleanBackendRoot := filepath.Clean(paths.BackendPath)
					if relEval, relErr := filepath.Rel(cleanBackendRoot, cleanEval); relErr == nil && !strings.HasPrefix(relEval, "..") && relEval != ".." {
						accessible = true
					}
				} else {
					accessible = true
				}
				_ = fi
			}
		}

		if !accessible && resolvedAgent != "" {
			if fi, sErr := os.Stat(resolvedAgent); sErr == nil {
				if eval, eErr := filepath.EvalSymlinks(resolvedAgent); eErr == nil {
					if _, ok := getRelativePathCrossPlatform(paths.AgentPath, eval); ok {
						accessible = true
					}
				} else {
					accessible = true
				}
				_ = fi
			}
		}

		if !accessible {
			if project.SourceType == models.SourceTypeLocalAgent {
				hostRoot := ""
				if pathValidator != nil {
					hostRoot = pathValidator.HostSourceRoot()
				}
				if hostRoot != "" {
					if _, ok := getRelativePathCrossPlatform(hostRoot, paths.AgentPath); !ok {
						return nil, fmt.Errorf("bind mount source directory '%s' (project root '%s') is outside the backend's mounted host source root ('%s'); relative bind mounts require the project to be located within the configured FORGELAB_HOST_SOURCE_ROOT or backend container mount", vol.Source, paths.AgentPath, hostRoot)
					}
				}
			}
			targetPath := resolvedBackend
			if targetPath == "" {
				targetPath = resolvedDaemon
			}
			return nil, fmt.Errorf("bind mount source directory '%s' (resolved to '%s') does not exist or is not accessible from backend environment", vol.Source, targetPath)
		}

		// Security: validate resolved daemon path against restricted system paths
		if err := validateRestrictedHostMount(resolvedDaemon); err != nil {
			return nil, err
		}

		return &ResolvedVolumeMount{
			AgentPath:     resolvedAgent,
			BackendPath:   resolvedBackend,
			DaemonPath:    resolvedDaemon,
			HostPath:      resolvedDaemon,
			ContainerPath: cleanTarget,
			ReadOnly:      vol.ReadOnly,
		}, nil
	}

	// Absolute bind mount: validate against sensitive host paths and path boundaries
	cleanHost := filepath.Clean(vol.Source)
	resolvedDaemon := cleanHost
	resolvedBackend := cleanHost
	resolvedAgent := cleanHost

	// If absolute path matches agent, backend, or daemon paths, translate appropriately
	if paths.AgentPath != "" {
		if rel, ok := getRelativePathCrossPlatform(paths.AgentPath, cleanHost); ok {
			resolvedAgent = cleanHost
			if paths.BackendPath != "" {
				resolvedBackend = joinContainerPath(paths.BackendPath, rel)
			}
			if paths.DaemonPath != "" {
				resolvedDaemon = joinHostPath(paths.DaemonPath, rel)
			}
		}
	}
	if paths.BackendPath != "" && cleanHost != resolvedBackend {
		if rel, ok := getRelativePathCrossPlatform(paths.BackendPath, cleanHost); ok {
			resolvedBackend = cleanHost
			if paths.DaemonPath != "" {
				resolvedDaemon = joinHostPath(paths.DaemonPath, rel)
			}
			if paths.AgentPath != "" {
				resolvedAgent = joinHostPath(paths.AgentPath, rel)
			}
		}
	}

	if err := validateRestrictedHostMount(resolvedDaemon); err != nil {
		return nil, err
	}

	return &ResolvedVolumeMount{
		AgentPath:     resolvedAgent,
		BackendPath:   resolvedBackend,
		DaemonPath:    resolvedDaemon,
		HostPath:      resolvedDaemon,
		ContainerPath: cleanTarget,
		ReadOnly:      vol.ReadOnly,
	}, nil
}

func validateRestrictedHostMount(p string) error {
	cleanHost := filepath.Clean(p)
	lowerHost := strings.ToLower(cleanHost)
	normHost := strings.ReplaceAll(lowerHost, "\\", "/")

	for _, restricted := range restrictedHostMounts {
		cleanRestricted := strings.ToLower(filepath.Clean(restricted))
		normRestricted := strings.ReplaceAll(cleanRestricted, "\\", "/")
		if normHost == normRestricted || strings.HasPrefix(normHost, normRestricted+"/") ||
			lowerHost == cleanRestricted || strings.HasPrefix(lowerHost, cleanRestricted+string(filepath.Separator)) || strings.HasPrefix(lowerHost, cleanRestricted+"/") {
			return fmt.Errorf("%w: %s", ErrSensitiveHostMountRejected, cleanHost)
		}
	}
	return nil
}
