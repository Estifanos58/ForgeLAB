package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/agent"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
)

// ResolvedVolumeMount encapsulates the normalized parameters for a Docker volume mount.
type ResolvedVolumeMount struct {
	IsNamedVolume  bool
	VolumeName     string
	HostPath       string
	ContainerPath  string
	ReadOnly       bool
	IsDockerSocket bool
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

// ResolveProjectSourceRoot determines the authoritative host and container source root paths for a project.
func ResolveProjectSourceRoot(ctx context.Context, project *models.Project, sourceService *services.SourceService, pathValidator *security.PathValidator) (hostSourceRoot string, containerSourceRoot string, err error) {
	if project == nil {
		return "", "", nil
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

		// 2. Query in-memory session manager, local sessions, DB sources, or agent
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
			return "", "", fmt.Errorf("unable to determine local-agent host source directory for project '%s'. Ensure agent session is active.", project.Name)
		}

		hostRoot := filepath.Clean(sourcePath)
		containerRoot := hostRoot
		if pathValidator != nil {
			if c, tErr := pathValidator.TranslateHostToContainer(hostRoot); tErr == nil && c != "" {
				containerRoot = filepath.Clean(c)
			}
		}
		return hostRoot, containerRoot, nil

	case models.SourceTypeLocalDirectory, models.SourceTypeLocal:
		rawPath := strings.TrimSpace(project.RepositoryPath)
		if rawPath == "" {
			return "", "", errors.New("repository_path is required for local directory projects")
		}
		hostRoot := filepath.Clean(rawPath)
		containerRoot := hostRoot
		if pathValidator != nil {
			if h, tErr := pathValidator.TranslateContainerToHost(rawPath); tErr == nil && h != "" {
				hostRoot = filepath.Clean(h)
			}
			if c, tErr := pathValidator.TranslateHostToContainer(rawPath); tErr == nil && c != "" {
				containerRoot = filepath.Clean(c)
			}
		}
		return hostRoot, containerRoot, nil

	case models.SourceTypeLocalUpload:
		if sourceService != nil && project.SourceReference != "" {
			if srcUUID, parseErr := uuid.Parse(project.SourceReference); parseErr == nil {
				if p, pErr := sourceService.GetSourcePath(ctx, project.OwnerID, srcUUID); pErr == nil && p != "" {
					cleanP := filepath.Clean(p)
					return cleanP, cleanP, nil
				}
			}
		}
		return "", "", fmt.Errorf("source files for uploaded archive project '%s' are unavailable", project.Name)

	case models.SourceTypeGitHub:
		// GitHub repos are cloned temporarily for builds and do not provide persistent runtime host directories
		return "", "", nil

	default:
		return "", "", nil
	}
}

// ResolveAndValidateVolumeMount resolves and validates a volume mount configuration consistently for both preflight and runtime engine.
func ResolveAndValidateVolumeMount(ctx context.Context, project *models.Project, vol models.VolumeMountConfig, sourceService *services.SourceService, pathValidator *security.PathValidator) (*ResolvedVolumeMount, error) {
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

	hostRoot, containerRoot, err := ResolveProjectSourceRoot(ctx, project, sourceService, pathValidator)
	if err != nil {
		return nil, err
	}

	if isRelativePath(vol.Source) {
		cleanRel := filepath.Clean(filepath.FromSlash(vol.Source))
		if cleanRel == "." || cleanRel == "" {
			cleanRel = "."
		}

		if strings.HasPrefix(cleanRel, "..") || cleanRel == ".." {
			return nil, fmt.Errorf("bind mount source '%s' escapes project source root boundary", vol.Source)
		}

		resolvedHost := filepath.Clean(filepath.Join(hostRoot, cleanRel))
		resolvedContainer := filepath.Clean(filepath.Join(containerRoot, cleanRel))

		// Strict repository boundary check
		cleanHostRoot := filepath.Clean(hostRoot)
		relCheck, rErr := filepath.Rel(cleanHostRoot, resolvedHost)
		if rErr != nil || strings.HasPrefix(relCheck, "..") || relCheck == ".." {
			return nil, fmt.Errorf("bind mount source '%s' escapes project source root boundary", vol.Source)
		}

		// Verify accessibility on host or container environment
		accessible := false
		if fi, sErr := os.Stat(resolvedContainer); sErr == nil {
			if !fi.IsDir() && vol.Target != resolvedContainer {
				// File or dir accessible
			}
			accessible = true
		} else if fi, sErr := os.Stat(resolvedHost); sErr == nil {
			if !fi.IsDir() && vol.Target != resolvedHost {
				// File or dir accessible
			}
			accessible = true
		}

		if !accessible {
			return nil, fmt.Errorf("bind mount source directory '%s' (resolved to '%s') does not exist or is not accessible from backend environment", vol.Source, resolvedHost)
		}

		return &ResolvedVolumeMount{
			HostPath:      resolvedHost,
			ContainerPath: cleanTarget,
			ReadOnly:      vol.ReadOnly,
		}, nil
	}

	// Absolute bind mount: validate against sensitive host paths and path boundaries
	cleanHost := filepath.Clean(vol.Source)
	lowerHost := strings.ToLower(cleanHost)

	for _, restricted := range restrictedHostMounts {
		cleanRestricted := strings.ToLower(filepath.Clean(restricted))
		if lowerHost == cleanRestricted || strings.HasPrefix(lowerHost, cleanRestricted+string(filepath.Separator)) || strings.HasPrefix(lowerHost, cleanRestricted+"/") {
			return nil, fmt.Errorf("%w: %s", ErrSensitiveHostMountRejected, cleanHost)
		}
	}

	return &ResolvedVolumeMount{
		HostPath:      cleanHost,
		ContainerPath: cleanTarget,
		ReadOnly:      vol.ReadOnly,
	}, nil
}
