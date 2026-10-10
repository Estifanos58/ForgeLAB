package detector

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/forgelab/backend/internal/discovery"
)

// DetectionResult represents the detected runtime, build system, and recommendations.
type DetectionResult struct {
	Runtime         string   `json:"runtime"`
	Framework       string   `json:"framework"`
	PackageManager  string   `json:"package_manager,omitempty"`
	BuildStrategy   string   `json:"build_strategy"`
	SuggestedPort   int      `json:"suggested_port"`
	BuildCommand    string   `json:"build_command"`
	StartCommand    string   `json:"start_command"`
	HealthCheckPath string   `json:"health_check_path"`
	HealthStrategy  string   `json:"health_strategy"`
	DetectedFiles   []string `json:"detected_files"`
}

var exposeRegex = regexp.MustCompile(`(?i)^\s*EXPOSE\s+(\d+)`)

// Detect inspects a local source directory and determines the application characteristics.
func Detect(sourceDir string) (*DetectionResult, error) {
	files := make(map[string][]byte)

	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read source directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			subEntries, _ := os.ReadDir(filepath.Join(sourceDir, entry.Name()))
			for _, sub := range subEntries {
				if !sub.IsDir() {
					relPath := filepath.Join(entry.Name(), sub.Name())
					if isIndicatorFile(sub.Name()) {
						content, _ := os.ReadFile(filepath.Join(sourceDir, relPath))
						files[relPath] = content
					}
				}
			}
			continue
		}

		name := entry.Name()
		if isIndicatorFile(name) {
			content, _ := os.ReadFile(filepath.Join(sourceDir, name))
			files[name] = content
		}
	}

	return DetectFromFiles(files), nil
}

func isIndicatorFile(name string) bool {
	lower := strings.ToLower(name)
	indicators := []string{
		"dockerfile", "package.json", "pnpm-lock.yaml", "yarn.lock", "package-lock.json",
		"npm-shrinkwrap.json", "bun.lockb", "bun.lock", "cargo.lock", "gemfile.lock",
		"composer.lock", "poetry.lock", "next.config.js", "next.config.mjs", "next.config.ts",
		"vite.config.js", "vite.config.ts", "vite.config.mjs", "requirements.txt",
		"pyproject.toml", "pipfile", "go.mod", "pom.xml", "build.gradle",
		"build.gradle.kts", "cargo.toml", "gemfile", "composer.json", "main.go",
		"main.py", "app.py", "manage.py", "index.html", "artisan",
	}
	for _, ind := range indicators {
		if lower == ind {
			return true
		}
	}
	return false
}

// DetectFromFiles runs detection from an in-memory map of filename -> content.
func DetectFromFiles(files map[string][]byte) *DetectionResult {
	var detectedFiles []string
	for f := range files {
		detectedFiles = append(detectedFiles, f)
	}

	// 1. Dockerfile check
	for f, content := range files {
		if strings.EqualFold(filepath.Base(f), "dockerfile") {
			port := 8080
			for _, line := range strings.Split(string(content), "\n") {
				if match := exposeRegex.FindStringSubmatch(strings.TrimSpace(line)); len(match) > 1 {
					if p, err := strconv.Atoi(match[1]); err == nil && p > 0 && p <= 65535 {
						port = p
						break
					}
				}
			}
			return &DetectionResult{
				Runtime:         "dockerfile",
				Framework:       "Dockerfile",
				BuildStrategy:   "dockerfile",
				SuggestedPort:   port,
				BuildCommand:    "",
				StartCommand:    "",
				HealthCheckPath: "/health",
				HealthStrategy:  "auto",
				DetectedFiles:   detectedFiles,
			}
		}
	}

	runtime, framework, pkgManager, port, healthPath, healthStrat, buildCmd, startCmd := discovery.DetectTechnology(files)

	strat := "auto"
	if runtime == "dockerfile" {
		strat = "dockerfile"
	}

	return &DetectionResult{
		Runtime:         runtime,
		Framework:       framework,
		PackageManager:  pkgManager,
		BuildStrategy:   strat,
		SuggestedPort:   port,
		BuildCommand:    buildCmd,
		StartCommand:    startCmd,
		HealthCheckPath: healthPath,
		HealthStrategy:  healthStrat,
		DetectedFiles:   detectedFiles,
	}
}

// GenerateDockerfile produces an optimized multi-stage Dockerfile string based on the detected runtime and optional package manager.
func GenerateDockerfile(runtime string, port int, startCmd string, pkgManager ...string) string {
	pm := ""
	if len(pkgManager) > 0 {
		pm = pkgManager[0]
	}
	return discovery.GenerateDockerfile(runtime, port, startCmd, pm)
}
