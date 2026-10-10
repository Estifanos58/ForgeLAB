package security

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

var (
	ErrPathNotExist         = errors.New("the selected directory does not exist or is not accessible from the backend environment")
	ErrPathNotDirectory     = errors.New("the selected path is not a directory")
	ErrPathNotAllowed       = errors.New("this directory is outside the configured ForgeLAB source roots")
	ErrRestrictedSystemPath = errors.New("access to system directory is forbidden")
	ErrDockerfileNotFound   = errors.New("configured Dockerfile does not exist in build context")
	ErrNoBuildFiles         = errors.New("the project contains no usable build files")
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

// HostSourceRoot returns the configured host source root directory.
func (v *PathValidator) HostSourceRoot() string {
	if v == nil {
		return ""
	}
	return v.hostSourceRoot
}

// ContainerSourceRoot returns the configured container source root directory.
func (v *PathValidator) ContainerSourceRoot() string {
	if v == nil {
		return ""
	}
	return v.containerSourceRoot
}

// AllowedRoots returns a copy of configured allowed source roots.
func (v *PathValidator) AllowedRoots() []string {
	if v == nil {
		return nil
	}
	return append([]string(nil), v.allowedRoots...)
}

// TranslateContainerToHost translates a container path to its host-mapped path if host and container source roots are configured.
func (v *PathValidator) TranslateContainerToHost(candidatePath string) (string, error) {
	trimmed := strings.TrimSpace(candidatePath)
	if trimmed == "" {
		return "", ErrPathNotExist
	}

	if v.hostSourceRoot == "" || v.containerSourceRoot == "" {
		return trimmed, nil
	}

	normContainerRoot := normalizePathForPrefix(v.containerSourceRoot)
	normCandidate := normalizePathForPrefix(trimmed)

	if normCandidate == normContainerRoot {
		return v.hostSourceRoot, nil
	}

	prefixWithSep := normContainerRoot
	if !strings.HasSuffix(prefixWithSep, "/") {
		prefixWithSep += "/"
	}

	if strings.HasPrefix(strings.ToLower(normCandidate), strings.ToLower(prefixWithSep)) {
		rel := normCandidate[len(prefixWithSep):]
		cleanRel := filepath.Clean(filepath.FromSlash(rel))
		if strings.HasPrefix(cleanRel, "..") || cleanRel == ".." {
			return "", ErrPathNotAllowed
		}
		// Join with hostSourceRoot preserving host format
		return filepath.Join(v.hostSourceRoot, cleanRel), nil
	}

	return trimmed, nil
}

func normalizePathForPrefix(p string) string {
	s := strings.TrimSpace(p)
	s = strings.ReplaceAll(s, "\\", "/")
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

// ValidateServiceBuildPaths validates service SourcePath, BuildContext, and DockerfilePath against canonical source root.
// It strictly rejects absolute paths outside source root, .. traversal, symlink escapes,
// build contexts outside repo, and Dockerfiles outside the build context.
func (v *PathValidator) ValidateServiceBuildPaths(
	canonicalSourceRoot string,
	serviceSourcePath string,
	buildContext string,
	dockerfilePath string,
	isAutoBuild bool,
) (resolvedContextDir string, relDockerPath string, err error) {
	// 1. Validate canonicalSourceRoot
	cleanRoot, err := filepath.Abs(canonicalSourceRoot)
	if err != nil {
		return "", "", fmt.Errorf("invalid source root path: %w", err)
	}
	cleanRoot, err = filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		return "", "", fmt.Errorf("source root does not exist: %w", err)
	}
	cleanRoot = filepath.Clean(cleanRoot)

	// 2. Validate serviceSourcePath within cleanRoot
	cleanSourcePath := strings.TrimSpace(serviceSourcePath)
	if cleanSourcePath == "" {
		cleanSourcePath = "."
	}

	// Reject absolute paths that attempt to reference outside cleanRoot
	if filepath.IsAbs(cleanSourcePath) {
		absSource, err := filepath.EvalSymlinks(filepath.Clean(cleanSourcePath))
		if err != nil {
			return "", "", fmt.Errorf("service source path does not exist: %w", err)
		}
		rel, err := filepath.Rel(cleanRoot, absSource)
		if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
			return "", "", errors.New("service source path is an absolute path outside repository boundary")
		}
		cleanSourcePath = rel
	}

	targetServiceDir := filepath.Join(cleanRoot, cleanSourcePath)
	absServiceDir, err := filepath.Abs(targetServiceDir)
	if err != nil {
		return "", "", fmt.Errorf("invalid service source directory: %w", err)
	}
	canonicalServiceDir, err := filepath.EvalSymlinks(absServiceDir)
	if err != nil {
		return "", "", fmt.Errorf("service source path does not exist: %w", err)
	}
	canonicalServiceDir = filepath.Clean(canonicalServiceDir)

	// Ensure canonicalServiceDir is within cleanRoot
	relService, err := filepath.Rel(cleanRoot, canonicalServiceDir)
	if err != nil || strings.HasPrefix(relService, "..") {
		return "", "", errors.New("service source path escapes repository boundary")
	}

	// 3. Validate buildContext
	cleanBuildContext := strings.TrimSpace(buildContext)
	if cleanBuildContext == "" {
		cleanBuildContext = "."
	}

	var targetContextDir string
	if filepath.IsAbs(cleanBuildContext) {
		absCtx, err := filepath.EvalSymlinks(filepath.Clean(cleanBuildContext))
		if err != nil {
			return "", "", fmt.Errorf("build context path does not exist: %w", err)
		}
		rel, err := filepath.Rel(cleanRoot, absCtx)
		if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
			return "", "", errors.New("build context is an absolute path outside repository boundary")
		}
		targetContextDir = absCtx
	} else {
		cleanRel := filepath.Clean(filepath.FromSlash(cleanBuildContext))
		if strings.HasPrefix(cleanRel, "..") {
			return "", "", errors.New("build context escapes repository boundary")
		}

		// Primary resolution: buildContext relative to repository / workspace root
		candRoot := filepath.Join(cleanRoot, cleanRel)
		if fi, err := os.Stat(candRoot); err == nil && fi.IsDir() {
			targetContextDir = candRoot
		} else if canonicalServiceDir != cleanRoot {
			// Secondary resolution: if not found directly under cleanRoot, check relative to service directory
			candSvc := filepath.Join(canonicalServiceDir, cleanRel)
			if fi, err := os.Stat(candSvc); err == nil && fi.IsDir() {
				relSvc, err := filepath.Rel(cleanRoot, candSvc)
				if err == nil && !strings.HasPrefix(relSvc, "..") {
					targetContextDir = candSvc
				} else {
					return "", "", errors.New("build context escapes repository boundary")
				}
			} else if cleanRel == "." {
				targetContextDir = canonicalServiceDir
			} else {
				return "", "", fmt.Errorf("build context directory does not exist: %s", cleanBuildContext)
			}
		} else if cleanRel == "." {
			targetContextDir = cleanRoot
		} else {
			return "", "", fmt.Errorf("build context directory does not exist: %s", cleanBuildContext)
		}
	}

	absContextDir, err := filepath.Abs(targetContextDir)
	if err != nil {
		return "", "", fmt.Errorf("invalid build context path: %w", err)
	}
	canonicalContextDir, err := filepath.EvalSymlinks(absContextDir)
	if err != nil {
		return "", "", fmt.Errorf("build context directory does not exist: %w", err)
	}
	canonicalContextDir = filepath.Clean(canonicalContextDir)

	// Ensure canonicalContextDir does not escape cleanRoot
	relContext, err := filepath.Rel(cleanRoot, canonicalContextDir)
	if err != nil || strings.HasPrefix(relContext, "..") {
		return "", "", errors.New("build context escapes repository boundary")
	}

	// 4. Validate DockerfilePath
	cleanDockerfilePath := strings.TrimSpace(dockerfilePath)
	if isAutoBuild {
		return canonicalContextDir, "Dockerfile.forgelab", nil
	}

	if cleanDockerfilePath == "" {
		cleanDockerfilePath = "Dockerfile"
	}

	if filepath.IsAbs(cleanDockerfilePath) {
		absDF, err := filepath.EvalSymlinks(filepath.Clean(cleanDockerfilePath))
		if err != nil {
			return "", "", ErrDockerfileNotFound
		}
		rel, err := filepath.Rel(canonicalContextDir, absDF)
		if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
			return "", "", errors.New("dockerfile escapes build context boundary")
		}
		cleanDockerfilePath = filepath.ToSlash(rel)
	}

	targetDF := filepath.Join(canonicalContextDir, cleanDockerfilePath)
	absDF, err := filepath.Abs(targetDF)
	if err != nil {
		return "", "", fmt.Errorf("invalid dockerfile path: %w", err)
	}
	canonicalDF, err := filepath.EvalSymlinks(absDF)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", ErrDockerfileNotFound
		}
		return "", "", fmt.Errorf("dockerfile path error: %w", err)
	}
	canonicalDF = filepath.Clean(canonicalDF)

	// Ensure canonicalDF is inside canonicalContextDir
	relDF, err := filepath.Rel(canonicalContextDir, canonicalDF)
	if err != nil || strings.HasPrefix(relDF, "..") {
		return "", "", errors.New("dockerfile escapes build context boundary")
	}

	dfInfo, err := os.Stat(canonicalDF)
	if err != nil || dfInfo.IsDir() {
		return "", "", ErrDockerfileNotFound
	}

	return canonicalContextDir, filepath.ToSlash(relDF), nil
}

// ValidateRelativeServicePath validates and sanitizes a relative service path (e.g. for local_agent).
// It strictly validates path syntax and boundary without performing host filesystem checks:
// - Rejects absolute POSIX paths (/...)
// - Rejects Windows drive paths (C:\..., C:/...)
// - Rejects UNC paths (\\..., //...)
// - Rejects '..' traversal components
// - Rejects paths that normalize outside '.'
// - Normalizes separators safely to forward slashes
// - Allows valid relative paths such as '.', 'frontend', 'backend', 'apps/web'
func ValidateRelativeServicePath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "." {
		return ".", nil
	}

	// 1. Check for UNC network paths (\\server\share or //server/share)
	if strings.HasPrefix(trimmed, `\\`) || strings.HasPrefix(trimmed, "//") {
		return "", errors.New("UNC network paths are not allowed")
	}

	// 2. Check for POSIX absolute paths (/foo) or backslash rooted paths (\foo)
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, `\`) {
		return "", errors.New("absolute paths are not allowed")
	}

	// 3. Check for Windows drive letters (e.g., C:, D:, etc.)
	if len(trimmed) >= 2 && trimmed[1] == ':' && unicode.IsLetter(rune(trimmed[0])) {
		return "", errors.New("windows drive letter paths are not allowed")
	}

	// 4. Normalize all backslashes to forward slashes
	slashPath := strings.ReplaceAll(trimmed, `\`, "/")

	// 5. Check components for '..' traversal and colons
	parts := strings.Split(slashPath, "/")
	for _, part := range parts {
		p := strings.TrimSpace(part)
		if p == ".." {
			return "", errors.New("path traversal '..' is not allowed")
		}
		if strings.Contains(p, ":") {
			return "", errors.New("colons are not allowed in path components")
		}
	}

	// 6. Clean the path using path.Clean (clean POSIX paths)
	cleaned := path.Clean(slashPath)
	if cleaned == "." || cleaned == "" {
		return ".", nil
	}

	// 7. Verify cleaned path does not escape or become absolute
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", errors.New("path escapes root boundary")
	}

	return cleaned, nil
}

// ValidateRelativeServicePath validates a relative service path using the validator instance.
func (v *PathValidator) ValidateRelativeServicePath(raw string) (string, error) {
	return ValidateRelativeServicePath(raw)
}
