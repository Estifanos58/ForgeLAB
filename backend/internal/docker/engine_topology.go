package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/models"
)

// TopologyServiceNode represents a service node in the project topology DAG.
type TopologyServiceNode struct {
	ID                  uuid.UUID         `json:"id"`
	Name                string            `json:"name"`
	Role                string            `json:"role"`
	Classification      string            `json:"classification"`
	Status              string            `json:"status"`
	InternalHostname    string            `json:"internal_hostname"`
	InternalPort        int               `json:"internal_port"`
	HostPort            *int              `json:"host_port,omitempty"`
	PreviewURL          *string           `json:"preview_url,omitempty"`
	NetworkName         string            `json:"network_name"`
	NetworkAliases      []string          `json:"network_aliases"`
	ContainerID         *string           `json:"container_id,omitempty"`
	HealthStrategy      string            `json:"health_strategy"`
	HealthCheckPath     string            `json:"health_check_path"`
	DependsOn           []string          `json:"depends_on"`
	DependsOnConditions map[string]string `json:"depends_on_conditions,omitempty"`
	Tier                int               `json:"tier"`
}

// TopologyDiagnostic represents an actionable diagnostic for a service or project.
type TopologyDiagnostic struct {
	Severity    string `json:"severity"` // "error", "warning", "info"
	Category    string `json:"category"` // "dns", "network", "port", "env_var", "dependency"
	ServiceName string `json:"service_name,omitempty"`
	Title       string `json:"title"`
	Message     string `json:"message"`
	Action      string `json:"action"`
}

// ProjectTopologyResponse represents the complete service-topology graph and connection diagnostics.
type ProjectTopologyResponse struct {
	ProjectID   uuid.UUID             `json:"project_id"`
	ProjectName string                `json:"project_name"`
	NetworkName string                `json:"network_name"`
	Tiers       [][]string            `json:"tiers"`
	Services    []TopologyServiceNode `json:"services"`
	Diagnostics []TopologyDiagnostic  `json:"diagnostics"`
}

// GetProjectTopology builds an authoritative service-topology representation using
// ForgeLAB's existing discovery and DAG plan architecture.
func (e *Engine) GetProjectTopology(ctx context.Context, projectID uuid.UUID) (*ProjectTopologyResponse, error) {
	project, err := e.getProjectByID(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to get project %s: %w", projectID, err)
	}

	var svcs []*models.Service
	if e.serviceService != nil {
		if sList, err := e.serviceService.ListServices(ctx, projectID); err == nil {
			svcs = sList
		}
	}
	if len(svcs) == 0 && len(project.Services) > 0 {
		svcs = project.Services
	}

	primaryNet := fmt.Sprintf("forgelab-net-%s", project.ID.String())

	// Build DAG nodes
	svcMap := make(map[string]*models.Service, len(svcs))
	var nodes []discovery.ServiceNode
	for _, s := range svcs {
		svcMap[s.Name] = s
		nodes = append(nodes, discovery.ServiceNode{
			Name:                s.Name,
			DependsOn:           s.DependsOn,
			DependsOnConditions: s.DependsOnConditions,
		})
	}

	// Calculate DAG execution tiers
	_, tiers, _ := discovery.ValidateAndBuildDAG(nodes)

	tierMap := make(map[string]int)
	for tIdx, tier := range tiers {
		for _, sName := range tier {
			tierMap[sName] = tIdx + 1
		}
	}

	var topologyServices []TopologyServiceNode
	for _, s := range svcs {
		hcPath := "/"
		if s.HealthCheckPath != nil && *s.HealthCheckPath != "" {
			hcPath = *s.HealthCheckPath
		}
		node := TopologyServiceNode{
			ID:                  s.ID,
			Name:                s.Name,
			Role:                s.Role,
			Classification:      s.Classification,
			Status:              s.Status,
			InternalHostname:    s.Name,
			InternalPort:        s.InternalPort,
			HostPort:            s.HostPort,
			PreviewURL:          s.PreviewURL,
			NetworkName:         primaryNet,
			NetworkAliases:      []string{s.Name},
			ContainerID:         s.ContainerID,
			HealthStrategy:      s.HealthStrategy,
			HealthCheckPath:     hcPath,
			DependsOn:           s.DependsOn,
			DependsOnConditions: s.DependsOnConditions,
			Tier:                tierMap[s.Name],
		}
		if node.Tier == 0 {
			node.Tier = 1
		}
		topologyServices = append(topologyServices, node)
	}

	// Generate actionable connection diagnostics
	var diagnostics []TopologyDiagnostic

	// 1. Check for port conflicts
	usedHostPorts := make(map[int]string)
	for _, s := range svcs {
		if s.HostPort != nil && *s.HostPort > 0 {
			p := *s.HostPort
			if existingSvc, exists := usedHostPorts[p]; exists {
				diagnostics = append(diagnostics, TopologyDiagnostic{
					Severity:    "error",
					Category:    "port",
					ServiceName: s.Name,
					Title:       fmt.Sprintf("Host Port Conflict on Port %d", p),
					Message:     fmt.Sprintf("Services '%s' and '%s' both declare host port %d.", existingSvc, s.Name, p),
					Action:      fmt.Sprintf("Change the host_port for '%s' or '%s' in service settings to eliminate port binding collisions.", s.Name, existingSvc),
				})
			} else {
				usedHostPorts[p] = s.Name
			}
		}
	}

	// 2. Check dependency relationships and readiness
	for _, s := range svcs {
		for _, depName := range s.DependsOn {
			depSvc, exists := svcMap[depName]
			if !exists {
				diagnostics = append(diagnostics, TopologyDiagnostic{
					Severity:    "error",
					Category:    "dns",
					ServiceName: s.Name,
					Title:       fmt.Sprintf("Missing Dependency '%s'", depName),
					Message:     fmt.Sprintf("Service '%s' depends on '%s', but '%s' is not registered in this project.", s.Name, depName, depName),
					Action:      fmt.Sprintf("Remove '%s' from %s's depends_on or add a service named '%s'.", depName, s.Name, depName),
				})
				continue
			}

			// Dependency readiness and state
			if depSvc.Status == models.ServiceStatusFailed {
				diagnostics = append(diagnostics, TopologyDiagnostic{
					Severity:    "error",
					Category:    "dependency",
					ServiceName: s.Name,
					Title:       fmt.Sprintf("Upstream Dependency '%s' Failed", depName),
					Message:     fmt.Sprintf("Service '%s' depends on '%s', but '%s' is in 'failed' status.", s.Name, depName, depName),
					Action:      fmt.Sprintf("Inspect %s deployment logs to resolve its failure before restarting %s.", depName, s.Name),
				})
			} else if depSvc.Status == models.ServiceStatusStopped && s.Status == models.ServiceStatusRunning {
				diagnostics = append(diagnostics, TopologyDiagnostic{
					Severity:    "warning",
					Category:    "dependency",
					ServiceName: s.Name,
					Title:       fmt.Sprintf("Upstream Dependency '%s' Is Stopped", depName),
					Message:     fmt.Sprintf("Service '%s' is running, but its dependency '%s' is stopped.", s.Name, depName),
					Action:      fmt.Sprintf("Start service '%s' to ensure '%s' can reach dependent backend APIs or datastores.", depName, s.Name),
				})
			}
		}
	}

	// 3. Environment variable diagnostics for database dependencies
	for _, s := range svcs {
		var peerEnvMap map[string]string
		if e.secretService != nil {
			if m, _, err := e.secretService.GetDecryptedEnvMap(ctx, project.ID, &s.ID, models.EnvScopeRuntime); err == nil {
				peerEnvMap = m
			}
		}
		if peerEnvMap == nil {
			peerEnvMap = make(map[string]string)
		}

		for _, depName := range s.DependsOn {
			depSvc, exists := svcMap[depName]
			if !exists {
				continue
			}
			depLower := strings.ToLower(depSvc.Name)
			depImage := strings.ToLower(depSvc.Image)
			isDB := strings.Contains(depLower, "postgres") || strings.Contains(depImage, "postgres") ||
				strings.Contains(depLower, "mysql") || strings.Contains(depLower, "mariadb") ||
				strings.Contains(depLower, "mongo")

			if isDB {
				hasConnURL := false
				for k := range peerEnvMap {
					kUpper := strings.ToUpper(k)
					if strings.Contains(kUpper, "DATABASE_URL") || strings.Contains(kUpper, "DB_URL") ||
						strings.Contains(kUpper, "DB_HOST") || strings.Contains(kUpper, strings.ToUpper(depSvc.Name)+"_URL") {
						hasConnURL = true
						break
					}
				}
				if !hasConnURL && s.Role != models.RoleFrontend {
					diagnostics = append(diagnostics, TopologyDiagnostic{
						Severity:    "info",
						Category:    "env_var",
						ServiceName: s.Name,
						Title:       fmt.Sprintf("Database Connection Variable For '%s'", depSvc.Name),
						Message:     fmt.Sprintf("Service '%s' connects to '%s' at internal hostname '%s:%d'.", s.Name, depSvc.Name, depSvc.Name, depSvc.InternalPort),
						Action:      fmt.Sprintf("ForgeLAB provides internal DNS '%s' on port %d; ensure '%s' reads %s_URL or DATABASE_URL.", depSvc.Name, depSvc.InternalPort, s.Name, strings.ToUpper(depSvc.Name)),
					})
				}
			}

			isRedis := strings.Contains(depLower, "redis") || strings.Contains(depImage, "redis") ||
				strings.Contains(depLower, "valkey") || strings.Contains(depImage, "valkey")
			if isRedis {
				hasRedisURL := false
				for k := range peerEnvMap {
					kUpper := strings.ToUpper(k)
					if strings.Contains(kUpper, "REDIS_URL") || strings.Contains(kUpper, "REDIS_HOST") ||
						strings.Contains(kUpper, strings.ToUpper(depSvc.Name)+"_URL") {
						hasRedisURL = true
						break
					}
				}
				if !hasRedisURL && s.Role != models.RoleFrontend {
					diagnostics = append(diagnostics, TopologyDiagnostic{
						Severity:    "info",
						Category:    "env_var",
						ServiceName: s.Name,
						Title:       fmt.Sprintf("Redis Connection Variable For '%s'", depSvc.Name),
						Message:     fmt.Sprintf("Service '%s' connects to Redis '%s' at internal hostname '%s:%d'.", s.Name, depSvc.Name, depSvc.Name, depSvc.InternalPort),
						Action:      fmt.Sprintf("ForgeLAB provides internal DNS '%s' on port %d; ensure '%s' reads REDIS_URL (e.g. redis://%s:%d).", depSvc.Name, depSvc.InternalPort, s.Name, depSvc.Name, depSvc.InternalPort),
					})
				}
			}

			// Check network connectivity: if services declare explicit custom networks, ensure they share at least one
			if len(s.Networks) > 0 && len(depSvc.Networks) > 0 {
				sharedNet := false
				for _, netA := range s.Networks {
					for _, netB := range depSvc.Networks {
						if netA == netB {
							sharedNet = true
							break
						}
					}
					if sharedNet {
						break
					}
				}
				if !sharedNet {
					diagnostics = append(diagnostics, TopologyDiagnostic{
						Severity:    "error",
						Category:    "network",
						ServiceName: s.Name,
						Title:       fmt.Sprintf("Network Isolation Collision with '%s'", depSvc.Name),
						Message:     fmt.Sprintf("Service '%s' depends on '%s', but they do not share any common network (%v vs %v).", s.Name, depSvc.Name, s.Networks, depSvc.Networks),
						Action:      fmt.Sprintf("Add a common network to both '%s' and '%s' to allow internal DNS and TCP communication.", s.Name, depSvc.Name),
					})
				}
			}

			// Check dependency readiness condition: if service_healthy is requested, verify upstream has health checks
			if cond, ok := s.DependsOnConditions[depName]; ok && cond == "service_healthy" {
				if depSvc.HealthStrategy == "none" || (!depSvc.HealthCheckEnabled && depSvc.HealthStrategy == "") {
					diagnostics = append(diagnostics, TopologyDiagnostic{
						Severity:    "warning",
						Category:    "dependency",
						ServiceName: s.Name,
						Title:       fmt.Sprintf("Health Check Missing for Condition on '%s'", depSvc.Name),
						Message:     fmt.Sprintf("Service '%s' requires '%s' to be healthy ('service_healthy'), but '%s' has health checks disabled or set to 'none'.", s.Name, depSvc.Name, depSvc.Name),
						Action:      fmt.Sprintf("Configure a valid health check strategy for service '%s' so readiness can be verified.", depSvc.Name),
					})
				}
			}
		}
	}

	return &ProjectTopologyResponse{
		ProjectID:   project.ID,
		ProjectName: project.Name,
		NetworkName: primaryNet,
		Tiers:       tiers,
		Services:    topologyServices,
		Diagnostics: diagnostics,
	}, nil
}
