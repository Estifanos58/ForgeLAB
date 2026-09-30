package dockerignore

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// DefaultIgnorePatterns are standard development directories/files to ignore
// when no .dockerignore exists or as baseline exclusions.
var DefaultIgnorePatterns = []string{
	"node_modules",
	".git",
	".next",
	"dist",
	"build",
	".venv",
	"venv",
	"__pycache__",
	".cache",
	".turbo",
	".DS_Store",
	"Thumbs.db",
	"*.log",
	".env.local",
	".env.*.local",
}

// IgnoreRule represents a single parsed rule from .dockerignore.
type IgnoreRule struct {
	Pattern   string
	IsNegated bool
	IsDirOnly bool
}

// DockerignoreMatcher handles filtering files based on .dockerignore semantics.
type DockerignoreMatcher struct {
	rules []IgnoreRule
}

// NewDockerignoreMatcher creates a matcher from a slice of rule patterns.
func NewDockerignoreMatcher(patterns []string) *DockerignoreMatcher {
	var rules []IgnoreRule
	for _, p := range patterns {
		line := strings.TrimSpace(p)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		isNegated := false
		if strings.HasPrefix(line, "!") {
			isNegated = true
			line = strings.TrimPrefix(line, "!")
			line = strings.TrimSpace(line)
		}

		// Normalize path separators to forward slash
		line = filepath.ToSlash(line)
		line = strings.TrimPrefix(line, "/")

		isDirOnly := false
		if strings.HasSuffix(line, "/") {
			isDirOnly = true
			line = strings.TrimSuffix(line, "/")
		}

		if line != "" {
			rules = append(rules, IgnoreRule{
				Pattern:   line,
				IsNegated: isNegated,
				IsDirOnly: isDirOnly,
			})
		}
	}

	return &DockerignoreMatcher{rules: rules}
}

// LoadDockerignore reads a .dockerignore file from rootDir.
// If the file does not exist, returns a matcher with DefaultIgnorePatterns.
func LoadDockerignore(rootDir string) (*DockerignoreMatcher, error) {
	ignorePath := filepath.Join(rootDir, ".dockerignore")
	f, err := os.Open(ignorePath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewDockerignoreMatcher(DefaultIgnorePatterns), nil
		}
		return nil, err
	}
	defer f.Close()

	var patterns []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		patterns = append(patterns, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return NewDockerignoreMatcher(patterns), nil
}

// Matches returns true if relPath should be ignored.
// Evaluates rules sequentially from top to bottom (last matching rule wins).
func (m *DockerignoreMatcher) Matches(relPath string, isDir bool) bool {
	if m == nil || len(m.rules) == 0 {
		return false
	}

	cleanRel := filepath.ToSlash(filepath.Clean(relPath))
	cleanRel = strings.TrimPrefix(cleanRel, "/")
	if cleanRel == "." || cleanRel == "" {
		return false
	}

	ignored := false

	for _, rule := range m.rules {
		if rule.IsDirOnly {
			// A directory-only rule matches the directory itself (when isDir is true)
			// or any file/subfolder inside that directory (even when isDir is false).
			if !isDir && !strings.HasPrefix(cleanRel, rule.Pattern+"/") {
				continue
			}
		}

		if matchPattern(rule.Pattern, cleanRel, isDir) {
			ignored = !rule.IsNegated
		}
	}

	return ignored
}

// CanSkipDir returns true if a directory can be completely skipped (pruned)
// without walking into it. It is safe to prune only if the directory is ignored
// AND there are no subsequent negation rules that could match inside this directory.
func (m *DockerignoreMatcher) CanSkipDir(relDirPath string) bool {
	if !m.Matches(relDirPath, true) {
		return false
	}

	cleanDir := filepath.ToSlash(filepath.Clean(relDirPath))
	cleanDir = strings.TrimPrefix(cleanDir, "/")

	// Check if any subsequent negation rule could match files inside this directory
	for _, rule := range m.rules {
		if rule.IsNegated {
			if strings.HasPrefix(rule.Pattern, cleanDir+"/") || rule.Pattern == cleanDir {
				return false
			}
			// If rule contains wildcards that might match inside, don't skip
			if strings.Contains(rule.Pattern, "**") {
				return false
			}
		}
	}

	return true
}

func matchPattern(pattern, path string, isDir bool) bool {
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)

	// Exact match
	if pattern == path {
		return true
	}

	// Direct directory prefix: pattern matches a directory and path is inside it
	if strings.HasPrefix(path, pattern+"/") {
		return true
	}

	// Wildcard ** handling
	if strings.HasPrefix(pattern, "**/") {
		subPattern := pattern[3:]
		// Match against full path or any tail segment
		if matchPattern(subPattern, path, isDir) {
			return true
		}
		parts := strings.Split(path, "/")
		for i := 1; i < len(parts); i++ {
			subPath := strings.Join(parts[i:], "/")
			if matchPattern(subPattern, subPath, isDir) {
				return true
			}
		}
	}

	if strings.HasSuffix(pattern, "/**") {
		prefix := pattern[:len(pattern)-3]
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}

	// Segment-by-segment filepath.Match
	if matched, _ := filepath.Match(pattern, path); matched {
		return true
	}

	// Match base name for simple wildcard patterns (e.g. *.log or *.tmp)
	if !strings.Contains(pattern, "/") {
		baseName := filepath.Base(path)
		if matched, _ := filepath.Match(pattern, baseName); matched {
			return true
		}
	}

	return false
}
