package discovery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/envparser"
	"github.com/forgelab/backend/internal/models"
)

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

// IsSecretFile returns true if a file contains sensitive data and should never be exposed
func IsSecretFile(name string) bool {
	lower := strings.ToLower(name)
	if lower == ".env" || strings.HasPrefix(lower, ".env.") || strings.HasSuffix(lower, ".pem") ||
		strings.HasSuffix(lower, ".key") || strings.HasSuffix(lower, ".crt") ||
		strings.Contains(lower, "id_rsa") || strings.Contains(lower, "secret") {
		return true
	}
	return false
}

// PlanOptions configures deployment plan generation
type PlanOptions struct {
	SourceID         *uuid.UUID
	SourceRevision   string
	SourceType       string
	StrategyOverride string
	ProjectID        *uuid.UUID
}

// Discover inspects a local repository workspace and produces an authoritative DiscoveryResult.
func Discover(repoRoot string) (*DiscoveryResult, error) {
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

	// 1. Check for Docker Compose definition first
	if composePath, found := FindComposeFile(cleanRoot); found {
		topology, services, err := ParseComposeFile(composePath, cleanRoot)
		if err == nil && len(services) > 0 {
			// For services with build contexts, enrich with technology detection
			for i := range services {
				if services[i].BuildStrategy != "image" && services[i].BuildContext != "" {
					bDir := filepath.Join(cleanRoot, filepath.FromSlash(services[i].BuildContext))
					if files, err := readDirectoryManifests(bDir); err == nil && len(files) > 0 {
						rt, fw, pm, port, hp, hs, bc, sc := DetectTechnology(files)
						if services[i].Runtime == "compose" && rt != "generic" {
							services[i].Runtime = rt
							services[i].RuntimeType = rt
							services[i].Framework = fw
							services[i].PackageManager = pm
						}
						if services[i].InternalPort == 8080 && port != 8080 {
							services[i].InternalPort = port
						}
						if services[i].HealthCheckPath == "/" && hp != "/" {
							services[i].HealthCheckPath = hp
						}
						if services[i].HealthStrategy == models.HealthStrategyAuto && hs != models.HealthStrategyAuto {
							services[i].HealthStrategy = hs
						}
						if services[i].BuildCommand == "" && bc != "" {
							services[i].BuildCommand = bc
						}
						if services[i].StartCommand == "" && sc != "" {
							services[i].StartCommand = sc
						}
					}
				}
			}

			depMap, _, _ := BuildDAG(services)

			totalFiles, totalBytes := quickStatScan(cleanRoot)
			return &DiscoveryResult{
				RepositoryName:  repoName,
				Topology:        *topology,
				PrimaryStrategy: StrategyCompose,
				Services:        services,
				TotalFiles:      totalFiles,
				TotalBytes:      totalBytes,
				Dependencies:    depMap,
			}, nil
		}
	}

	// 2. Discover independent application subdirectories
	servicePaths := discoverServicePaths(cleanRoot)
	var services []DiscoveredService

	for _, relPath := range servicePaths {
		svcDir := filepath.Join(cleanRoot, filepath.FromSlash(relPath))
		svc, err := inspectServiceDirectory(cleanRoot, svcDir, relPath)
		if err == nil && svc != nil {
			services = append(services, *svc)
		}
	}

	topologyType := TopologyMonorepo
	// 3. Fallback: single service at repository root
	if len(services) == 0 {
		topologyType = TopologySingleService
		svc, err := inspectServiceDirectory(cleanRoot, cleanRoot, ".")
		if err == nil && svc != nil {
			svc.Name = repoName
			services = append(services, *svc)
		} else {
			services = append(services, createGenericService(repoName, "."))
		}
	}

	// Determine primary strategy
	primaryStrat := StrategyAuto
	hasDockerfile := false
	for _, s := range services {
		if s.BuildStrategy == models.BuildStrategyDockerfile {
			hasDockerfile = true
			break
		}
	}
	if hasDockerfile {
		primaryStrat = StrategyDockerfiles
	}

	envFiles := discoverEnvFiles(cleanRoot)
	rootEnvVars := scanEnvFileProvenance(cleanRoot, envFiles)

	totalFiles, totalBytes := quickStatScan(cleanRoot)

	depMap := make(map[string][]string)
	for _, s := range services {
		depMap[s.Name] = s.DependsOn
	}

	return &DiscoveryResult{
		RepositoryName: repoName,
		Topology: DiscoveredTopology{
			Type:        topologyType,
			EnvFiles:    envFiles,
			RootEnvVars: rootEnvVars,
		},
		PrimaryStrategy: primaryStrat,
		Services:        services,
		TotalFiles:      totalFiles,
		TotalBytes:      totalBytes,
		Dependencies:    depMap,
	}, nil
}

func quickStatScan(cleanRoot string) (totalFiles int, totalBytes int64) {
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
		}
		return nil
	})
	return
}

func readDirectoryManifests(dirPath string) (map[string][]byte, error) {
	files := make(map[string][]byte)
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

		rel, _ := filepath.Rel(dirPath, path)
		slashRel := filepath.ToSlash(rel)
		if strings.Count(slashRel, "/") <= 2 && isManifestOrConfigFile(filepath.Base(path)) {
			if data, err := readSmallFile(path, 256*1024); err == nil {
				files[slashRel] = data
			}
		}
		return nil
	})
	return files, nil
}

func discoverServicePaths(root string) []string {
	var candidates []string
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

		if isApplicationDirectory(subPath) {
			candidates = append(candidates, name)
			continue
		}

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

func isApplicationDirectory(dir string) bool {
	indicators := []string{
		"package.json", "go.mod", "pom.xml", "build.gradle", "build.gradle.kts",
		"requirements.txt", "pyproject.toml", "Pipfile", "Cargo.toml", "composer.json",
		"Gemfile", "Dockerfile", "docker/Dockerfile", "index.html",
	}

	for _, ind := range indicators {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(ind))); err == nil {
			return true
		}
	}

	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".csproj") {
				return true
			}
		}
	}
	return false
}

func inspectServiceDirectory(repoRoot, dirPath, relPath string) (*DiscoveredService, error) {
	files, err := readDirectoryManifests(dirPath)
	if err != nil {
		return nil, err
	}

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
		if !IsSecretFile(d.Name()) {
			filesCount++
			if info, err := d.Info(); err == nil {
				totalBytes += info.Size()
			}
		}
		return nil
	})

	role := inferRole(relPath, files)
	name := filepath.Base(dirPath)
	if relPath == "." {
		name = filepath.Base(repoRoot)
	}
	name = sanitizeServiceName(name)

	runtime, framework, pkgManager, suggestedPort, healthPath, healthStrat, buildCmd, startCmd := DetectTechnology(files)

	hasDockerfile := false
	dockerfilePath := ""
	for f, content := range files {
		if strings.EqualFold(filepath.Base(f), "dockerfile") {
			hasDockerfile = true
			dockerfilePath = f
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

	candidates := generateBuildCandidates(
		runtime, framework, pkgManager, suggestedPort,
		buildCmd, startCmd, hasDockerfile, dockerfilePath, healthPath, healthStrat,
	)

	selectedStrategy := StrategyAuto
	if len(candidates) > 0 {
		selectedStrategy = candidates[0].Strategy
		buildCmd = candidates[0].BuildCommand
		startCmd = candidates[0].StartCommand
		if candidates[0].Strategy == models.BuildStrategyDockerfile && candidates[0].DockerfilePath != "" {
			dockerfilePath = candidates[0].DockerfilePath
		}
	}
	if !hasDockerfile && selectedStrategy != models.BuildStrategyDockerfile {
		dockerfilePath = ""
	}

	buildContext := relPath
	if buildContext == "" {
		buildContext = "."
	}

	classification := ClassificationApplication
	if role == models.RoleWorker {
		classification = ClassificationWorker
	}

	// Service-specific .env files
	svcEnvFiles := discoverEnvFiles(dirPath)
	svcEnvVars := scanEnvFileProvenance(dirPath, svcEnvFiles)
	for i := range svcEnvVars {
		svcEnvVars[i].ServiceName = name
	}

	healthCheck := HealthCheckConfig{
		Strategy: healthStrat,
		Path:     healthPath,
		Port:     suggestedPort,
	}

	publicExposed := (role == models.RoleFrontend || relPath == "." || strings.Contains(strings.ToLower(name), "frontend") || strings.Contains(strings.ToLower(name), "web"))

	return &DiscoveredService{
		Name:               name,
		Role:               role,
		Classification:     classification,
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
		BuildContext:       buildContext,
		InternalPort:       suggestedPort,
		PublicExposed:      publicExposed,
		HealthCheck:        healthCheck,
		HealthStrategy:     healthStrat,
		HealthCheckPath:    healthPath,
		HealthCheckEnabled: true,
		Environment:        svcEnvVars,
		ResourceConfig:     models.DefaultResourceConfig(),
		FilesCount:         filesCount,
		TotalBytes:         totalBytes,
	}, nil
}

func inferRole(relPath string, files map[string][]byte) string {
	lowerRel := strings.ToLower(relPath)
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

	if hasKey(files, "index.html") && len(files) <= 5 {
		return models.RoleFrontend
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

func generateBuildCandidates(
	runtime, framework, pkgManager string,
	port int,
	buildCmd, startCmd string,
	hasDockerfile bool,
	dockerfilePath string,
	healthPath, healthStrat string,
) []models.BuildCandidate {
	var candidates []models.BuildCandidate

	if hasDockerfile {
		candidates = append(candidates, models.BuildCandidate{
			ID:              "dockerfile",
			Strategy:        models.BuildStrategyDockerfile,
			Name:            "Existing Dockerfile",
			Description:     fmt.Sprintf("Build using repository's %s", dockerfilePath),
			Confidence:      0.95,
			DockerfilePath:  dockerfilePath,
			SuggestedPort:   port,
			HealthCheckPath: healthPath,
			HealthStrategy:  healthStrat,
		})
	}

	confidence := 0.90
	if framework == "Generic Application" {
		confidence = 0.50
	}
	desc := fmt.Sprintf("ForgeLAB optimized multi-stage build for %s", framework)
	if pkgManager != "" {
		desc += fmt.Sprintf(" using %s", pkgManager)
	}

	candidates = append(candidates, models.BuildCandidate{
		ID:              "auto",
		Strategy:        StrategyAuto,
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

	if strings.Contains(runtime, "node") || runtime == "nextjs" || strings.Contains(runtime, "vite") {
		otherManagers := []string{"npm", "pnpm", "yarn"}
		for _, m := range otherManagers {
			if m != pkgManager {
				candidates = append(candidates, models.BuildCandidate{
					ID:              "auto-" + m,
					Strategy:        StrategyAuto,
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

	if rec := RecommendProductionExecution(runtime, framework, pkgManager, port, healthPath, healthStrat); len(rec.Candidates) > 0 {
		candidates = append(rec.Candidates, candidates...)
	}

	return candidates
}

func createGenericService(name, relPath string) DiscoveredService {
	return DiscoveredService{
		Name:           name,
		Role:           models.RoleOther,
		Classification: ClassificationApplication,
		SourcePath:     relPath,
		Runtime:        "generic",
		RuntimeType:    "generic",
		Framework:      "Generic Application",
		BuildStrategy:  StrategyAuto,
		BuildCandidates: []models.BuildCandidate{
			{
				ID:              "auto",
				Strategy:        StrategyAuto,
				Name:            "ForgeLAB Generic Container",
				Description:     "Standard application runtime container",
				Confidence:      0.50,
				SuggestedPort:   8080,
				HealthCheckPath: "/health",
				HealthStrategy:  models.HealthStrategyAuto,
			},
		},
		InternalPort:       8080,
		PublicExposed:      true,
		HealthStrategy:     models.HealthStrategyAuto,
		HealthCheckPath:    "/health",
		HealthCheckEnabled: true,
		ResourceConfig:     models.DefaultResourceConfig(),
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
		"composer.json", "composer.lock", "artisan",
		"dockerfile", "index.html",
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
	if err != nil && n == 0 {
		return []byte{}, nil
	}
	return buf[:n], nil
}

func scanEnvFileProvenance(dir string, envFiles []string) []EnvironmentProvenance {
	var results []EnvironmentProvenance
	for _, ef := range envFiles {
		p := filepath.Join(dir, ef)
		vars, err := envparser.ParseFile(p)
		if err != nil {
			continue
		}
		keys := make([]string, 0, len(vars))
		for k := range vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := vars[k]
			isSecret := IsSecretEnvKey(k)
			val := v
			if isSecret {
				val = "[REDACTED]"
			}
			results = append(results, EnvironmentProvenance{
				Key:                k,
				Value:              val,
				IsSecret:           isSecret,
				Scope:              models.EnvScopeRuntime,
				SourceFile:         ef,
				HasConflict:        false,
				ConflictResolution: "imported",
				ActiveValue:        "source",
			})
		}
	}
	return results
}

// GeneratePlan constructs an immutable DeploymentPlan from a DiscoveryResult
func GeneratePlan(res *DiscoveryResult, opts PlanOptions) (*DeploymentPlan, error) {
	if res == nil {
		return nil, fmt.Errorf("discovery result is nil")
	}

	planID := uuid.New()
	strat := res.PrimaryStrategy
	if opts.StrategyOverride != "" {
		strat = opts.StrategyOverride
	}

	// 1. Build PlannedServices
	plannedServices := make([]PlannedService, 0, len(res.Services))
	for _, s := range res.Services {
		ps := PlannedService{
			Name:                s.Name,
			Role:                s.Role,
			Classification:      s.Classification,
			SourcePath:          s.SourcePath,
			BuildStrategy:       s.BuildStrategy,
			Image:               s.Image,
			DockerfilePath:      s.DockerfilePath,
			BuildContext:        s.BuildContext,
			BuildCommand:        s.BuildCommand,
			StartCommand:        s.StartCommand,
			RuntimeType:         s.RuntimeType,
			Framework:           s.Framework,
			PackageManager:      s.PackageManager,
			InternalPort:        s.InternalPort,
			HostPort:            s.HostPort,
			PublicExposed:       s.PublicExposed,
			HealthCheck:         s.HealthCheck,
			ResourceConfig:      s.ResourceConfig,
			DependsOn:           s.DependsOn,
			DependsOnConditions: s.DependsOnConditions,
			Volumes:             s.Volumes,
			Networks:            s.Networks,
			Environment:         s.Environment,
			BuildCandidates:     s.BuildCandidates,
		}
		plannedServices = append(plannedServices, ps)
	}

	// 2. Compute DAG and execution tiers
	depMap, tiers, err := BuildDAG(res.Services)
	if err != nil {
		// If cycle detected, create linear tiers
		tiers = [][]string{}
		var allNames []string
		for _, s := range res.Services {
			allNames = append(allNames, s.Name)
		}
		tiers = append(tiers, allNames)
	}

	// 3. Compute endpoints
	var publicEndpoints []PlannedEndpoint
	var internalEndpoints []PlannedEndpoint

	for _, s := range plannedServices {
		internalEndpoints = append(internalEndpoints, PlannedEndpoint{
			ServiceName: s.Name,
			Port:        s.InternalPort,
			Type:        "internal",
			Protocol:    "http",
			Address:     fmt.Sprintf("%s:%d", s.Name, s.InternalPort),
		})

		if s.PublicExposed {
			hp := s.InternalPort
			if s.HostPort != nil && *s.HostPort > 0 {
				hp = *s.HostPort
			}
			publicEndpoints = append(publicEndpoints, PlannedEndpoint{
				ServiceName: s.Name,
				Port:        hp,
				Type:        "public",
				Protocol:    "http",
				Address:     fmt.Sprintf("0.0.0.0:%d", hp),
			})
		}
	}

	// 4. Collect networks and named volumes
	netSet := make(map[string]bool)
	var networks []string
	if len(res.Topology.Networks) > 0 {
		for _, n := range res.Topology.Networks {
			if !netSet[n.Name] {
				netSet[n.Name] = true
				networks = append(networks, n.Name)
			}
		}
	}
	for _, s := range plannedServices {
		for _, n := range s.Networks {
			if !netSet[n] {
				netSet[n] = true
				networks = append(networks, n)
			}
		}
	}
	if len(networks) == 0 {
		networks = []string{"default"}
	}

	var volumes []string
	volSet := make(map[string]bool)
	for _, v := range res.Topology.Volumes {
		if !volSet[v] {
			volSet[v] = true
			volumes = append(volumes, v)
		}
	}
	for _, s := range plannedServices {
		for _, v := range s.Volumes {
			if v.Type == "volume" && !volSet[v.Source] {
				volSet[v.Source] = true
				volumes = append(volumes, v.Source)
			}
		}
	}

	// Collect environment provenance
	var allEnv []EnvironmentProvenance
	allEnv = append(allEnv, res.Topology.RootEnvVars...)
	for _, s := range plannedServices {
		allEnv = append(allEnv, s.Environment...)
	}

	// Redact all secret values across plan and discovery to ensure zero plaintext secret leakage
	for i := range allEnv {
		if allEnv[i].IsSecret {
			allEnv[i].Value = "[REDACTED]"
		}
	}
	for i := range plannedServices {
		for j := range plannedServices[i].Environment {
			if plannedServices[i].Environment[j].IsSecret {
				plannedServices[i].Environment[j].Value = "[REDACTED]"
			}
		}
	}
	for i := range res.Topology.RootEnvVars {
		if res.Topology.RootEnvVars[i].IsSecret {
			res.Topology.RootEnvVars[i].Value = "[REDACTED]"
		}
	}
	for i := range res.Services {
		for j := range res.Services[i].Environment {
			if res.Services[i].Environment[j].IsSecret {
				res.Services[i].Environment[j].Value = "[REDACTED]"
			}
		}
	}

	return &DeploymentPlan{
		ID:                planID,
		SourceID:          opts.SourceID,
		SourceRevision:    opts.SourceRevision,
		SourceType:        opts.SourceType,
		Strategy:          strat,
		Topology:          res.Topology.Type,
		Services:          plannedServices,
		Dependencies:      depMap,
		ExecutionTiers:    tiers,
		Networks:          networks,
		Volumes:           volumes,
		Environment:       allEnv,
		PublicEndpoints:   publicEndpoints,
		InternalEndpoints: internalEndpoints,
		CreatedAt:         time.Now(),
	}, nil
}
