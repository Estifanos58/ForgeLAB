package analyzer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/forgelab/backend/internal/models"
)

// ServiceDefinition represents a discovered service and its detected build candidates.
type ServiceDefinition struct {
	Name               string                  `json:"name"`
	Role               string                  `json:"role"` // "frontend", "backend", "worker", "other"
	SourcePath         string                  `json:"source_path"`
	Runtime            string                  `json:"runtime"`
	RuntimeType        string                  `json:"runtime_type"`
	Framework          string                  `json:"framework"`
	PackageManager     string                  `json:"package_manager"`
	BuildStrategy      string                  `json:"build_strategy"`
	BuildCandidates    []models.BuildCandidate `json:"build_candidates"`
	BuildCommand       string                  `json:"build_command"`
	StartCommand       string                  `json:"start_command"`
	DockerfilePath     string                  `json:"dockerfile_path"`
	BuildContext       string                  `json:"build_context"`
	InternalPort       int                     `json:"internal_port"`
	HealthStrategy     string                  `json:"health_strategy"`
	HealthCheckPath    string                  `json:"health_check_path"`
	HealthCheckEnabled bool                    `json:"health_check_enabled"`
	FilesCount         int                     `json:"files_count"`
	TotalBytes         int64                   `json:"total_bytes"`
}

// AnalysisResult represents the overall repository inspection result.
type AnalysisResult struct {
	RepositoryName string              `json:"repository_name"`
	TotalFiles     int                 `json:"total_files"`
	TotalBytes     int64               `json:"total_bytes"`
	Services       []ServiceDefinition `json:"services"`
}

var exposeRegex = regexp.MustCompile(`(?i)^\s*EXPOSE\s+(\d+)`)

// Pruned directory names that must never be traversed
var prunedDirNames = map[string]bool{
	".git":         true,
	"node_modules": true,
	".next":        true,
	"dist":         true,
	"build":        true,
	"target":       true,
	".venv":        true,
	"venv":         true,
	"__pycache__":  true,
	"vendor":       true,
	".turbo":       true,
	".cache":       true,
	"coverage":     true,
	".idea":        true,
	".vscode":      true,
	"bin":          true,
	"obj":          true,
	"tmp":          true,
	".tmp":         true,
}

// IsPrunedDir returns true if a directory should be skipped during walk
func IsPrunedDir(name string) bool {
	return prunedDirNames[strings.ToLower(name)]
}

// IsSecretFile returns true if a file contains sensitive data and should never be read or exposed
func IsSecretFile(name string) bool {
	lower := strings.ToLower(name)
	if lower == ".env" || strings.HasPrefix(lower, ".env.") || strings.HasSuffix(lower, ".pem") ||
		strings.HasSuffix(lower, ".key") || strings.HasSuffix(lower, ".crt") ||
		strings.Contains(lower, "id_rsa") || strings.Contains(lower, "secret") {
		return true
	}
	return false
}

// ProgressCallback defines progress reporting for repository analysis.
type ProgressCallback func(phase string, filesScanned int, totalFiles int, detectedCount int)

// AnalyzeRepository recursively inspects the repository root, discovers services,
// detects technologies using evidence, and produces ranked build candidates.
func AnalyzeRepository(repoRoot string) (*AnalysisResult, error) {
	return AnalyzeRepositoryWithProgress(repoRoot, nil)
}

// AnalyzeRepositoryWithProgress recursively inspects the repository root with progress reporting.
func AnalyzeRepositoryWithProgress(repoRoot string, onProgress ProgressCallback) (*AnalysisResult, error) {
	cleanRoot := filepath.Clean(repoRoot)
	info, err := os.Stat(cleanRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to access repository path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("repository path is not a directory")
	}

	repoName := filepath.Base(cleanRoot)
	if repoName == "" || repoName == "/" || repoName == "." || repoName == "\\" {
		repoName = "project"
	}

	if onProgress != nil {
		onProgress("scanning", 0, 0, 0)
	}

	totalFiles := 0
	var totalBytes int64

	// Quick stats scan respecting prune rules
	_ = filepath.WalkDir(cleanRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if IsPrunedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !IsSecretFile(d.Name()) {
			totalFiles++
			if info, err := d.Info(); err == nil {
				totalBytes += info.Size()
			}
			if onProgress != nil && totalFiles%50 == 0 {
				onProgress("scanning", totalFiles, 0, 0)
			}
		}
		return nil
	})

	if onProgress != nil {
		onProgress("detecting", totalFiles, totalFiles, 0)
	}

	// 1. Discover potential service root subdirectories
	servicePaths := discoverServicePaths(cleanRoot)

	var services []ServiceDefinition
	for _, relPath := range servicePaths {
		svcDir := filepath.Join(cleanRoot, filepath.FromSlash(relPath))
		svc, err := inspectServiceDirectory(cleanRoot, svcDir, relPath)
		if err == nil && svc != nil {
			services = append(services, *svc)
			if onProgress != nil {
				onProgress("detecting", totalFiles, totalFiles, len(services))
			}
		}
	}

	// 2. If no sub-services were discovered, evaluate the repository root as a single service
	if len(services) == 0 {
		svc, err := inspectServiceDirectory(cleanRoot, cleanRoot, ".")
		if err == nil && svc != nil {
			svc.Name = repoName
			services = append(services, *svc)
		} else {
			// Fallback generic single service
			services = append(services, createGenericService(repoName, "."))
		}
	}

	if onProgress != nil {
		onProgress("ready", totalFiles, totalFiles, len(services))
	}

	return &AnalysisResult{
		RepositoryName: repoName,
		TotalFiles:     totalFiles,
		TotalBytes:     totalBytes,
		Services:       services,
	}, nil
}

// discoverServicePaths identifies subdirectories that represent independent deployable applications
func discoverServicePaths(root string) []string {
	var candidates []string

	// Direct child inspection
	entries, err := os.ReadDir(root)
	if err != nil {
		return candidates
	}

	for _, entry := range entries {
		if !entry.IsDir() || IsPrunedDir(entry.Name()) {
			continue
		}

		name := entry.Name()
		subPath := filepath.Join(root, name)

		// Check if entry itself is an application directory
		if isApplicationDirectory(subPath) {
			candidates = append(candidates, name)
			continue
		}

		// Check common grouping directories like apps/*, services/*, packages/*, src/*
		lower := strings.ToLower(name)
		if lower == "apps" || lower == "services" || lower == "packages" || lower == "src" {
			subEntries, err := os.ReadDir(subPath)
			if err == nil {
				for _, sub := range subEntries {
					if sub.IsDir() && !IsPrunedDir(sub.Name()) {
						nestedPath := filepath.Join(subPath, sub.Name())
						if isApplicationDirectory(nestedPath) {
							candidates = append(candidates, filepath.ToSlash(filepath.Join(name, sub.Name())))
						}
					}
				}
			}
		}
	}

	return candidates
}

// isApplicationDirectory returns true if a directory contains evidence of an application
func isApplicationDirectory(dir string) bool {
	indicators := []string{
		"package.json",
		"go.mod",
		"pom.xml",
		"build.gradle",
		"build.gradle.kts",
		"requirements.txt",
		"pyproject.toml",
		"Pipfile",
		"Cargo.toml",
		"composer.json",
		"Gemfile",
		"Dockerfile",
		"docker/Dockerfile",
	}

	for _, ind := range indicators {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(ind))); err == nil {
			return true
		}
	}

	// Check for .csproj, .fsproj
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				lower := strings.ToLower(e.Name())
				if strings.HasSuffix(lower, ".csproj") || strings.HasSuffix(lower, ".fsproj") {
					return true
				}
			}
		}
	}

	return false
}

// inspectServiceDirectory performs detailed technology detection and build candidate generation for one service
func inspectServiceDirectory(repoRoot, dirPath, relPath string) (*ServiceDefinition, error) {
	// Read indicator files and manifests
	files := make(map[string][]byte)

	var filesCount int
	var totalBytes int64

	_ = filepath.WalkDir(dirPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != dirPath && IsPrunedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		if IsSecretFile(d.Name()) {
			return nil
		}

		filesCount++
		if info, err := d.Info(); err == nil {
			totalBytes += info.Size()
		}

		// Collect manifest and config contents up to depth 2
		relToSvc, _ := filepath.Rel(dirPath, path)
		slashRel := filepath.ToSlash(relToSvc)
		if strings.Count(slashRel, "/") <= 2 && isManifestOrConfigFile(filepath.Base(path)) {
			// Read up to 256KB of manifest file
			if data, err := readSmallFile(path, 256*1024); err == nil {
				files[slashRel] = data
			}
		}
		return nil
	})

	// Role determination
	role := inferRole(relPath, files)

	// Service name
	name := filepath.Base(dirPath)
	if relPath == "." {
		name = filepath.Base(repoRoot)
	}
	name = sanitizeServiceName(name)

	// Technology & Framework Detection
	runtime, framework, pkgManager, suggestedPort, healthPath, healthStrat, buildCmd, startCmd := detectTechnology(files)

	// Dockerfile inspection
	hasDockerfile := false
	dockerfilePath := "Dockerfile"
	for f, content := range files {
		if strings.EqualFold(filepath.Base(f), "dockerfile") {
			hasDockerfile = true
			dockerfilePath = f
			// If expose port found in dockerfile, update suggestedPort
			for _, line := range strings.Split(string(content), "\n") {
				if match := exposeRegex.FindStringSubmatch(strings.TrimSpace(line)); len(match) > 1 {
					if p, err := strconv.Atoi(match[1]); err == nil && p > 0 && p <= 65535 {
						suggestedPort = p
						break
					}
				}
			}
			break
		}
	}

	// Build Candidates Generation
	candidates := generateBuildCandidates(
		runtime,
		framework,
		pkgManager,
		suggestedPort,
		buildCmd,
		startCmd,
		hasDockerfile,
		dockerfilePath,
		healthPath,
		healthStrat,
	)

	// Selected strategy: user preference order, ranked by confidence
	selectedStrategy := "auto"
	if len(candidates) > 0 {
		selectedStrategy = candidates[0].Strategy
		buildCmd = candidates[0].BuildCommand
		startCmd = candidates[0].StartCommand
		if candidates[0].Strategy == "dockerfile" && candidates[0].DockerfilePath != "" {
			dockerfilePath = candidates[0].DockerfilePath
		}
	}

	return &ServiceDefinition{
		Name:               name,
		Role:               role,
		SourcePath:         relPath,
		Runtime:            runtime,
		RuntimeType:        runtime,
		Framework:          framework,
		PackageManager:     pkgManager,
		BuildStrategy:      selectedStrategy,
		BuildCandidates:    candidates,
		BuildCommand:       buildCmd,
		StartCommand:       startCmd,
		DockerfilePath:     dockerfilePath,
		BuildContext:       ".",
		InternalPort:       suggestedPort,
		HealthStrategy:     healthStrat,
		HealthCheckPath:    healthPath,
		HealthCheckEnabled: true,
		FilesCount:         filesCount,
		TotalBytes:         totalBytes,
	}, nil
}

func isManifestOrConfigFile(baseName string) bool {
	lower := strings.ToLower(baseName)
	manifests := []string{
		"package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lockb", "bun.lock",
		"next.config.js", "next.config.mjs", "next.config.ts", "next.config.cjs",
		"vite.config.js", "vite.config.ts", "vite.config.mjs", "vite.config.cjs",
		"nuxt.config.js", "nuxt.config.ts", "angular.json", "nest-cli.json", "tsconfig.json",
		"requirements.txt", "pyproject.toml", "pipfile", "pipfile.lock", "poetry.lock", "uv.lock",
		"go.mod", "go.sum",
		"pom.xml", "build.gradle", "build.gradle.kts", "gradlew",
		"cargo.toml", "cargo.lock",
		"gemfile", "gemfile.lock",
		"composer.json", "composer.lock",
		"dockerfile",
	}

	for _, m := range manifests {
		if lower == m || strings.HasPrefix(lower, "dockerfile") || strings.HasSuffix(lower, ".csproj") {
			return true
		}
	}
	return false
}

func readSmallFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, maxBytes)
	n, err := f.Read(buf)
	if err != nil {
		if n == 0 {
			return []byte{}, nil
		}
	}
	return buf[:n], nil
}

// inferRole determines the role: frontend, backend, worker, or other
func inferRole(relPath string, files map[string][]byte) string {
	lowerRel := strings.ToLower(relPath)

	// Check path cues
	if strings.Contains(lowerRel, "frontend") || strings.Contains(lowerRel, "client") ||
		strings.Contains(lowerRel, "web") || strings.Contains(lowerRel, "ui") {
		return models.RoleFrontend
	}
	if strings.Contains(lowerRel, "backend") || strings.Contains(lowerRel, "server") ||
		strings.Contains(lowerRel, "api") || strings.Contains(lowerRel, "svc") {
		return models.RoleBackend
	}
	if strings.Contains(lowerRel, "worker") || strings.Contains(lowerRel, "job") ||
		strings.Contains(lowerRel, "queue") || strings.Contains(lowerRel, "cron") {
		return models.RoleWorker
	}

	// Check manifest cues
	if pkgRaw, ok := files["package.json"]; ok {
		var pkg struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		_ = json.Unmarshal(pkgRaw, &pkg)
		if hasDep(pkg.Dependencies, "next") || hasDep(pkg.Dependencies, "react") ||
			hasDep(pkg.Dependencies, "vue") || hasDep(pkg.Dependencies, "svelte") ||
			hasDep(pkg.Dependencies, "@angular/core") || hasDep(pkg.DevDependencies, "vite") {
			return models.RoleFrontend
		}
		if hasDep(pkg.Dependencies, "express") || hasDep(pkg.Dependencies, "@nestjs/core") ||
			hasDep(pkg.Dependencies, "fastify") || hasDep(pkg.Dependencies, "koa") {
			return models.RoleBackend
		}
	}

	if _, ok := files["go.mod"]; ok {
		return models.RoleBackend
	}
	if _, ok := files["pom.xml"]; ok {
		return models.RoleBackend
	}
	if hasFileWithExt(files, ".gradle") || hasFileWithExt(files, ".gradle.kts") {
		return models.RoleBackend
	}
	if _, ok := files["Cargo.toml"]; ok {
		return models.RoleBackend
	}
	if _, ok := files["requirements.txt"]; ok || hasFileWithPrefix(files, "requirements") {
		return models.RoleBackend
	}

	return models.RoleOther
}

// detectTechnology parses manifests and lockfiles to determine runtime, framework, package manager, and ports
func detectTechnology(files map[string][]byte) (runtime, framework, pkgManager string, port int, healthPath, healthStrat, buildCmd, startCmd string) {
	// Defaults
	runtime = "generic"
	framework = "Generic"
	pkgManager = ""
	port = 8080
	healthPath = "/health"
	healthStrat = "auto"
	buildCmd = ""
	startCmd = ""

	// 1. Node.js Ecosystem
	if pkgRaw, ok := files["package.json"]; ok {
		runtime = "nodejs"
		framework = "Node.js"
		port = 3000
		healthPath = "/"
		healthStrat = "http"

		// Detect package manager
		if _, ok := files["pnpm-lock.yaml"]; ok {
			pkgManager = "pnpm"
		} else if _, ok := files["yarn.lock"]; ok {
			pkgManager = "yarn"
		} else if _, ok := files["bun.lockb"]; ok || hasKey(files, "bun.lock") {
			pkgManager = "bun"
		} else {
			pkgManager = "npm"
		}

		var pkg struct {
			Scripts         map[string]string `json:"scripts"`
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
			Main            string            `json:"main"`
		}
		_ = json.Unmarshal(pkgRaw, &pkg)

		// Check Frameworks
		allDeps := make(map[string]string)
		for k, v := range pkg.Dependencies {
			allDeps[k] = v
		}
		for k, v := range pkg.DevDependencies {
			allDeps[k] = v
		}

		// Next.js
		if hasDep(allDeps, "next") || hasFileWithPrefix(files, "next.config.") {
			runtime = "nextjs"
			framework = "Next.js"
			port = 3000
			healthPath = "/"
			buildCmd = formatRunCommand(pkgManager, "build")
			startCmd = formatRunCommand(pkgManager, "start")
			return
		}

		// Vite / React / Vue / Svelte
		if hasDep(allDeps, "vite") || hasFileWithPrefix(files, "vite.config.") {
			if hasDep(allDeps, "vue") {
				runtime = "vue-vite"
				framework = "Vue (Vite)"
			} else if hasDep(allDeps, "svelte") {
				runtime = "svelte-vite"
				framework = "Svelte (Vite)"
			} else {
				runtime = "react-vite"
				framework = "React (Vite)"
			}
			port = 3000
			healthPath = "/"
			buildCmd = formatRunCommand(pkgManager, "build")
			startCmd = fmt.Sprintf("npx serve -s dist -l %d", port)
			return
		}

		// Nuxt
		if hasDep(allDeps, "nuxt") || hasFileWithPrefix(files, "nuxt.config.") {
			runtime = "nuxtjs"
			framework = "Nuxt"
			port = 3000
			healthPath = "/"
			buildCmd = formatRunCommand(pkgManager, "build")
			startCmd = formatRunCommand(pkgManager, "start")
			return
		}

		// NestJS
		if hasDep(allDeps, "@nestjs/core") || hasKey(files, "nest-cli.json") {
			runtime = "nestjs"
			framework = "NestJS"
			port = 3000
			healthPath = "/"
			buildCmd = formatRunCommand(pkgManager, "build")
			startCmd = formatRunCommand(pkgManager, "start:prod")
			return
		}

		// Express / Fastify / Koa
		if hasDep(allDeps, "express") {
			runtime = "express"
			framework = "Express"
			port = 3000
		} else if hasDep(allDeps, "fastify") {
			runtime = "fastify"
			framework = "Fastify"
			port = 3000
		}

		// Generic Node.js scripts
		if _, ok := pkg.Scripts["build"]; ok {
			buildCmd = formatRunCommand(pkgManager, "build")
		}
		if _, ok := pkg.Scripts["start"]; ok {
			startCmd = formatRunCommand(pkgManager, "start")
		} else if pkg.Main != "" {
			startCmd = "node " + pkg.Main
		} else {
			startCmd = "node index.js"
		}
		return
	}

	// 2. Python Ecosystem
	hasPyReqs := false
	var pyContent string
	if c, ok := files["requirements.txt"]; ok {
		hasPyReqs = true
		pyContent = string(c)
		pkgManager = "pip"
	} else if c, ok := files["pyproject.toml"]; ok {
		hasPyReqs = true
		pyContent = string(c)
		if hasKey(files, "uv.lock") {
			pkgManager = "uv"
		} else if hasKey(files, "poetry.lock") {
			pkgManager = "poetry"
		} else {
			pkgManager = "pip"
		}
	} else if c, ok := files["Pipfile"]; ok {
		hasPyReqs = true
		pyContent = string(c)
		pkgManager = "pipenv"
	}

	if hasPyReqs || hasKey(files, "main.py") || hasKey(files, "app.py") || hasKey(files, "manage.py") {
		lowerPy := strings.ToLower(pyContent)
		if strings.Contains(lowerPy, "fastapi") || strings.Contains(lowerPy, "uvicorn") {
			runtime = "python-fastapi"
			framework = "FastAPI"
			port = 8000
			healthPath = "/docs"
			healthStrat = "http"
			startCmd = "uvicorn main:app --host 0.0.0.0 --port 8000"
			return
		}
		if strings.Contains(lowerPy, "flask") {
			runtime = "python-flask"
			framework = "Flask"
			port = 5000
			healthPath = "/"
			healthStrat = "http"
			startCmd = "python app.py"
			return
		}
		if strings.Contains(lowerPy, "django") || hasKey(files, "manage.py") {
			runtime = "python-django"
			framework = "Django"
			port = 8000
			healthPath = "/"
			healthStrat = "http"
			startCmd = "python manage.py runserver 0.0.0.0:8000"
			return
		}

		runtime = "python"
		framework = "Python"
		port = 8000
		healthPath = "/"
		healthStrat = "http"
		if hasKey(files, "app.py") {
			startCmd = "python app.py"
		} else {
			startCmd = "python main.py"
		}
		return
	}

	// 3. Go Ecosystem
	if modRaw, ok := files["go.mod"]; ok {
		runtime = "go"
		framework = "Go"
		pkgManager = "go modules"
		port = 8080
		healthPath = "/health"
		healthStrat = "http"
		buildCmd = "go build -o /app/server ."
		startCmd = "/app/server"

		lowerMod := strings.ToLower(string(modRaw))
		if strings.Contains(lowerMod, "gin-gonic/gin") {
			framework = "Go (Gin)"
		} else if strings.Contains(lowerMod, "gofiber/fiber") {
			framework = "Go (Fiber)"
		} else if strings.Contains(lowerMod, "labstack/echo") {
			framework = "Go (Echo)"
		} else if strings.Contains(lowerMod, "go-chi/chi") {
			framework = "Go (Chi)"
		}
		return
	}

	// 4. Java / Spring Boot Ecosystem
	if pomRaw, ok := files["pom.xml"]; ok {
		pkgManager = "maven"
		port = 8080
		healthPath = "/health"
		healthStrat = "http"
		buildCmd = "mvn clean package -DskipTests"
		startCmd = "java -jar /app/app.jar"

		if strings.Contains(string(pomRaw), "spring-boot") {
			runtime = "java"
			framework = "Spring Boot (Maven)"
			healthPath = "/actuator/health"
		} else {
			runtime = "java"
			framework = "Java (Maven)"
		}
		return
	}

	if hasFileWithExt(files, ".gradle") || hasFileWithExt(files, ".gradle.kts") {
		pkgManager = "gradle"
		port = 8080
		healthPath = "/health"
		healthStrat = "http"
		buildCmd = "./gradlew build -x test"
		startCmd = "java -jar /app/app.jar"

		isSpring := false
		for _, content := range files {
			lowerContent := strings.ToLower(string(content))
			if strings.Contains(lowerContent, "spring-boot") || strings.Contains(lowerContent, "springframework.boot") {
				isSpring = true
				break
			}
		}

		if isSpring {
			runtime = "java"
			framework = "Spring Boot (Gradle)"
			healthPath = "/actuator/health"
		} else {
			runtime = "java"
			framework = "Java (Gradle)"
		}
		return
	}

	// 5. Rust Ecosystem
	if _, ok := files["Cargo.toml"]; ok {
		runtime = "rust"
		framework = "Rust"
		pkgManager = "cargo"
		port = 8080
		healthPath = "/"
		healthStrat = "http"
		buildCmd = "cargo build --release"
		startCmd = "./target/release/server"
		return
	}

	// 6. PHP / Laravel
	if _, ok := files["composer.json"]; ok {
		runtime = "php"
		framework = "PHP"
		pkgManager = "composer"
		port = 8000
		healthPath = "/"
		healthStrat = "http"
		if hasKey(files, "artisan") {
			framework = "Laravel"
			startCmd = "php artisan serve --host=0.0.0.0 --port=8000"
		} else {
			startCmd = "php -S 0.0.0.0:8000"
		}
		return
	}

	// 7. Ruby / Rails
	if _, ok := files["Gemfile"]; ok {
		runtime = "ruby"
		framework = "Ruby"
		pkgManager = "bundler"
		port = 3000
		healthPath = "/"
		healthStrat = "http"
		if hasKey(files, "config/routes.rb") {
			framework = "Ruby on Rails"
			startCmd = "bundle exec rails server -b 0.0.0.0 -p 3000"
		} else {
			startCmd = "bundle exec rackup -o 0.0.0.0 -p 3000"
		}
		return
	}

	// 8. .NET / C#
	for f := range files {
		if strings.HasSuffix(strings.ToLower(f), ".csproj") {
			runtime = "dotnet"
			framework = ".NET / ASP.NET"
			pkgManager = "dotnet"
			port = 8080
			healthPath = "/health"
			healthStrat = "http"
			buildCmd = "dotnet publish -c Release -o /app/out"
			startCmd = "dotnet /app/out/app.dll"
			return
		}
	}

	return
}

// generateBuildCandidates produces ranked build options according to evidence
func generateBuildCandidates(
	runtime, framework, pkgManager string,
	port int,
	buildCmd, startCmd string,
	hasDockerfile bool,
	dockerfilePath string,
	healthPath, healthStrat string,
) []models.BuildCandidate {
	var candidates []models.BuildCandidate

	// Candidate 1: Existing Dockerfile (if present)
	if hasDockerfile {
		candidates = append(candidates, models.BuildCandidate{
			ID:              "dockerfile",
			Strategy:        "dockerfile",
			Name:            "Existing Dockerfile",
			Description:     fmt.Sprintf("Build using the repository's %s", dockerfilePath),
			Confidence:      0.95,
			DockerfilePath:  dockerfilePath,
			SuggestedPort:   port,
			HealthCheckPath: healthPath,
			HealthStrategy:  healthStrat,
		})
	}

	// Candidate 2: Generated multi-stage Docker build
	confidence := 0.90
	if framework == "Generic" {
		confidence = 0.50
	}
	desc := fmt.Sprintf("ForgeLAB optimized multi-stage build for %s", framework)
	if pkgManager != "" {
		desc += fmt.Sprintf(" using %s", pkgManager)
	}

	candidates = append(candidates, models.BuildCandidate{
		ID:              "auto",
		Strategy:        "auto",
		Name:            fmt.Sprintf("ForgeLAB %s Build", framework),
		Description:     desc,
		Confidence:      confidence,
		BuildCommand:    buildCmd,
		StartCommand:    startCmd,
		PackageManager:  pkgManager,
		SuggestedPort:   port,
		HealthCheckPath: healthPath,
		HealthStrategy:  healthStrat,
	})

	// Candidate 3: Alternative package managers for Node if applicable
	if strings.Contains(runtime, "node") || runtime == "nextjs" || strings.Contains(runtime, "vite") {
		otherManagers := []string{"npm", "pnpm", "yarn"}
		for _, m := range otherManagers {
			if m != pkgManager {
				candidates = append(candidates, models.BuildCandidate{
					ID:              "auto-" + m,
					Strategy:        "auto",
					Name:            fmt.Sprintf("Build with %s", m),
					Description:     fmt.Sprintf("Build %s using %s package manager", framework, m),
					Confidence:      0.75,
					BuildCommand:    formatRunCommand(m, "build"),
					StartCommand:    formatRunCommand(m, "start"),
					PackageManager:  m,
					SuggestedPort:   port,
					HealthCheckPath: healthPath,
					HealthStrategy:  healthStrat,
				})
			}
		}
	}

	// Candidate 4: Custom Strategy
	candidates = append(candidates, models.BuildCandidate{
		ID:              "custom",
		Strategy:        "custom",
		Name:            "Custom Build",
		Description:     "Specify custom build and startup commands",
		Confidence:      0.40,
		BuildCommand:    buildCmd,
		StartCommand:    startCmd,
		SuggestedPort:   port,
		HealthCheckPath: healthPath,
		HealthStrategy:  healthStrat,
	})

	return candidates
}

func createGenericService(name, relPath string) ServiceDefinition {
	return ServiceDefinition{
		Name:           name,
		Role:           models.RoleOther,
		SourcePath:     relPath,
		Runtime:        "generic",
		RuntimeType:    "generic",
		Framework:      "Generic Application",
		PackageManager: "",
		BuildStrategy:  "auto",
		BuildCandidates: []models.BuildCandidate{
			{
				ID:              "auto",
				Strategy:        "auto",
				Name:            "ForgeLAB Generic Container",
				Description:     "Standard application runtime container",
				Confidence:      0.50,
				SuggestedPort:   8080,
				HealthCheckPath: "/health",
				HealthStrategy:  "auto",
			},
			{
				ID:              "custom",
				Strategy:        "custom",
				Name:            "Custom Build Commands",
				Description:     "Specify custom shell build/start commands",
				Confidence:      0.30,
				SuggestedPort:   8080,
				HealthCheckPath: "/health",
				HealthStrategy:  "auto",
			},
		},
		InternalPort:       8080,
		HealthStrategy:     "auto",
		HealthCheckPath:    "/health",
		HealthCheckEnabled: true,
	}
}

func sanitizeServiceName(name string) string {
	s := strings.ToLower(name)
	reg := regexp.MustCompile(`[^a-z0-9-]`)
	s = reg.ReplaceAllString(s, "-")
	reg2 := regexp.MustCompile(`-+`)
	s = reg2.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "service"
	}
	return s
}

func formatRunCommand(pkgManager, script string) string {
	if pkgManager == "pnpm" {
		return "pnpm run " + script
	}
	if pkgManager == "yarn" {
		return "yarn " + script
	}
	if pkgManager == "bun" {
		return "bun run " + script
	}
	return "npm run " + script
}

func hasDep(deps map[string]string, name string) bool {
	if deps == nil {
		return false
	}
	_, ok := deps[name]
	return ok
}

func hasKey(m map[string][]byte, key string) bool {
	_, ok := m[key]
	return ok
}

func hasFileWithPrefix(files map[string][]byte, prefix string) bool {
	for f := range files {
		base := filepath.Base(f)
		if strings.HasPrefix(strings.ToLower(base), strings.ToLower(prefix)) {
			return true
		}
	}
	return false
}

func hasFileWithExt(files map[string][]byte, ext string) bool {
	for f := range files {
		base := filepath.Base(f)
		if strings.HasSuffix(strings.ToLower(base), strings.ToLower(ext)) {
			return true
		}
	}
	return false
}

// GenerateDockerfile produces an optimized multi-stage Dockerfile based on runtime and package manager
func GenerateDockerfile(runtime string, port int, startCmd, pkgManager string) string {
	if port <= 0 {
		port = 8080
	}

	installCmd := "npm install"
	runBuildCmd := "npm run build"
	runStartCmd := "npm start"

	if pkgManager == "pnpm" {
		installCmd = "corepack enable && pnpm install --frozen-lockfile || pnpm install"
		runBuildCmd = "pnpm run build"
		runStartCmd = "pnpm start"
	} else if pkgManager == "yarn" {
		installCmd = "yarn install"
		runBuildCmd = "yarn build"
		runStartCmd = "yarn start"
	} else if pkgManager == "bun" {
		installCmd = "bun install"
		runBuildCmd = "bun run build"
		runStartCmd = "bun run start"
	}

	switch runtime {
	case "nextjs":
		return fmt.Sprintf(`FROM node:20-alpine AS builder
WORKDIR /app
COPY package*.json pnpm-lock.yaml* yarn.lock* bun.lock* ./
RUN %s
COPY . .
ENV NEXT_TELEMETRY_DISABLED=1
RUN %s

FROM node:20-alpine AS runner
WORKDIR /app
ENV NODE_ENV=production
ENV PORT=%d
COPY --from=builder /app ./
EXPOSE %d
CMD ["sh", "-c", "%s"]
`, installCmd, runBuildCmd, port, port, runStartCmd)

	case "react-vite", "vue-vite", "svelte-vite":
		return fmt.Sprintf(`FROM node:20-alpine AS builder
WORKDIR /app
COPY package*.json pnpm-lock.yaml* yarn.lock* bun.lock* ./
RUN %s
COPY . .
RUN %s

FROM node:20-alpine AS runner
WORKDIR /app
RUN npm install -g serve
COPY --from=builder /app/dist ./dist
ENV PORT=%d
EXPOSE %d
CMD ["serve", "-s", "dist", "-l", "%d"]
`, installCmd, runBuildCmd, port, port, port)

	case "nodejs", "express", "fastify", "nestjs":
		cmd := runStartCmd
		if startCmd != "" {
			cmd = startCmd
		}
		return fmt.Sprintf(`FROM node:20-alpine
WORKDIR /app
COPY package*.json pnpm-lock.yaml* yarn.lock* bun.lock* ./
RUN %s
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["sh", "-c", "%s"]
`, installCmd, port, port, cmd)

	case "python-fastapi":
		return fmt.Sprintf(`FROM python:3.11-slim
WORKDIR /app
COPY requirements*.txt pyproject.toml* Pipfile* ./
RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; elif [ -f pyproject.toml ]; then pip install --no-cache-dir .; fi && \
    pip install --no-cache-dir uvicorn fastapi
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "%d"]
`, port, port, port)

	case "python-flask":
		return fmt.Sprintf(`FROM python:3.11-slim
WORKDIR /app
COPY requirements*.txt pyproject.toml* Pipfile* ./
RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; elif [ -f pyproject.toml ]; then pip install --no-cache-dir .; fi
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["python", "app.py"]
`, port, port)

	case "python-django":
		return fmt.Sprintf(`FROM python:3.11-slim
WORKDIR /app
COPY requirements*.txt pyproject.toml* Pipfile* ./
RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; elif [ -f pyproject.toml ]; then pip install --no-cache-dir .; fi
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["python", "manage.py", "runserver", "0.0.0.0:%d"]
`, port, port, port)

	case "python":
		cmd := "python main.py"
		if startCmd != "" {
			cmd = startCmd
		}
		return fmt.Sprintf(`FROM python:3.11-slim
WORKDIR /app
COPY requirements*.txt pyproject.toml* Pipfile* ./
RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; elif [ -f pyproject.toml ]; then pip install --no-cache-dir .; fi
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["sh", "-c", "%s"]
`, port, port, cmd)

	case "go":
		return fmt.Sprintf(`FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /app/server . || CGO_ENABLED=0 go build -o /app/server ./cmd/... || CGO_ENABLED=0 go build -o /app/server ./...

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/server /app/server
ENV PORT=%d
EXPOSE %d
CMD ["/app/server"]
`, port, port)

	case "java":
		if pkgManager == "gradle" {
			return fmt.Sprintf(`FROM gradle:8.5-jdk17-alpine AS builder
WORKDIR /app
COPY build.gradle* settings.gradle* gradlew* ./
COPY gradle ./gradle
COPY src ./src
RUN if [ -f ./gradlew ]; then chmod +x ./gradlew && ./gradlew build -x test; else gradle build -x test; fi && \
    find build/libs -name "*.jar" ! -name "*-plain.jar" -exec cp {} /app/app.jar \;

FROM eclipse-temurin:17-jre-alpine AS runner
WORKDIR /app
COPY --from=builder /app/app.jar /app/app.jar
ENV PORT=%d
EXPOSE %d
CMD ["java", "-jar", "/app/app.jar"]
`, port, port)
		}
		// Default to Maven for Java
		return fmt.Sprintf(`FROM maven:3.9-eclipse-temurin-17-alpine AS builder
WORKDIR /app
COPY pom.xml ./
RUN mvn dependency:go-offline -B || true
COPY src ./src
RUN mvn clean package -DskipTests && \
    find target -maxdepth 1 -name "*.jar" ! -name "*-sources.jar" ! -name "*-javadoc.jar" -exec cp {} /app/app.jar \;

FROM eclipse-temurin:17-jre-alpine AS runner
WORKDIR /app
COPY --from=builder /app/app.jar /app/app.jar
ENV PORT=%d
EXPOSE %d
CMD ["java", "-jar", "/app/app.jar"]
`, port, port)

	case "rust":
		return fmt.Sprintf(`FROM rust:1.75-alpine AS builder
RUN apk add --no-cache musl-dev
WORKDIR /app
COPY Cargo.toml Cargo.lock* ./
COPY src ./src
RUN cargo build --release && \
    find target/release -maxdepth 1 -type f -perm /111 ! -name "*.d" -exec cp {} /app/server \;

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/server /app/server
ENV PORT=%d
EXPOSE %d
CMD ["/app/server"]
`, port, port)

	case "dotnet":
		return fmt.Sprintf(`FROM mcr.microsoft.com/dotnet/sdk:8.0 AS build
WORKDIR /src
COPY *.csproj ./
RUN dotnet restore
COPY . .
RUN dotnet publish -c Release -o /app/publish

FROM mcr.microsoft.com/dotnet/aspnet:8.0 AS final
WORKDIR /app
COPY --from=build /app/publish .
ENV PORT=%d
EXPOSE %d
CMD ["sh", "-c", "dotnet $(ls *.dll | head -n 1)"]
`, port, port)

	case "php":
		return fmt.Sprintf(`FROM php:8.2-cli-alpine
WORKDIR /app
COPY composer.json composer.lock* ./
RUN if [ -f composer.json ]; then curl -sS https://getcomposer.org/installer | php -- --install-dir=/usr/local/bin --filename=composer && composer install --no-dev; fi
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["sh", "-c", "%s"]
`, port, port, startCmd)

	case "ruby":
		return fmt.Sprintf(`FROM ruby:3.2-alpine
RUN apk add --no-cache build-base
WORKDIR /app
COPY Gemfile Gemfile.lock* ./
RUN bundle install
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["sh", "-c", "%s"]
`, port, port, startCmd)

	default:
		return fmt.Sprintf(`FROM alpine:latest
WORKDIR /app
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["sh", "-c", "echo 'Application started on port %d' && sleep infinity"]
`, port, port, port)
	}
}
