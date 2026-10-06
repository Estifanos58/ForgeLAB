package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/forgelab/backend/internal/envparser"
	"github.com/forgelab/backend/internal/models"
)

var composeFilenames = []string{
	"compose.yaml",
	"compose.yml",
	"docker-compose.yaml",
	"docker-compose.yml",
}

// FindComposeFile checks if any standard Compose file exists in the directory.
func FindComposeFile(dir string) (string, bool) {
	for _, fn := range composeFilenames {
		p := filepath.Join(dir, fn)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, true
		}
	}
	return "", false
}

// RawComposeData holds the top-level structure of a docker-compose / compose file
type RawComposeData struct {
	Version  string                       `yaml:"version"`
	Services map[string]RawComposeService `yaml:"services"`
	Networks map[string]RawComposeNetwork `yaml:"networks"`
	Volumes  map[string]interface{}       `yaml:"volumes"`
}

type RawComposeNetwork struct {
	Driver   string `yaml:"driver"`
	Internal bool   `yaml:"internal"`
}

type RawComposeService struct {
	Image       string                 `yaml:"image"`
	Build       interface{}            `yaml:"build"` // string or map
	Ports       []interface{}          `yaml:"ports"` // strings or ints or maps
	Expose      []interface{}          `yaml:"expose"`
	Environment interface{}            `yaml:"environment"` // map[string]interface{} or []interface{}
	EnvFile     interface{}            `yaml:"env_file"`    // string or []interface{}
	DependsOn   interface{}            `yaml:"depends_on"`  // []interface{} or map[string]interface{}
	Volumes     []interface{}          `yaml:"volumes"`
	Networks    interface{}            `yaml:"networks"`
	Command     interface{}            `yaml:"command"`
	HealthCheck *RawComposeHealthCheck `yaml:"healthcheck"`
	Restart     string                 `yaml:"restart"`
}

type RawComposeHealthCheck struct {
	Test        interface{} `yaml:"test"` // string or []interface{}
	Interval    string      `yaml:"interval"`
	Timeout     string      `yaml:"timeout"`
	Retries     int         `yaml:"retries"`
	StartPeriod string      `yaml:"start_period"`
	Disable     bool        `yaml:"disable"`
}

// ParseComposeFile parses a compose file into DiscoveredServices and DiscoveredTopology
func ParseComposeFile(filePath, repoRoot string) (*DiscoveredTopology, []DiscoveredService, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read compose file: %w", err)
	}

	var raw RawComposeData
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("failed to parse compose YAML: %w", err)
	}

	if len(raw.Services) == 0 {
		return nil, nil, fmt.Errorf("compose file contains no services")
	}

	relComposePath, _ := filepath.Rel(repoRoot, filePath)
	if relComposePath == "" {
		relComposePath = filepath.Base(filePath)
	}

	// 1. Extract networks
	var networks []NetworkConfig
	for netName, netCfg := range raw.Networks {
		networks = append(networks, NetworkConfig{
			Name:     netName,
			Driver:   netCfg.Driver,
			Internal: netCfg.Internal,
		})
	}
	if len(networks) == 0 {
		networks = append(networks, NetworkConfig{
			Name:   "default",
			Driver: "bridge",
		})
	}

	// 2. Extract declared named volumes
	var declaredVolumes []string
	for volName := range raw.Volumes {
		declaredVolumes = append(declaredVolumes, volName)
	}
	sort.Strings(declaredVolumes)

	// 3. Extract and classify services
	var services []DiscoveredService
	declaredVolMap := make(map[string]bool)
	for _, v := range declaredVolumes {
		declaredVolMap[v] = true
	}

	// Deterministic service ordering
	svcNames := make([]string, 0, len(raw.Services))
	for name := range raw.Services {
		svcNames = append(svcNames, name)
	}
	sort.Strings(svcNames)

	composeDir := filepath.Dir(filePath)
	for _, name := range svcNames {
		rawSvc := raw.Services[name]
		svc := parseComposeService(name, rawSvc, repoRoot, composeDir, declaredVolMap)
		services = append(services, svc)
	}

	topology := &DiscoveredTopology{
		Type:            TopologyCompose,
		ComposeFilePath: filepath.ToSlash(relComposePath),
		Networks:        networks,
		Volumes:         declaredVolumes,
		EnvFiles:        discoverEnvFiles(repoRoot),
	}

	return topology, services, nil
}

func parseComposeService(name string, raw RawComposeService, repoRoot, composeDir string, declaredVolumes map[string]bool) DiscoveredService {
	classification := ClassifyService(name, raw.Image, raw.Command, raw.Restart)
	role := inferComposeRole(name, classification, raw)

	buildStrategy := StrategyAuto
	dockerfilePath := ""
	buildContext := "."
	sourcePath := "."
	var buildCmd, startCmd string

	if raw.Build != nil {
		dockerfilePath = "Dockerfile"
		switch b := raw.Build.(type) {
		case string:
			buildContext = b
			sourcePath = b
			buildStrategy = models.BuildStrategyDockerfile
		case map[string]interface{}:
			if ctxVal, ok := b["context"].(string); ok && ctxVal != "" {
				buildContext = ctxVal
				sourcePath = ctxVal
			}
			if dfVal, ok := b["dockerfile"].(string); ok && dfVal != "" {
				dockerfilePath = dfVal
			}
			buildStrategy = models.BuildStrategyDockerfile
		}
	} else if raw.Image != "" {
		buildStrategy = "image"
		dockerfilePath = ""
	}

	// Parse ports
	internalPort, hostPort, publicExposed := parsePorts(raw.Ports, raw.Expose, classification, role)

	// Parse healthcheck
	healthCheck := parseHealthCheck(raw.HealthCheck, internalPort)

	// Parse depends_on
	dependsOn, depConditions := parseDependsOn(raw.DependsOn)

	// Parse volumes
	volumes := parseVolumes(raw.Volumes, declaredVolumes)

	// Parse networks
	networks := parseNetworks(raw.Networks)

	// Parse environment & env_files (explicit environment overrides env_file)
	explicitEnvVars := parseEnvironment(raw.Environment, name)
	envFileVars := parseEnvFiles(raw.EnvFile, repoRoot, composeDir, name)
	envVars := mergeEnvironment(envFileVars, explicitEnvVars)

	// Commands
	if raw.Command != nil {
		switch c := raw.Command.(type) {
		case string:
			startCmd = c
		case []interface{}:
			var parts []string
			for _, p := range c {
				parts = append(parts, fmt.Sprintf("%v", p))
			}
			startCmd = strings.Join(parts, " ")
		}
	}

	// Resource configuration
	resConfig := models.DefaultResourceConfig()
	if classification == ClassificationInfrastructure {
		resConfig.MemoryMB = 512
		resConfig.CpuMillicores = 1000
	}

	// Build candidates
	var candidates []models.BuildCandidate
	if buildStrategy == "image" {
		candidates = append(candidates, models.BuildCandidate{
			ID:              "image",
			Strategy:        "image",
			Name:            fmt.Sprintf("Docker Image: %s", raw.Image),
			Description:     fmt.Sprintf("Run pre-built image %s directly", raw.Image),
			Confidence:      1.0,
			SuggestedPort:   internalPort,
			HealthStrategy:  healthCheck.Strategy,
			HealthCheckPath: healthCheck.Path,
		})
	} else {
		candidates = append(candidates, models.BuildCandidate{
			ID:              "dockerfile",
			Strategy:        models.BuildStrategyDockerfile,
			Name:            "Compose Build Context",
			Description:     fmt.Sprintf("Build using %s in %s", dockerfilePath, buildContext),
			Confidence:      0.95,
			DockerfilePath:  dockerfilePath,
			SuggestedPort:   internalPort,
			HealthStrategy:  healthCheck.Strategy,
			HealthCheckPath: healthCheck.Path,
		})
	}

	healthEnabled := healthCheck.Strategy != models.HealthStrategyNone && healthCheck.Strategy != "none"

	return DiscoveredService{
		Name:                name,
		Role:                role,
		Classification:      classification,
		SourcePath:          sourcePath,
		Runtime:             "compose",
		RuntimeType:         inferRuntimeType(name, raw.Image, classification),
		Framework:           inferFramework(name, raw.Image, classification),
		PackageManager:      "",
		BuildStrategy:       buildStrategy,
		Image:               raw.Image,
		BuildCandidates:     candidates,
		BuildCommand:        buildCmd,
		StartCommand:        startCmd,
		DockerfilePath:      dockerfilePath,
		BuildContext:        buildContext,
		InternalPort:        internalPort,
		HostPort:            hostPort,
		PublicExposed:       publicExposed,
		HealthCheck:         healthCheck,
		HealthStrategy:      healthCheck.Strategy,
		HealthCheckPath:     healthCheck.Path,
		HealthCheckEnabled:  healthEnabled,
		DependsOn:           dependsOn,
		DependsOnConditions: depConditions,
		Volumes:             volumes,
		Networks:            networks,
		Environment:         envVars,
		ResourceConfig:      resConfig,
	}
}

// ClassifyService identifies whether a service is application, worker, infrastructure, or job.
func ClassifyService(name, image string, command interface{}, restart string) string {
	lowerName := strings.ToLower(name)
	lowerImage := strings.ToLower(image)
	cmdStr := fmt.Sprintf("%v", command)
	lowerCmd := strings.ToLower(cmdStr)

	// 1. Infrastructure checks
	infraKeywords := []string{
		"postgres", "mysql", "mariadb", "redis", "mongodb", "mongo", "memcached",
		"rabbitmq", "elasticsearch", "opensearch", "minio", "vault", "kafka",
		"zookeeper", "etcd", "consul", "cockroach", "clickhouse", "influxdb",
		"nats", "localstack", "jaeger", "prometheus", "grafana", "mailhog", "smtp",
	}
	for _, kw := range infraKeywords {
		if strings.Contains(lowerImage, kw) || lowerName == kw || strings.HasPrefix(lowerName, kw+"-") || strings.HasSuffix(lowerName, "-"+kw) {
			return ClassificationInfrastructure
		}
	}
	if lowerName == "db" || lowerName == "database" || lowerName == "cache" || lowerName == "mq" || lowerName == "queue" {
		return ClassificationInfrastructure
	}

	// 2. Job checks (one-off tasks, database migrations, seeds)
	if strings.Contains(lowerName, "migrate") || strings.Contains(lowerName, "migration") ||
		strings.Contains(lowerName, "seed") || strings.Contains(lowerName, "setup") ||
		strings.EqualFold(restart, "no") || strings.EqualFold(restart, "never") {
		return ClassificationJob
	}

	// 3. Worker checks
	if strings.Contains(lowerName, "worker") || strings.Contains(lowerName, "celery") ||
		strings.Contains(lowerName, "consumer") || strings.Contains(lowerName, "sidekiq") ||
		strings.Contains(lowerName, "cron") || strings.Contains(lowerCmd, "worker") ||
		strings.Contains(lowerCmd, "celery") || strings.Contains(lowerCmd, "sidekiq") {
		return ClassificationWorker
	}

	// Default
	return ClassificationApplication
}

func inferComposeRole(name string, classification string, raw RawComposeService) string {
	if classification == ClassificationInfrastructure {
		return models.RoleOther
	}
	if classification == ClassificationWorker {
		return models.RoleWorker
	}

	lower := strings.ToLower(name)
	if strings.Contains(lower, "frontend") || strings.Contains(lower, "client") ||
		strings.Contains(lower, "web") || strings.Contains(lower, "ui") {
		return models.RoleFrontend
	}
	if strings.Contains(lower, "backend") || strings.Contains(lower, "api") ||
		strings.Contains(lower, "server") || strings.Contains(lower, "service") {
		return models.RoleBackend
	}

	return models.RoleOther
}

func inferRuntimeType(name, image, classification string) string {
	if classification == ClassificationInfrastructure {
		for _, kw := range []string{"postgres", "mysql", "redis", "mongo", "rabbitmq", "minio"} {
			if strings.Contains(strings.ToLower(image), kw) || strings.Contains(strings.ToLower(name), kw) {
				return kw
			}
		}
		return "infrastructure"
	}
	return "generic"
}

func inferFramework(name, image, classification string) string {
	if classification == ClassificationInfrastructure {
		return "Infrastructure (" + image + ")"
	}
	return "Docker Compose Service"
}

var portMappingRegex = regexp.MustCompile(`^(\d+):(\d+)`)

func parsePorts(ports []interface{}, expose []interface{}, classification, role string) (internalPort int, hostPort *int, publicExposed bool) {
	internalPort = 8080

	for _, p := range ports {
		switch v := p.(type) {
		case string:
			if match := portMappingRegex.FindStringSubmatch(strings.TrimSpace(v)); len(match) == 3 {
				hp, _ := strconv.Atoi(match[1])
				ip, _ := strconv.Atoi(match[2])
				if ip > 0 {
					internalPort = ip
				}
				if hp > 0 {
					hostPort = &hp
					publicExposed = true
				}
				return
			}
			if num, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && num > 0 {
				internalPort = num
			}
		case int:
			internalPort = v
		}
	}

	for _, e := range expose {
		switch v := e.(type) {
		case string:
			if num, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && num > 0 {
				internalPort = num
			}
		case int:
			internalPort = v
		}
	}

	// By default, frontends or public applications are exposed; infrastructure and workers are internal
	if classification == ClassificationApplication && (role == models.RoleFrontend || strings.Contains(role, "web")) {
		publicExposed = true
	} else if classification == ClassificationInfrastructure || classification == ClassificationWorker || classification == ClassificationJob {
		publicExposed = false
	}

	return
}

func parseHealthCheck(hc *RawComposeHealthCheck, internalPort int) HealthCheckConfig {
	if hc == nil {
		return HealthCheckConfig{
			Strategy: models.HealthStrategyAuto,
			Path:     "/",
			Port:     internalPort,
		}
	}

	if hc.Disable {
		return HealthCheckConfig{
			Strategy: models.HealthStrategyNone,
			Path:     "/",
			Port:     internalPort,
			Test:     []string{"NONE"},
		}
	}

	var testCmd []string
	switch t := hc.Test.(type) {
	case string:
		testCmd = []string{"CMD-SHELL", t}
	case []interface{}:
		for _, item := range t {
			testCmd = append(testCmd, fmt.Sprintf("%v", item))
		}
	}

	interval := parseDurationSeconds(hc.Interval, 10)
	timeout := parseDurationSeconds(hc.Timeout, 5)
	startPeriod := parseDurationSeconds(hc.StartPeriod, 0)
	retries := hc.Retries
	if retries <= 0 {
		retries = 3
	}

	strat := "docker"
	if len(testCmd) == 0 {
		strat = models.HealthStrategyAuto
	} else if len(testCmd) == 1 && strings.EqualFold(testCmd[0], "none") {
		strat = models.HealthStrategyNone
	}

	return HealthCheckConfig{
		Strategy:           strat,
		Path:               "/",
		Port:               internalPort,
		Test:               testCmd,
		IntervalSeconds:    interval,
		TimeoutSeconds:     timeout,
		Retries:            retries,
		StartPeriodSeconds: startPeriod,
	}
}

func parseDurationSeconds(s string, defaultSec int) int {
	if s == "" {
		return defaultSec
	}
	d, err := time.ParseDuration(s)
	if err == nil && d > 0 {
		return int(d.Seconds())
	}
	return defaultSec
}

func parseDependsOn(dep interface{}) ([]string, map[string]string) {
	var result []string
	conditions := make(map[string]string)
	if dep == nil {
		return result, conditions
	}

	switch d := dep.(type) {
	case []interface{}:
		for _, item := range d {
			if s, ok := item.(string); ok && s != "" {
				result = append(result, s)
				conditions[s] = "service_started"
			}
		}
	case map[string]interface{}:
		for k, v := range d {
			result = append(result, k)
			cond := "service_started"
			if m, ok := v.(map[string]interface{}); ok {
				if c, ok := m["condition"].(string); ok && c != "" {
					cond = c
				}
			}
			conditions[k] = cond
		}
	}
	sort.Strings(result)
	return result, conditions
}

func parseVolumes(vols []interface{}, declaredVolumes map[string]bool) []VolumeMountConfig {
	var mounts []VolumeMountConfig
	for _, v := range vols {
		switch item := v.(type) {
		case string:
			parts := strings.Split(item, ":")
			if len(parts) >= 2 {
				source := parts[0]
				target := parts[1]
				readOnly := false
				if len(parts) >= 3 && strings.Contains(parts[2], "ro") {
					readOnly = true
				}
				volType := "bind"
				if declaredVolumes[source] || (!strings.HasPrefix(source, ".") && !strings.HasPrefix(source, "/") && !strings.Contains(source, "/") && !strings.Contains(source, "\\")) {
					volType = "volume"
				}
				mounts = append(mounts, VolumeMountConfig{
					Source:   source,
					Target:   target,
					Type:     volType,
					ReadOnly: readOnly,
				})
			} else if len(parts) == 1 && parts[0] != "" {
				mounts = append(mounts, VolumeMountConfig{
					Source:   parts[0],
					Target:   parts[0],
					Type:     "volume",
					ReadOnly: false,
				})
			}
		case map[string]interface{}:
			vType, _ := item["type"].(string)
			source, _ := item["source"].(string)
			target, _ := item["target"].(string)
			readOnly := false
			if ro, ok := item["read_only"].(bool); ok {
				readOnly = ro
			}
			if vType == "" {
				if declaredVolumes[source] || (!strings.HasPrefix(source, ".") && !strings.HasPrefix(source, "/") && !strings.Contains(source, "/") && !strings.Contains(source, "\\")) {
					vType = "volume"
				} else {
					vType = "bind"
				}
			}
			if target != "" {
				mounts = append(mounts, VolumeMountConfig{
					Source:   source,
					Target:   target,
					Type:     vType,
					ReadOnly: readOnly,
				})
			}
		}
	}
	return mounts
}

func parseNetworks(nets interface{}) []string {
	var result []string
	if nets == nil {
		return result
	}
	switch n := nets.(type) {
	case string:
		if strings.TrimSpace(n) != "" {
			result = append(result, strings.TrimSpace(n))
		}
	case []interface{}:
		for _, item := range n {
			switch it := item.(type) {
			case string:
				if strings.TrimSpace(it) != "" {
					result = append(result, strings.TrimSpace(it))
				}
			case map[string]interface{}:
				for k := range it {
					if strings.TrimSpace(k) != "" {
						result = append(result, strings.TrimSpace(k))
					}
				}
			}
		}
	case map[string]interface{}:
		for k := range n {
			if strings.TrimSpace(k) != "" {
				result = append(result, strings.TrimSpace(k))
			}
		}
	}
	sort.Strings(result)
	return result
}

func parseEnvironment(env interface{}, serviceName string) []EnvironmentProvenance {
	var result []EnvironmentProvenance
	if env == nil {
		return result
	}

	switch e := env.(type) {
	case map[string]interface{}:
		for k, v := range e {
			valStr := fmt.Sprintf("%v", v)
			isSecret := IsSecretEnvKey(k)
			val := valStr
			if isSecret {
				val = "[REDACTED]"
			}
			result = append(result, EnvironmentProvenance{
				Key:                k,
				Value:              val,
				IsSecret:           isSecret,
				Scope:              models.EnvScopeRuntime,
				SourceFile:         "compose.yaml",
				ServiceName:        serviceName,
				HasConflict:        false,
				ConflictResolution: "imported",
				ActiveValue:        "source",
			})
		}
	case []interface{}:
		for _, item := range e {
			str := fmt.Sprintf("%v", item)
			parts := strings.SplitN(str, "=", 2)
			key := parts[0]
			val := ""
			if len(parts) > 1 {
				val = parts[1]
			}
			isSecret := IsSecretEnvKey(key)
			if isSecret {
				val = "[REDACTED]"
			}
			result = append(result, EnvironmentProvenance{
				Key:                key,
				Value:              val,
				IsSecret:           isSecret,
				Scope:              models.EnvScopeRuntime,
				SourceFile:         "compose.yaml",
				ServiceName:        serviceName,
				HasConflict:        false,
				ConflictResolution: "imported",
				ActiveValue:        "source",
			})
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Key < result[j].Key
	})
	return result
}

func parseEnvFiles(envFile interface{}, repoRoot, composeDir, serviceName string) []EnvironmentProvenance {
	var fileList []string
	switch ef := envFile.(type) {
	case string:
		if strings.TrimSpace(ef) != "" {
			fileList = append(fileList, strings.TrimSpace(ef))
		}
	case []interface{}:
		for _, item := range ef {
			switch it := item.(type) {
			case string:
				if strings.TrimSpace(it) != "" {
					fileList = append(fileList, strings.TrimSpace(it))
				}
			case map[string]interface{}:
				if p, ok := it["path"].(string); ok && strings.TrimSpace(p) != "" {
					fileList = append(fileList, strings.TrimSpace(p))
				}
			}
		}
	}

	var results []EnvironmentProvenance
	for _, rawPath := range fileList {
		candidates := []string{
			filepath.Join(composeDir, rawPath),
			filepath.Join(repoRoot, rawPath),
		}
		var targetPath string
		for _, cand := range candidates {
			if info, err := os.Stat(cand); err == nil && !info.IsDir() {
				targetPath = cand
				break
			}
		}
		if targetPath == "" {
			continue
		}

		relPath, _ := filepath.Rel(repoRoot, targetPath)
		if relPath == "" {
			relPath = rawPath
		}
		relPath = filepath.ToSlash(relPath)

		parsed, err := envparser.ParseFile(targetPath)
		if err != nil {
			continue
		}

		keys := make([]string, 0, len(parsed))
		for k := range parsed {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			v := parsed[k]
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
				SourceFile:         relPath,
				ServiceName:        serviceName,
				HasConflict:        false,
				ConflictResolution: "imported",
				ActiveValue:        "source",
			})
		}
	}
	return results
}

func mergeEnvironment(envFileVars, explicitEnvVars []EnvironmentProvenance) []EnvironmentProvenance {
	envMap := make(map[string]EnvironmentProvenance)
	for _, ef := range envFileVars {
		envMap[ef.Key] = ef
	}
	for _, ex := range explicitEnvVars {
		envMap[ex.Key] = ex
	}

	keys := make([]string, 0, len(envMap))
	for k := range envMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	res := make([]EnvironmentProvenance, 0, len(keys))
	for _, k := range keys {
		res = append(res, envMap[k])
	}
	return res
}

// IsSecretEnvKey checks if an environment variable key indicates sensitive data.
func IsSecretEnvKey(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	if upper == "" {
		return false
	}
	secretKeywords := []string{
		"PASSWORD", "PASSWD", "SECRET", "TOKEN", "API_KEY", "APIKEY",
		"PRIVATE_KEY", "PRIVKEY", "ACCESS_KEY", "AUTH_KEY", "DATABASE_URL",
		"DB_PASS", "DB_PASSWORD", "CREDENTIAL", "CREDENTIALS", "CERTIFICATE",
		"SIGNING_KEY", "ENCRYPTION_KEY", "BEARER", "SECRET_KEY", "CLIENT_SECRET",
	}
	for _, kw := range secretKeywords {
		if strings.Contains(upper, kw) {
			return true
		}
	}
	return false
}

func discoverEnvFiles(repoRoot string) []string {
	var envFiles []string
	candidates := []string{".env", ".env.local", ".env.production", ".env.example"}
	for _, c := range candidates {
		p := filepath.Join(repoRoot, c)
		if _, err := os.Stat(p); err == nil {
			envFiles = append(envFiles, c)
		}
	}
	return envFiles
}

// BuildDAG creates a dependency map and partitions services into ordered execution tiers.
// Tier 0 has 0 dependencies, Tier 1 depends only on Tier 0, etc.
// Returns an error if a cycle is detected.
func BuildDAG(services []DiscoveredService) (map[string][]string, [][]string, error) {
	depMap := make(map[string][]string)
	serviceSet := make(map[string]bool)
	for _, s := range services {
		serviceSet[s.Name] = true
		depMap[s.Name] = s.DependsOn
	}

	inDegree := make(map[string]int)
	adj := make(map[string][]string)

	for _, s := range services {
		validDeps := make([]string, 0, len(s.DependsOn))
		for _, dep := range s.DependsOn {
			// Only consider dependencies that are actually among the declared services
			if serviceSet[dep] && dep != s.Name {
				validDeps = append(validDeps, dep)
				adj[dep] = append(adj[dep], s.Name)
			}
		}
		depMap[s.Name] = validDeps
		inDegree[s.Name] = len(validDeps)
	}

	// Kahn's algorithm with tiering
	var tiers [][]string
	remaining := len(services)

	for remaining > 0 {
		var currentTier []string
		for name, deg := range inDegree {
			if deg == 0 {
				currentTier = append(currentTier, name)
			}
		}

		if len(currentTier) == 0 {
			// Cycle detected
			return depMap, nil, fmt.Errorf("cyclic service dependency detected in Compose definition")
		}

		sort.Strings(currentTier)
		tiers = append(tiers, currentTier)

		for _, name := range currentTier {
			delete(inDegree, name)
			remaining--
			for _, neighbor := range adj[name] {
				if _, ok := inDegree[neighbor]; ok {
					inDegree[neighbor]--
				}
			}
		}
	}

	return depMap, tiers, nil
}
