package docker

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/security"
)

var validNetworkNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)

// PreflightValidationError represents a failure during deployment preflight inspection.
type PreflightValidationError struct {
	ServiceName string
	Field       string
	Message     string
}

func (e *PreflightValidationError) Error() string {
	if e.ServiceName != "" {
		if e.Field != "" {
			return fmt.Sprintf("service '%s' (%s): %s", e.ServiceName, e.Field, e.Message)
		}
		return fmt.Sprintf("service '%s': %s", e.ServiceName, e.Message)
	}
	return e.Message
}

// PreflightOptions contains parameters for validating a release or service deployment.
type PreflightOptions struct {
	Project             *models.Project
	Deployments         []*models.ServiceDeployment
	ServicesMap         map[string]*models.Service
	PathValidator       *security.PathValidator
	IsServiceDeployment bool
}

// ValidateDeploymentPreflight inspects the entire deployment plan before any build or container action begins.
// It verifies:
// 1. Service name uniqueness
// 2. Dependency existence and absence of cycles
// 3. Build strategy compliance (image vs dockerfile vs auto)
// 4. Dockerfile existence for dockerfile-based builds
// 5. Port validity (1-65535)
// 6. Network name formatting
// 7. Volume syntax and path boundary integrity
func ValidateDeploymentPreflight(ctx context.Context, opts PreflightOptions) error {
	if len(opts.Deployments) == 0 {
		return &PreflightValidationError{Message: "deployment plan contains no services"}
	}

	nameMap := make(map[string]*models.ServiceDeployment, len(opts.Deployments))
	depGraph := make(map[string][]string, len(opts.Deployments))

	// 1. Service uniqueness and index building
	for _, sd := range opts.Deployments {
		lowerName := strings.ToLower(strings.TrimSpace(sd.ServiceName))
		if lowerName == "" {
			return &PreflightValidationError{
				ServiceName: sd.ServiceName,
				Field:       "name",
				Message:     "service name cannot be empty",
			}
		}
		if _, exists := nameMap[lowerName]; exists {
			return &PreflightValidationError{
				ServiceName: sd.ServiceName,
				Field:       "name",
				Message:     fmt.Sprintf("duplicate service name '%s' in deployment plan", sd.ServiceName),
			}
		}
		nameMap[lowerName] = sd
		depGraph[sd.ServiceName] = sd.DependsOn
	}

	// 2. Dependency existence and self-dependency checks
	for _, sd := range opts.Deployments {
		for _, dep := range sd.DependsOn {
			trimmedDep := strings.TrimSpace(dep)
			if trimmedDep == "" {
				continue
			}
			if strings.EqualFold(trimmedDep, sd.ServiceName) {
				return &PreflightValidationError{
					ServiceName: sd.ServiceName,
					Field:       "depends_on",
					Message:     "self-dependency is not allowed",
				}
			}

			if opts.IsServiceDeployment {
				// Independent service deployment: verify that every dependency exists in the project
				inProject := false
				if opts.ServicesMap != nil {
					_, inProject = opts.ServicesMap[trimmedDep]
					if !inProject {
						for sName := range opts.ServicesMap {
							if strings.EqualFold(sName, trimmedDep) {
								inProject = true
								break
							}
						}
					}
				}
				if !inProject {
					return &PreflightValidationError{
						ServiceName: sd.ServiceName,
						Field:       "depends_on",
						Message:     fmt.Sprintf("dependency '%s' does not exist in project", dep),
					}
				}
			} else {
				// Release deployment: verify that every dependency exists in the deployment plan
				_, inPlan := nameMap[strings.ToLower(trimmedDep)]
				if !inPlan {
					return &PreflightValidationError{
						ServiceName: sd.ServiceName,
						Field:       "depends_on",
						Message:     fmt.Sprintf("dependency '%s' does not exist in deployment plan", dep),
					}
				}
			}
		}
	}

	// 3. Dependency cycle detection (DFS 3-color graph) - mandatory for release deployments
	if !opts.IsServiceDeployment {
		if cycle := detectDependencyCycle(depGraph); len(cycle) > 0 {
			return &PreflightValidationError{
				Field:   "depends_on",
				Message: fmt.Sprintf("dependency cycle detected: %s", strings.Join(cycle, " -> ")),
			}
		}
	}

	// 4. Service-specific preflight checks
	for _, sd := range opts.Deployments {
		var svc *models.Service
		if opts.ServicesMap != nil {
			svc = opts.ServicesMap[sd.ServiceName]
		}

		buildStrategy := sd.BuildStrategy
		if buildStrategy == "" && svc != nil {
			buildStrategy = svc.BuildStrategy
		}
		classification := sd.Classification
		if classification == "" && svc != nil {
			classification = svc.Classification
		}

		// A. Image strategy validation: strict requirement for non-empty image name
		if buildStrategy == models.BuildStrategyImage || classification == models.ClassificationInfrastructure {
			img := strings.TrimSpace(sd.Image)
			if img == "" && svc != nil {
				img = strings.TrimSpace(svc.Image)
			}
			if img == "" {
				return &PreflightValidationError{
					ServiceName: sd.ServiceName,
					Field:       "image",
					Message:     "build strategy 'image' requires a valid non-empty image reference",
				}
			}
			if strings.ContainsAny(img, " \t\r\n") {
				return &PreflightValidationError{
					ServiceName: sd.ServiceName,
					Field:       "image",
					Message:     fmt.Sprintf("invalid image reference '%s' (contains whitespace)", img),
				}
			}
		}

		// B. Dockerfile strategy validation: requires real Dockerfile and verifies path
		if buildStrategy == models.BuildStrategyDockerfile {
			dockerfilePath := strings.TrimSpace(sd.DockerfilePath)
			if dockerfilePath == "" && svc != nil {
				dockerfilePath = strings.TrimSpace(svc.DockerfilePath)
			}
			if dockerfilePath == "" {
				dockerfilePath = "Dockerfile"
			}

			// If project root is locally available (not remote agent), verify Dockerfile on disk
			if opts.Project != nil && opts.Project.SourceType != models.SourceTypeLocalAgent && opts.Project.RepositoryPath != "" && opts.PathValidator != nil {
				sourcePath := ""
				buildCtx := sd.BuildContext
				if svc != nil {
					sourcePath = svc.SourcePath
					if buildCtx == "" {
						buildCtx = svc.BuildContext
					}
				}
				_, _, err := opts.PathValidator.ValidateServiceBuildPaths(
					opts.Project.RepositoryPath,
					sourcePath,
					buildCtx,
					dockerfilePath,
					false,
				)
				if err != nil {
					return &PreflightValidationError{
						ServiceName: sd.ServiceName,
						Field:       "dockerfile",
						Message:     fmt.Sprintf("Dockerfile validation failed: %v", err),
					}
				}
			}
		}

		// C. Port validation
		intPort := sd.InternalPort
		if intPort <= 0 && svc != nil {
			intPort = svc.InternalPort
		}
		if intPort < 0 || intPort > 65535 {
			return &PreflightValidationError{
				ServiceName: sd.ServiceName,
				Field:       "internal_port",
				Message:     fmt.Sprintf("port %d out of valid range (1-65535)", intPort),
			}
		}

		// D. Network validation
		var networks []string
		if len(sd.Networks) > 0 {
			networks = sd.Networks
		} else if svc != nil && len(svc.Networks) > 0 {
			networks = svc.Networks
		}
		for _, netName := range networks {
			trimmedNet := strings.TrimSpace(netName)
			if trimmedNet == "" {
				return &PreflightValidationError{
					ServiceName: sd.ServiceName,
					Field:       "networks",
					Message:     "empty network name is not allowed",
				}
			}
			if !validNetworkNameRegex.MatchString(trimmedNet) {
				return &PreflightValidationError{
					ServiceName: sd.ServiceName,
					Field:       "networks",
					Message:     fmt.Sprintf("invalid network name '%s' (only alphanumeric, dashes, and underscores allowed)", trimmedNet),
				}
			}
		}

		// E. Volume validation
		for _, vol := range sd.Volumes {
			if strings.TrimSpace(vol.Target) == "" {
				return &PreflightValidationError{
					ServiceName: sd.ServiceName,
					Field:       "volumes",
					Message:     "volume target path cannot be empty",
				}
			}
			// Target inside container must be an absolute path
			if !strings.HasPrefix(filepath.ToSlash(vol.Target), "/") {
				return &PreflightValidationError{
					ServiceName: sd.ServiceName,
					Field:       "volumes",
					Message:     fmt.Sprintf("container volume target '%s' must be an absolute path starting with '/'", vol.Target),
				}
			}

			// Validate host path for bind mounts
			isDockerSocket := vol.Source == "/var/run/docker.sock" || vol.Source == `\\.\pipe\docker_engine`
			isBind := vol.Type == models.VolumeTypeBind || vol.Type == "bind" ||
				strings.HasPrefix(vol.Source, ".") || strings.HasPrefix(vol.Source, "/") ||
				strings.HasPrefix(vol.Source, "~") || strings.Contains(vol.Source, "/") || strings.Contains(vol.Source, "\\")

			if isBind && !isDockerSocket && opts.Project != nil {
				if opts.Project.SourceType == models.SourceTypeGitHub || opts.Project.SourceType == models.SourceTypeLocalAgent {
					if !filepath.IsAbs(vol.Source) {
						return &PreflightValidationError{
							ServiceName: sd.ServiceName,
							Field:       "volumes",
							Message:     fmt.Sprintf("relative bind mount '%s' is not supported for remote/agent source type '%s'. Use a named volume or absolute host path.", vol.Source, opts.Project.SourceType),
						}
					}
				} else if opts.Project.RepositoryPath != "" && opts.Project.SourceType != models.SourceTypeLocalAgent {
					hostPath := vol.Source
					if !filepath.IsAbs(hostPath) {
						hostPath = filepath.Join(opts.Project.RepositoryPath, hostPath)
					}
					cleanHost := filepath.Clean(hostPath)
					cleanRepo := filepath.Clean(opts.Project.RepositoryPath)
					rel, err := filepath.Rel(cleanRepo, cleanHost)
					if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
						return &PreflightValidationError{
							ServiceName: sd.ServiceName,
							Field:       "volumes",
							Message:     fmt.Sprintf("bind mount source '%s' escapes repository boundary", vol.Source),
						}
					}
				}
			}
		}
	}

	return nil
}

// detectDependencyCycle runs depth-first search to find directed cycles in the dependency graph.
func detectDependencyCycle(graph map[string][]string) []string {
	visited := make(map[string]int) // 0: unvisited, 1: visiting (in stack), 2: visited
	var path []string

	var dfs func(node string) []string
	dfs = func(node string) []string {
		visited[node] = 1
		path = append(path, node)

		for _, neighbor := range graph[node] {
			if _, exists := graph[neighbor]; !exists {
				continue
			}
			if visited[neighbor] == 1 {
				// Cycle detected
				cycleStartIdx := 0
				for idx, n := range path {
					if n == neighbor {
						cycleStartIdx = idx
						break
					}
				}
				cycle := append([]string{}, path[cycleStartIdx:]...)
				cycle = append(cycle, neighbor)
				return cycle
			}
			if visited[neighbor] == 0 {
				if cycle := dfs(neighbor); len(cycle) > 0 {
					return cycle
				}
			}
		}

		visited[node] = 2
		path = path[:len(path)-1]
		return nil
	}

	for node := range graph {
		if visited[node] == 0 {
			if cycle := dfs(node); len(cycle) > 0 {
				return cycle
			}
		}
	}

	return nil
}
