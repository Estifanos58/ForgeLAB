package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/forgelab/backend/internal/security"
)

var (
	ErrPathNotExist           = errors.New("directory does not exist")
	ErrPathNotDirectory       = errors.New("path is not a directory")
	ErrPathOutsideAllowedRoot = errors.New("path is outside allowed root directories")
	ErrRestrictedSystemPath   = errors.New("access to system root directory is restricted")
	ErrSymlinkEscape          = errors.New("symlink escapes parent directory")
)

var restrictedWindowsPrefixes = []string{
	`c:\windows`,
	`c:\program files`,
	`c:\program files (x86)`,
	`c:\recovery`,
	`c:\$recycle.bin`,
	`c:\system volume information`,
}

var restrictedUnixPrefixes = []string{
	"/etc",
	"/bin",
	"/sbin",
	"/usr",
	"/lib",
	"/lib64",
	"/sys",
	"/proc",
	"/dev",
	"/boot",
	"/root",
}

// PathValidator verifies that a given filesystem path is a safe directory for inspection and deployment
type PathValidator struct {
	allowedRoots []string
}

func canonicalizeWithAncestor(rawPath string) (string, error) {
	return security.CanonicalizeWithAncestor(rawPath)
}

func isWithinRoot(root, target string) bool {
	return security.IsWithinRoot(root, target)
}

func NewPathValidator(allowedRoots []string) *PathValidator {
	var cleanRoots []string
	for _, r := range allowedRoots {
		if canon, err := security.CanonicalizeWithAncestor(r); err == nil {
			cleanRoots = append(cleanRoots, canon)
		}
	}
	return &PathValidator{allowedRoots: cleanRoots}
}

// ValidateSourcePath verifies the path exists, is a directory, does not escape, and is not a restricted system directory
func (v *PathValidator) ValidateSourcePath(rawPath string) (string, error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return "", errors.New("path is required")
	}

	if runtime.GOOS == "windows" && (strings.HasPrefix(trimmed, `\\`) || strings.HasPrefix(trimmed, `//`)) {
		return "", ErrRestrictedSystemPath
	}

	absPath, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid path format: %w", err)
	}

	cleanPath := filepath.Clean(absPath)

	// Check if path exists
	info, err := os.Stat(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrPathNotExist
		}
		return "", fmt.Errorf("failed to access directory: %w", err)
	}

	if !info.IsDir() {
		return "", ErrPathNotDirectory
	}

	// Resolve symlinks, Windows NTFS junctions/reparse points, and 8.3 short paths (e.g. RUNNER~1)
	realClean, err := security.CanonicalizeExistingPath(cleanPath)
	if err != nil {
		if errors.Is(err, security.ErrRestrictedSystemPath) {
			return "", ErrRestrictedSystemPath
		}
		return "", fmt.Errorf("failed to resolve path symlinks: %w", err)
	}

	// Reject system roots
	lower := strings.ToLower(realClean)
	if runtime.GOOS == "windows" {
		if strings.HasPrefix(realClean, `\\`) {
			return "", ErrRestrictedSystemPath
		}
		// Reject drive roots like C:\ or D:\
		if len(lower) <= 3 && strings.HasSuffix(lower, ":\\") {
			return "", ErrRestrictedSystemPath
		}
		for _, prefix := range restrictedWindowsPrefixes {
			if lower == prefix || strings.HasPrefix(lower, prefix+`\`) {
				return "", ErrRestrictedSystemPath
			}
		}
	} else {
		// Unix
		if realClean == "/" {
			return "", ErrRestrictedSystemPath
		}
		for _, prefix := range restrictedUnixPrefixes {
			if realClean == prefix || strings.HasPrefix(realClean, prefix+"/") {
				return "", ErrRestrictedSystemPath
			}
		}
	}

	// If explicit allowed roots are configured, enforce them
	if len(v.allowedRoots) > 0 {
		allowed := false
		for _, root := range v.allowedRoots {
			if isWithinRoot(root, realClean) {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", ErrPathOutsideAllowedRoot
		}
	}

	return realClean, nil
}
