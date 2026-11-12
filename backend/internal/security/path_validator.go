package security

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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
	daemonSourceRoot    string
	mu                  sync.RWMutex
}

// NewPathValidator creates a new PathValidator with configured allowed root directories.
func NewPathValidator(allowedRoots []string) *PathValidator {
	return NewPathValidatorWithMapping(allowedRoots, "", "")
}

// NormalizeReadlinkTarget normalizes Windows object-manager prefixes (e.g. \??\C:\... or \??\UNC\...)
// returned by NTFS junctions and reparse points into standard Win32 paths.
func NormalizeReadlinkTarget(target string) string {
	if strings.HasPrefix(target, `\??\`) {
		trimmed := strings.TrimPrefix(target, `\??\`)
		if len(trimmed) >= 2 && trimmed[1] == ':' {
			return trimmed
		}
		if strings.HasPrefix(trimmed, `UNC\`) {
			return `\\` + strings.TrimPrefix(trimmed, `UNC\`)
		}
	}
	return target
}

func isUNCPath(raw string) bool {
	return strings.HasPrefix(raw, `\\`) || strings.HasPrefix(raw, `//`)
}

func stepResolveFirstReparse(cleanAbs string) (next string, changed bool, missingAt string, err error) {
	vol := filepath.VolumeName(cleanAbs)
	rest := strings.TrimPrefix(cleanAbs, vol)
	rest = strings.TrimLeft(rest, `/\`)

	prefix := vol
	if vol != "" && strings.HasPrefix(strings.TrimPrefix(cleanAbs, vol), string(os.PathSeparator)) {
		prefix += string(os.PathSeparator)
	} else if vol == "" && strings.HasPrefix(cleanAbs, string(os.PathSeparator)) {
		prefix = string(os.PathSeparator)
	}
	if rest == "" {
		return cleanAbs, false, "", nil
	}

	parts := strings.Split(rest, string(os.PathSeparator))
	cur := prefix
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		lfi, lErr := os.Lstat(cur)
		if lErr != nil {
			return "", false, cur, lErr
		}
		if lfi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			target, rErr := os.Readlink(cur)
			if rErr == nil {
				target = NormalizeReadlinkTarget(strings.TrimSpace(target))
				if target == "" {
					return "", false, "", fmt.Errorf("empty reparse target for %s", cur)
				}
				if runtime.GOOS == "windows" && isUNCPath(target) {
					return "", false, "", ErrRestrictedSystemPath
				}
				if !filepath.IsAbs(target) {
					target = filepath.Join(filepath.Dir(cur), target)
				}
				rem := make([]string, 0, 1+len(parts)-i-1)
				rem = append(rem, filepath.Clean(target))
				rem = append(rem, parts[i+1:]...)
				return filepath.Clean(filepath.Join(rem...)), true, "", nil
			}
		}
	}
	return cleanAbs, false, "", nil
}

// CanonicalizeExistingPath resolves symlinks, Windows NTFS junctions/reparse points,
// and Windows 8.3 short paths (e.g. RUNNER~1) for an existing filesystem path.
func CanonicalizeExistingPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("path is empty")
	}
	if runtime.GOOS == "windows" && isUNCPath(trimmed) {
		return "", ErrRestrictedSystemPath
	}

	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid path format: %w", err)
	}
	cur := filepath.Clean(abs)
	if runtime.GOOS == "windows" && isUNCPath(cur) {
		return "", ErrRestrictedSystemPath
	}

	const maxLinkHops = 40
	for hop := 0; hop < maxLinkHops; hop++ {
		next, changed, _, sErr := stepResolveFirstReparse(cur)
		if sErr != nil {
			return "", sErr
		}
		if changed {
			cur = next
			if runtime.GOOS == "windows" && isUNCPath(cur) {
				return "", ErrRestrictedSystemPath
			}
			continue
		}

		eval, eErr := filepath.EvalSymlinks(cur)
		if eErr != nil {
			return "", eErr
		}
		cleanEval := filepath.Clean(eval)
		if cleanEval == cur {
			return cleanEval, nil
		}
		cur = cleanEval
	}
	return "", errors.New("too many levels of symbolic links or junctions")
}

// CanonicalizeWithAncestor canonicalizes an existing path or, when the leaf does not yet exist
// (such as a broken symlink target or pre-creation directory), resolves symlinks, NTFS junctions,
// and 8.3 short segments through the deepest existing ancestor after following any reparse hops.
func CanonicalizeWithAncestor(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("path is empty")
	}
	if runtime.GOOS == "windows" && isUNCPath(trimmed) {
		return "", ErrRestrictedSystemPath
	}

	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", err
	}
	cur := filepath.Clean(abs)
	if runtime.GOOS == "windows" && isUNCPath(cur) {
		return "", ErrRestrictedSystemPath
	}

	const maxLinkHops = 40
	for hop := 0; hop < maxLinkHops; hop++ {
		next, changed, _, sErr := stepResolveFirstReparse(cur)
		if sErr == nil {
			if changed {
				cur = next
				if runtime.GOOS == "windows" && isUNCPath(cur) {
					return "", ErrRestrictedSystemPath
				}
				continue
			}
			if eval, eErr := filepath.EvalSymlinks(cur); eErr == nil {
				cleanEval := filepath.Clean(eval)
				if cleanEval == cur {
					return cleanEval, nil
				}
				cur = cleanEval
				continue
			}
			return cur, nil
		}
		if errors.Is(sErr, ErrRestrictedSystemPath) {
			return "", sErr
		}
		// Leaf or intermediate segment after any already-resolved reparse hops does not exist:
		// walk up ancestors of the current rewritten path cur to canonicalize the existing prefix.
		break
	}

	var suffix []string
	walk := cur
	for {
		parent := filepath.Dir(walk)
		if parent == walk {
			break
		}
		suffix = append(suffix, filepath.Base(walk))
		walk = parent
		if realCur, cErr := CanonicalizeExistingPath(walk); cErr == nil {
			parts := make([]string, 0, 1+len(suffix))
			parts = append(parts, realCur)
			for i := len(suffix) - 1; i >= 0; i-- {
				parts = append(parts, suffix[i])
			}
			return filepath.Clean(filepath.Join(parts...)), nil
		}
	}
	return cur, nil
}

// IsWithinRoot checks whether canonical target resides at or inside canonical root using path component boundaries.
func IsWithinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func isWithinRoot(root, target string) bool {
	return IsWithinRoot(root, target)
}

func hasParentTraversal(cleanRel string) bool {
	return cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) || filepath.IsAbs(cleanRel)
}

// NewPathValidatorWithMapping creates a PathValidator with allowed roots and optional host-to-container path translation.
func NewPathValidatorWithMapping(allowedRoots []string, hostSourceRoot, containerSourceRoot string) *PathValidator {
	var cleanRoots []string
	for _, root := range allowedRoots {
		if canon, err := CanonicalizeWithAncestor(root); err == nil {
			cleanRoots = append(cleanRoots, canon)
		}
	}

	cleanHost := strings.TrimSpace(hostSourceRoot)
	cleanContainer := strings.TrimSpace(containerSourceRoot)

	if cleanContainer != "" {
		if canonContainer, err := CanonicalizeWithAncestor(cleanContainer); err == nil {
			cleanContainer = canonContainer
		}
	}

	return &PathValidator{
		allowedRoots:        cleanRoots,
		hostSourceRoot:      cleanHost,
		containerSourceRoot: cleanContainer,
		daemonSourceRoot:    cleanHost,
	}
}

// TranslateHostToContainer translates a host or daemon path to its container-mapped path if roots are configured.
// If mapping is not configured or the path does not match hostSourceRoot/daemonSourceRoot, returns candidatePath.
func (v *PathValidator) TranslateHostToContainer(candidatePath string) (string, error) {
	trimmed := strings.TrimSpace(candidatePath)
	if trimmed == "" {
		return "", ErrPathNotExist
	}

	if v == nil {
		return trimmed, nil
	}

	v.mu.RLock()
	hostRoot := v.hostSourceRoot
	containerRoot := v.containerSourceRoot
	daemonRoot := v.daemonSourceRoot
	v.mu.RUnlock()

	if containerRoot == "" {
		return trimmed, nil
	}

	normCandidate := normalizePathForPrefix(trimmed)

	// Check if path is already inside containerSourceRoot
	normContainerRoot := normalizePathForPrefix(containerRoot)
	if strings.EqualFold(normCandidate, normContainerRoot) || strings.HasPrefix(strings.ToLower(normCandidate), strings.ToLower(normContainerRoot+"/")) {
		return trimmed, nil
	}

	// 1. Check against hostRoot
	if hostRoot != "" {
		normHostRoot := normalizePathForPrefix(hostRoot)
		if strings.EqualFold(normCandidate, normHostRoot) {
			return containerRoot, nil
		}

		prefixWithSep := normHostRoot
		if !strings.HasSuffix(prefixWithSep, "/") {
			prefixWithSep += "/"
		}

		if strings.HasPrefix(strings.ToLower(normCandidate), strings.ToLower(prefixWithSep)) {
			rel := normCandidate[len(prefixWithSep):]
			cleanRel := filepath.Clean(filepath.FromSlash(rel))
			if hasParentTraversal(cleanRel) {
				return "", ErrPathNotAllowed
			}
			return filepath.Join(containerRoot, cleanRel), nil
		}
	}

	// 2. Check against daemonRoot
	if daemonRoot != "" && daemonRoot != hostRoot {
		normDaemonRoot := normalizePathForPrefix(daemonRoot)
		if strings.EqualFold(normCandidate, normDaemonRoot) {
			return containerRoot, nil
		}

		prefixWithSep := normDaemonRoot
		if !strings.HasSuffix(prefixWithSep, "/") {
			prefixWithSep += "/"
		}

		if strings.HasPrefix(strings.ToLower(normCandidate), strings.ToLower(prefixWithSep)) {
			rel := normCandidate[len(prefixWithSep):]
			cleanRel := filepath.Clean(filepath.FromSlash(rel))
			if hasParentTraversal(cleanRel) {
				return "", ErrPathNotAllowed
			}
			return filepath.Join(containerRoot, cleanRel), nil
		}
	}

	// Fail closed: if containerRoot is configured, unmapped host/daemon paths must be rejected
	if hostRoot == "" {
		return "", fmt.Errorf("%w: host path %q cannot be mapped to container source root %q because FORGELAB_HOST_SOURCE_ROOT is not configured. Set FORGELAB_HOST_SOURCE_ROOT to the host directory containing your projects and mount it to %s in docker-compose.yml", ErrPathNotAllowed, candidatePath, containerRoot, containerRoot)
	}
	return "", fmt.Errorf("%w: host path %q cannot be mapped to container source root %q because it is outside configured FORGELAB_HOST_SOURCE_ROOT %q. Ensure FORGELAB_HOST_SOURCE_ROOT contains the project directory and is mounted to %s in docker-compose.yml", ErrPathNotAllowed, candidatePath, containerRoot, hostRoot, containerRoot)
}

// HostSourceRoot returns the configured host source root directory.
func (v *PathValidator) HostSourceRoot() string {
	if v == nil {
		return ""
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.hostSourceRoot
}

// ContainerSourceRoot returns the configured container source root directory.
func (v *PathValidator) ContainerSourceRoot() string {
	if v == nil {
		return ""
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.containerSourceRoot
}

// DaemonSourceRoot returns the configured Docker daemon source root directory.
func (v *PathValidator) DaemonSourceRoot() string {
	if v == nil {
		return ""
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.daemonSourceRoot != "" {
		return v.daemonSourceRoot
	}
	return v.hostSourceRoot
}

// SetHostSourceRoot dynamically updates the configured host source root directory.
func (v *PathValidator) SetHostSourceRoot(root string) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.hostSourceRoot = strings.TrimSpace(root)
}

// SetDaemonSourceRoot dynamically updates the configured Docker daemon source root directory.
func (v *PathValidator) SetDaemonSourceRoot(root string) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.daemonSourceRoot = strings.TrimSpace(root)
}

// AllowedRoots returns a copy of configured allowed source roots.
func (v *PathValidator) AllowedRoots() []string {
	if v == nil {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return append([]string(nil), v.allowedRoots...)
}

// TranslateContainerToHost translates a container path to its canonical host/agent path.
func (v *PathValidator) TranslateContainerToHost(candidatePath string) (string, error) {
	trimmed := strings.TrimSpace(candidatePath)
	if trimmed == "" {
		return "", ErrPathNotExist
	}

	if v == nil {
		return trimmed, nil
	}

	v.mu.RLock()
	hostRoot := v.hostSourceRoot
	containerRoot := v.containerSourceRoot
	v.mu.RUnlock()

	if hostRoot == "" || containerRoot == "" {
		return trimmed, nil
	}

	normContainerRoot := normalizePathForPrefix(containerRoot)
	normCandidate := normalizePathForPrefix(trimmed)

	if strings.EqualFold(normCandidate, normContainerRoot) {
		return hostRoot, nil
	}

	prefixWithSep := normContainerRoot
	if !strings.HasSuffix(prefixWithSep, "/") {
		prefixWithSep += "/"
	}

	if strings.HasPrefix(strings.ToLower(normCandidate), strings.ToLower(prefixWithSep)) {
		rel := normCandidate[len(prefixWithSep):]
		cleanRel := filepath.Clean(filepath.FromSlash(rel))
		if hasParentTraversal(cleanRel) {
			return "", ErrPathNotAllowed
		}
		// Join with hostRoot preserving host format
		return joinHostPath(hostRoot, cleanRel), nil
	}

	// Fail closed: if container mapping is configured, candidate must reside inside containerRoot
	return "", fmt.Errorf("%w: container path %q cannot be mapped outside container source root %q (FORGELAB_HOST_SOURCE_ROOT is %q). Ensure the directory resides within the configured source root", ErrPathNotAllowed, candidatePath, containerRoot, hostRoot)
}

// TranslateContainerToDaemon translates a container path to its Docker daemon mount source path.
func (v *PathValidator) TranslateContainerToDaemon(candidatePath string) (string, error) {
	trimmed := strings.TrimSpace(candidatePath)
	if trimmed == "" {
		return "", ErrPathNotExist
	}

	if v == nil {
		return trimmed, nil
	}

	v.mu.RLock()
	hostRoot := v.hostSourceRoot
	containerRoot := v.containerSourceRoot
	daemonRoot := v.daemonSourceRoot
	if daemonRoot == "" {
		daemonRoot = hostRoot
	}
	v.mu.RUnlock()

	if daemonRoot == "" || containerRoot == "" {
		return trimmed, nil
	}

	normContainerRoot := normalizePathForPrefix(containerRoot)
	normCandidate := normalizePathForPrefix(trimmed)

	if strings.EqualFold(normCandidate, normContainerRoot) {
		return daemonRoot, nil
	}

	prefixWithSep := normContainerRoot
	if !strings.HasSuffix(prefixWithSep, "/") {
		prefixWithSep += "/"
	}

	if strings.HasPrefix(strings.ToLower(normCandidate), strings.ToLower(prefixWithSep)) {
		rel := normCandidate[len(prefixWithSep):]
		cleanRel := filepath.Clean(filepath.FromSlash(rel))
		if hasParentTraversal(cleanRel) {
			return "", ErrPathNotAllowed
		}
		// Join with daemonRoot preserving host format
		return joinHostPath(daemonRoot, cleanRel), nil
	}

	// Fail closed: if container mapping is configured, candidate must reside inside containerRoot
	return "", fmt.Errorf("%w: container path %q cannot be mapped outside container source root %q (daemon root is %q). Ensure the directory resides within the configured source root", ErrPathNotAllowed, candidatePath, containerRoot, daemonRoot)
}

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
	if runtime.GOOS == "windows" && isUNCPath(strings.TrimSpace(candidatePath)) {
		return "", ErrRestrictedSystemPath
	}

	// 1. Translate host path to container path if configured
	resolvedPath, err := v.TranslateHostToContainer(candidatePath)
	if err != nil {
		return "", err
	}

	// 2. Canonicalize by evaluating symlinks, NTFS junctions/reparse points, and 8.3 short paths
	canonicalPath, err := CanonicalizeExistingPath(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrPathNotExist
		}
		if errors.Is(err, ErrRestrictedSystemPath) {
			return "", ErrRestrictedSystemPath
		}
		return "", fmt.Errorf("failed to evaluate symlinks: %w", err)
	}

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

	// 4. Block dangerous system directories and drive roots
	if runtime.GOOS == "windows" {
		lower := strings.ToLower(canonicalPath)
		if isUNCPath(canonicalPath) || (len(lower) <= 3 && strings.HasSuffix(lower, `:\`)) {
			return "", ErrRestrictedSystemPath
		}
	} else if canonicalPath == "/" {
		return "", ErrRestrictedSystemPath
	}
	systemDirs := []string{
		"/etc", "/var", "/usr", "/sys", "/proc", "/dev", "/boot", "/bin", "/sbin", "/root",
		`C:\Windows`, `C:\Program Files`, `C:\Program Files (x86)`, `C:\System Volume Information`,
		`C:\Recovery`, `C:\$Recycle.Bin`,
	}
	for _, sysDir := range systemDirs {
		cleanSysDir := filepath.Clean(sysDir)
		if strings.EqualFold(canonicalPath, cleanSysDir) || strings.HasPrefix(strings.ToLower(canonicalPath), strings.ToLower(cleanSysDir)+string(filepath.Separator)) {
			return "", ErrRestrictedSystemPath
		}
	}

	// 5. Check containerSourceRoot boundary if mapping is enabled
	if v.containerSourceRoot != "" {
		if !isWithinRoot(v.containerSourceRoot, canonicalPath) {
			return "", fmt.Errorf("%w: source path escapes container source root boundary %s", ErrPathNotAllowed, v.containerSourceRoot)
		}
	}

	// 6. If allowedRoots are specified, check boundary
	if len(v.allowedRoots) > 0 {
		allowed := false
		for _, root := range v.allowedRoots {
			if isWithinRoot(root, canonicalPath) {
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
	canonicalCtx, err := CanonicalizeExistingPath(targetCtx)
	if err != nil {
		return "", "", fmt.Errorf("build context does not exist: %w", err)
	}

	// Ensure build context is within sourcePath
	if !isWithinRoot(canonicalSource, canonicalCtx) {
		return "", "", errors.New("build context escapes repository path boundary")
	}

	if dockerfilePath == "" {
		dockerfilePath = "Dockerfile"
	}

	// Resolve Dockerfile relative to canonicalCtx
	targetDockerfile := filepath.Join(canonicalCtx, dockerfilePath)
	canonicalDockerfile, err := CanonicalizeExistingPath(targetDockerfile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", ErrDockerfileNotFound
		}
		return "", "", fmt.Errorf("dockerfile path error: %w", err)
	}

	// Ensure Dockerfile is within canonicalCtx
	if !isWithinRoot(canonicalCtx, canonicalDockerfile) {
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
	cleanRoot, err := CanonicalizeExistingPath(canonicalSourceRoot)
	if err != nil {
		return "", "", fmt.Errorf("source root does not exist: %w", err)
	}

	// 2. Validate serviceSourcePath within cleanRoot
	cleanSourcePath := strings.TrimSpace(serviceSourcePath)
	if cleanSourcePath == "" {
		cleanSourcePath = "."
	}

	// Reject absolute paths that attempt to reference outside cleanRoot
	if filepath.IsAbs(cleanSourcePath) {
		absSource, err := CanonicalizeExistingPath(filepath.Clean(cleanSourcePath))
		if err != nil {
			return "", "", fmt.Errorf("service source path does not exist: %w", err)
		}
		if !isWithinRoot(cleanRoot, absSource) {
			return "", "", errors.New("service source path is an absolute path outside repository boundary")
		}
		rel, err := filepath.Rel(cleanRoot, absSource)
		if err != nil {
			return "", "", errors.New("service source path is an absolute path outside repository boundary")
		}
		cleanSourcePath = rel
	}

	targetServiceDir := filepath.Join(cleanRoot, cleanSourcePath)
	canonicalServiceDir, err := CanonicalizeExistingPath(targetServiceDir)
	if err != nil {
		return "", "", fmt.Errorf("service source path does not exist: %w", err)
	}

	// Ensure canonicalServiceDir is within cleanRoot
	if !isWithinRoot(cleanRoot, canonicalServiceDir) {
		return "", "", errors.New("service source path escapes repository boundary")
	}

	// 3. Validate buildContext
	cleanBuildContext := strings.TrimSpace(buildContext)
	if cleanBuildContext == "" {
		cleanBuildContext = "."
	}

	var targetContextDir string
	if filepath.IsAbs(cleanBuildContext) {
		absCtx, err := CanonicalizeExistingPath(filepath.Clean(cleanBuildContext))
		if err != nil {
			return "", "", fmt.Errorf("build context path does not exist: %w", err)
		}
		if !isWithinRoot(cleanRoot, absCtx) {
			return "", "", errors.New("build context is an absolute path outside repository boundary")
		}
		targetContextDir = absCtx
	} else {
		cleanRel := filepath.Clean(filepath.FromSlash(cleanBuildContext))
		if hasParentTraversal(cleanRel) {
			return "", "", errors.New("build context escapes repository boundary")
		}

		if cleanRel == "." || cleanRel == "" {
			targetContextDir = canonicalServiceDir
		} else {
			// Resolve relative to service dir first, fallback to source root
			candSvc := filepath.Join(canonicalServiceDir, cleanRel)
			candRoot := filepath.Join(cleanRoot, cleanRel)
			if fi, err := os.Stat(candSvc); err == nil && fi.IsDir() {
				targetContextDir = candSvc
			} else if fi, err := os.Stat(candRoot); err == nil && fi.IsDir() {
				targetContextDir = candRoot
			} else {
				return "", "", fmt.Errorf("build context directory does not exist: %s", cleanBuildContext)
			}
		}
	}

	canonicalContextDir, err := CanonicalizeExistingPath(targetContextDir)
	if err != nil {
		return "", "", fmt.Errorf("build context directory does not exist: %w", err)
	}

	// Ensure canonicalContextDir does not escape cleanRoot
	if !isWithinRoot(cleanRoot, canonicalContextDir) {
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
		absDF, err := CanonicalizeExistingPath(filepath.Clean(cleanDockerfilePath))
		if err != nil {
			return "", "", ErrDockerfileNotFound
		}
		if !isWithinRoot(canonicalContextDir, absDF) {
			return "", "", errors.New("dockerfile escapes build context boundary")
		}
		rel, err := filepath.Rel(canonicalContextDir, absDF)
		if err != nil {
			return "", "", errors.New("dockerfile escapes build context boundary")
		}
		cleanDockerfilePath = filepath.ToSlash(rel)
	}

	targetDF := filepath.Join(canonicalContextDir, cleanDockerfilePath)
	canonicalDF, err := CanonicalizeExistingPath(targetDF)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", ErrDockerfileNotFound
		}
		return "", "", fmt.Errorf("dockerfile path error: %w", err)
	}

	// Ensure canonicalDF is inside canonicalContextDir
	if !isWithinRoot(canonicalContextDir, canonicalDF) {
		return "", "", errors.New("dockerfile escapes build context boundary")
	}
	relDF, err := filepath.Rel(canonicalContextDir, canonicalDF)
	if err != nil {
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
