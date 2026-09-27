package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrPathNotExist         = errors.New("the selected directory does not exist or is not accessible from the backend environment")
	ErrPathNotDirectory    = errors.New("the selected path is not a directory")
	ErrPathNotAllowed      = errors.New("this directory is outside the configured ForgeLAB source roots")
	ErrRestrictedSystemPath = errors.New("access to system directory is forbidden")
	ErrDockerfileNotFound  = errors.New("configured Dockerfile does not exist in build context")
	ErrNoBuildFiles        = errors.New("the project contains no usable build files")
)

// PathValidator validates local repository paths and build contexts.
type PathValidator struct {
	allowedRoots        []string
	hostSourceRoot      string
	containerSourceRoot string
}

// NewPathValidator creates a new PathValidator with configured allowed root directories.
func NewPathValidator(allowedRoots []string) *PathValidator {
	return NewPathValidatorWithMapping(allowedRoots, "", "")
}

// NewPathValidatorWithMapping creates a PathValidator with allowed roots and optional host-to-container path translation.
func NewPathValidatorWithMapping(allowedRoots []string, hostSourceRoot, containerSourceRoot string) *PathValidator {
	var cleanRoots []string
	for _, root := range allowedRoots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		abs, err := filepath.Abs(strings.TrimSpace(root))
		if err == nil {
			eval, err := filepath.EvalSymlinks(abs)
			if err == nil {
				cleanRoots = append(cleanRoots, filepath.Clean(eval))
			} else {
				cleanRoots = append(cleanRoots, filepath.Clean(abs))
			}
		}
	}

	cleanHost := strings.TrimSpace(hostSourceRoot)
	cleanContainer := strings.TrimSpace(containerSourceRoot)

	if cleanContainer != "" {
		absContainer, err := filepath.Abs(cleanContainer)
		if err == nil {
			cleanContainer = filepath.Clean(absContainer)
		}
	}

	return &PathValidator{
		allowedRoots:        cleanRoots,
		hostSourceRoot:      cleanHost,
		containerSourceRoot: cleanContainer,
	}
}

// TranslateHostToContainer translates a host path to its container-mapped path if host and container source roots are configured.
// If mapping is not configured or the path does not match hostSourceRoot, returns candidatePath.
func (v *PathValidator) TranslateHostToContainer(candidatePath string) (string, error) {
	trimmed := strings.TrimSpace(candidatePath)
	if trimmed == "" {
		return "", ErrPathNotExist
	}

	if v.hostSourceRoot == "" || v.containerSourceRoot == "" {
		return trimmed, nil
	}

	// Normalize paths for comparison (supporting both Windows and POSIX path separators)
	normHostRoot := normalizePathForPrefix(v.hostSourceRoot)
	normCandidate := normalizePathForPrefix(trimmed)

	if normCandidate == normHostRoot {
		return v.containerSourceRoot, nil
	}

	prefixWithSep := normHostRoot
	if !strings.HasSuffix(prefixWithSep, "/") {
		prefixWithSep += "/"
	}

	if strings.HasPrefix(strings.ToLower(normCandidate), strings.ToLower(prefixWithSep)) {
		rel := normCandidate[len(prefixWithSep):]
		// Prevent traversal within relative portion
		cleanRel := filepath.Clean(filepath.FromSlash(rel))
		if strings.HasPrefix(cleanRel, "..") || cleanRel == ".." {
			return "", ErrPathNotAllowed
		}
		return filepath.Join(v.containerSourceRoot, cleanRel), nil
	}

	// Check if path is already inside containerSourceRoot
	normContainerRoot := normalizePathForPrefix(v.containerSourceRoot)
	if normCandidate == normContainerRoot || strings.HasPrefix(strings.ToLower(normCandidate), strings.ToLower(normContainerRoot+"/")) {
		return trimmed, nil
	}

	return trimmed, nil
}

func normalizePathForPrefix(p string) string {
	s := strings.TrimSpace(p)
	s = filepath.ToSlash(s)
	s = strings.TrimRight(s, "/")
	return s
}

// ValidateSourcePath validates that candidatePath exists, is a directory, is canonicalized,
// is not a system directory, and stays within configured allowed roots if any exist.
func (v *PathValidator) ValidateSourcePath(candidatePath string) (string, error) {
	if strings.TrimSpace(candidatePath) == "" {
		return "", ErrPathNotExist
	}

	// 1. Translate host path to container path if configured
	resolvedPath, err := v.TranslateHostToContainer(candidatePath)
	if err != nil {
		return "", err
	}

	// 2. Convert to absolute path
	absPath, err := filepath.Abs(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("invalid path format: %w", err)
	}

	// 3. Canonicalize by evaluating symlinks
	canonicalPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrPathNotExist
		}
		return "", fmt.Errorf("failed to evaluate symlinks: %w", err)
	}

	canonicalPath = filepath.Clean(canonicalPath)

	// 4. Verify existence and directory stat
	info, err := os.Stat(canonicalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrPathNotExist
		}
		return "", fmt.Errorf("failed to stat path: %w", err)
	}
	if !info.IsDir() {
		return "", ErrPathNotDirectory
	}

	// 5. Block dangerous system directories
	systemDirs := []string{
		"/etc", "/var", "/usr", "/sys", "/proc", "/dev", "/boot", "/bin", "/sbin", "/root",
		`C:\Windows`, `C:\Program Files`, `C:\Program Files (x86)`, `C:\System Volume Information`,
		`C:\Recovery`,
	}
	for _, sysDir := range systemDirs {
		cleanSysDir := filepath.Clean(sysDir)
		if strings.EqualFold(canonicalPath, cleanSysDir) || strings.HasPrefix(strings.ToLower(canonicalPath), strings.ToLower(cleanSysDir)+string(filepath.Separator)) {
			return "", ErrRestrictedSystemPath
		}
	}

	// 6. Check containerSourceRoot boundary if mapping is enabled
	if v.containerSourceRoot != "" && (v.hostSourceRoot != "" || len(v.allowedRoots) == 0) {
		// If containerSourceRoot is set and candidate was mapped into it, verify boundary
		relContainer, err := filepath.Rel(v.containerSourceRoot, canonicalPath)
		if err == nil && !strings.HasPrefix(relContainer, "..") && relContainer != ".." {
			return canonicalPath, nil
		}
	}

	// 7. If allowedRoots are specified, check boundary
	if len(v.allowedRoots) > 0 {
		allowed := false
		for _, root := range v.allowedRoots {
			rel, err := filepath.Rel(root, canonicalPath)
			if err == nil && !strings.HasPrefix(rel, "..") && rel != ".." {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", ErrPathNotAllowed
		}
	}

	return canonicalPath, nil
}

// ValidateBuildContextAndDockerfile verifies that dockerfilePath and buildContext reside safely within sourcePath.
func (v *PathValidator) ValidateBuildContextAndDockerfile(sourcePath, buildContext, dockerfilePath string) (fullBuildContext string, fullDockerfile string, err error) {
	canonicalSource, err := v.ValidateSourcePath(sourcePath)
	if err != nil {
		return "", "", err
	}

	if buildContext == "" {
		buildContext = "."
	}

	// Resolve build context relative to canonicalSource
	targetCtx := filepath.Join(canonicalSource, buildContext)
	absCtx, err := filepath.Abs(targetCtx)
	if err != nil {
		return "", "", fmt.Errorf("invalid build context path: %w", err)
	}

	canonicalCtx, err := filepath.EvalSymlinks(absCtx)
	if err != nil {
		return "", "", fmt.Errorf("build context does not exist: %w", err)
	}
	canonicalCtx = filepath.Clean(canonicalCtx)

	// Ensure build context is within sourcePath
	relCtx, err := filepath.Rel(canonicalSource, canonicalCtx)
	if err != nil || strings.HasPrefix(relCtx, "..") {
		return "", "", errors.New("build context escapes repository path boundary")
	}

	if dockerfilePath == "" {
		dockerfilePath = "Dockerfile"
	}

	// Resolve Dockerfile relative to canonicalCtx
	targetDockerfile := filepath.Join(canonicalCtx, dockerfilePath)
	absDockerfile, err := filepath.Abs(targetDockerfile)
	if err != nil {
		return "", "", fmt.Errorf("invalid dockerfile path: %w", err)
	}

	canonicalDockerfile, err := filepath.EvalSymlinks(absDockerfile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", ErrDockerfileNotFound
		}
		return "", "", fmt.Errorf("dockerfile path error: %w", err)
	}
	canonicalDockerfile = filepath.Clean(canonicalDockerfile)

	// Ensure Dockerfile is within canonicalCtx
	relDocker, err := filepath.Rel(canonicalCtx, canonicalDockerfile)
	if err != nil || strings.HasPrefix(relDocker, "..") {
		return "", "", errors.New("dockerfile escapes build context boundary")
	}

	dfInfo, err := os.Stat(canonicalDockerfile)
	if err != nil || dfInfo.IsDir() {
		return "", "", ErrDockerfileNotFound
	}

	return canonicalCtx, canonicalDockerfile, nil
}
