package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrPathNotExist        = errors.New("repository path does not exist")
	ErrPathNotDirectory   = errors.New("repository path is not a directory")
	ErrPathNotAllowed     = errors.New("repository path is outside allowed source root directory")
	ErrRestrictedSystemPath = errors.New("access to system directory is forbidden")
	ErrDockerfileNotFound = errors.New("configured Dockerfile does not exist in build context")
)

// PathValidator validates local repository paths and build contexts.
type PathValidator struct {
	allowedRoots []string
}

// NewPathValidator creates a new PathValidator with configured allowed root directories.
// If no allowed roots are provided, defaults to current working directory and user's home directory.
func NewPathValidator(allowedRoots []string) *PathValidator {
	var cleanRoots []string
	for _, root := range allowedRoots {
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err == nil {
			eval, err := filepath.EvalSymlinks(abs)
			if err == nil {
				cleanRoots = append(cleanRoots, filepath.Clean(eval))
			} else {
				cleanRoots = append(cleanRoots, filepath.Clean(abs))
			}
		}
	}

	return &PathValidator{
		allowedRoots: cleanRoots,
	}
}

// ValidateSourcePath validates that candidatePath exists, is a directory, is canonicalized,
// is not a system directory, and stays within configured allowed roots if any exist.
func (v *PathValidator) ValidateSourcePath(candidatePath string) (string, error) {
	if strings.TrimSpace(candidatePath) == "" {
		return "", ErrPathNotExist
	}

	// 1. Convert to absolute path
	absPath, err := filepath.Abs(candidatePath)
	if err != nil {
		return "", fmt.Errorf("invalid path format: %w", err)
	}

	// 2. Canonicalize by evaluating symlinks
	canonicalPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrPathNotExist
		}
		return "", fmt.Errorf("failed to evaluate symlinks: %w", err)
	}

	canonicalPath = filepath.Clean(canonicalPath)

	// 3. Verify existence and directory stat
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

	// 4. Block dangerous system directories
	systemDirs := []string{
		"/etc", "/var", "/usr", "/sys", "/proc", "/dev", "/boot", "/bin", "/sbin",
		`C:\Windows`, `C:\Program Files`, `C:\Program Files (x86)`, `C:\System Volume Information`,
	}
	for _, sysDir := range systemDirs {
		cleanSysDir := filepath.Clean(sysDir)
		if strings.EqualFold(canonicalPath, cleanSysDir) || strings.HasPrefix(strings.ToLower(canonicalPath), strings.ToLower(cleanSysDir)+string(filepath.Separator)) {
			return "", ErrRestrictedSystemPath
		}
	}

	// 5. If allowedRoots are specified, check boundary
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

	// Resolve build context relative to sourcePath
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
