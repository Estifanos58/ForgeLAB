package discovery

import (
	"time"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/models"
)

// Repository topology types
const (
	TopologyCompose       = "compose"
	TopologyMonorepo      = "monorepo"
	TopologySingleService = "single_service"
)

// Service classification types
const (
	ClassificationApplication    = "application"
	ClassificationWorker         = "worker"
	ClassificationInfrastructure = "infrastructure"
	ClassificationJob            = "job"
)

// Repository-level deployment strategies
const (
	StrategyCompose     = "compose"
	StrategyDockerfiles = "dockerfile"
	StrategyAuto        = "auto"
	StrategyCustom      = "custom"
)

// HealthCheckConfig represents detailed healthcheck configuration
type HealthCheckConfig = models.HealthCheckConfig

// VolumeMountConfig specifies a container volume mount
type VolumeMountConfig = models.VolumeMountConfig

// NetworkConfig defines a declared Docker network
type NetworkConfig struct {
	Name     string `json:"name"`
	Driver   string `json:"driver,omitempty"`
	Internal bool   `json:"internal,omitempty"`
}

// EnvironmentProvenance tracks the origin and conflict status of an environment variable
type EnvironmentProvenance struct {
	Key                string `json:"key"`
	Value              string `json:"value"` // masked if secret
	IsSecret           bool   `json:"is_secret"`
	Scope              string `json:"scope"`                 // "runtime", "build", "both"
	SourceFile         string `json:"source_file,omitempty"` // e.g. ".env", "docker-compose.yml"
	ServiceName        string `json:"service_name,omitempty"`
	HasConflict        bool   `json:"has_conflict"`
	ConflictResolution string `json:"conflict_resolution"` // "overridden_by_forgelab", "imported", "overridden_by_service"
	ActiveValue        string `json:"active_value"`        // "forgelab" or "source"
}

// DiscoveredService represents an individual deployable unit found during discovery
type DiscoveredService struct {
	Name                string                  `json:"name"`
	Role                string                  `json:"role"`           // "frontend", "backend", "worker", "other"
	Classification      string                  `json:"classification"` // "application", "worker", "infrastructure", "job"
	SourcePath          string                  `json:"source_path"`
	Runtime             string                  `json:"runtime"`
	RuntimeType         string                  `json:"runtime_type"`
	Framework           string                  `json:"framework"`
	PackageManager      string                  `json:"package_manager"`
	BuildStrategy       string                  `json:"build_strategy"`  // "dockerfile", "auto", "custom", "image"
	Image               string                  `json:"image,omitempty"` // e.g. "postgres:16-alpine"
	BuildCandidates     []models.BuildCandidate `json:"build_candidates"`
	BuildCommand        string                  `json:"build_command"`
	StartCommand        string                  `json:"start_command"`
	DockerfilePath      string                  `json:"dockerfile_path"`
	BuildContext        string                  `json:"build_context"`
	InternalPort        int                     `json:"internal_port"`
	HostPort            *int                    `json:"host_port,omitempty"`
	PublicExposed       bool                    `json:"public_exposed"`
	HealthCheck         HealthCheckConfig       `json:"health_check"`
	HealthStrategy      string                  `json:"health_strategy"`
	HealthCheckPath     string                  `json:"health_check_path"`
	HealthCheckEnabled  bool                    `json:"health_check_enabled"`
	DependsOn           []string                `json:"depends_on"`
	DependsOnConditions map[string]string       `json:"depends_on_conditions,omitempty"`
	Volumes             []VolumeMountConfig     `json:"volumes"`
	Networks            []string                `json:"networks,omitempty"`
	Environment         []EnvironmentProvenance `json:"environment"`
	ResourceConfig      models.ResourceConfig   `json:"resource_config"`
	FilesCount          int                     `json:"files_count"`
	TotalBytes          int64                   `json:"total_bytes"`
}

// DiscoveredTopology describes the discovered structure of the codebase
type DiscoveredTopology struct {
	Type            string                  `json:"type"` // "compose", "monorepo", "single_service"
	ComposeFilePath string                  `json:"compose_file_path,omitempty"`
	Networks        []NetworkConfig         `json:"networks"`
	Volumes         []string                `json:"volumes"`   // declared named volumes
	EnvFiles        []string                `json:"env_files"` // detected env files
	RootEnvVars     []EnvironmentProvenance `json:"root_env_vars"`
}

// DiscoveryResult is the authoritative result of repository inspection
type DiscoveryResult struct {
	RepositoryName  string              `json:"repository_name"`
	Topology        DiscoveredTopology  `json:"topology"`
	PrimaryStrategy string              `json:"primary_strategy"` // "compose", "dockerfile", "auto", "custom"
	Services        []DiscoveredService `json:"services"`
	TotalFiles      int                 `json:"total_files"`
	TotalBytes      int64               `json:"total_bytes"`
	Dependencies    map[string][]string `json:"dependencies"` // service -> depends_on
}

// PlannedEndpoint describes a network endpoint
type PlannedEndpoint struct {
	ServiceName string `json:"service_name"`
	Port        int    `json:"port"`
	Type        string `json:"type"` // "public" or "internal"
	Protocol    string `json:"protocol"`
	Address     string `json:"address"` // e.g. "0.0.0.0:8080" or "backend:8080"
}

// PlannedService represents a service ready for execution under a DeploymentPlan
type PlannedService struct {
	Name                string                  `json:"name"`
	Role                string                  `json:"role"`
	Classification      string                  `json:"classification"` // "application", "worker", "infrastructure", "job"
	SourcePath          string                  `json:"source_path"`
	BuildStrategy       string                  `json:"build_strategy"` // "dockerfile", "auto", "custom", "image"
	Image               string                  `json:"image,omitempty"`
	DockerfilePath      string                  `json:"dockerfile_path,omitempty"`
	BuildContext        string                  `json:"build_context,omitempty"`
	BuildCommand        string                  `json:"build_command,omitempty"`
	StartCommand        string                  `json:"start_command,omitempty"`
	RuntimeType         string                  `json:"runtime_type"`
	Framework           string                  `json:"framework"`
	PackageManager      string                  `json:"package_manager,omitempty"`
	InternalPort        int                     `json:"internal_port"`
	HostPort            *int                    `json:"host_port,omitempty"`
	PublicExposed       bool                    `json:"public_exposed"`
	HealthCheck         HealthCheckConfig       `json:"health_check"`
	ResourceConfig      models.ResourceConfig   `json:"resource_config"`
	DependsOn           []string                `json:"depends_on"`
	DependsOnConditions map[string]string       `json:"depends_on_conditions,omitempty"`
	Volumes             []VolumeMountConfig     `json:"volumes"`
	Networks            []string                `json:"networks,omitempty"`
	Environment         []EnvironmentProvenance `json:"environment"`
	BuildCandidates     []models.BuildCandidate `json:"build_candidates,omitempty"`
}

// DeploymentPlan is the immutable blueprint for a deployment release
type DeploymentPlan struct {
	ID                uuid.UUID               `json:"id"`
	SourceID          *uuid.UUID              `json:"source_id,omitempty"`
	SourceRevision    string                  `json:"source_revision"` // commit SHA or content fingerprint
	SourceType        string                  `json:"source_type"`
	Strategy          string                  `json:"strategy"` // "compose", "dockerfile", "auto", "custom"
	Topology          string                  `json:"topology"`
	Services          []PlannedService        `json:"services"`
	Dependencies      map[string][]string     `json:"dependencies"`
	ExecutionTiers    [][]string              `json:"execution_tiers"` // ordered stages for DAG execution
	Networks          []string                `json:"networks"`
	Volumes           []string                `json:"volumes"`
	Environment       []EnvironmentProvenance `json:"environment"`
	PublicEndpoints   []PlannedEndpoint       `json:"public_endpoints"`
	InternalEndpoints []PlannedEndpoint       `json:"internal_endpoints"`
	CreatedAt         time.Time               `json:"created_at"`
}
