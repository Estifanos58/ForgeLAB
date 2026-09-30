package models

import (
	"time"

	"github.com/google/uuid"
)

// User represents a ForgeLab user account.
// This is completely separate from users of applications deployed onto ForgeLab.
type User struct {
	ID           uuid.UUID `json:"id"`
	Email        string    `json:"email"`
	PasswordHash *string   `json:"-"` // Nullable for OAuth accounts; never serialized to JSON
	DisplayName  string    `json:"display_name"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// AuthIdentity represents an external OAuth identity (Google, GitHub) linked to a user.
type AuthIdentity struct {
	ID              uuid.UUID `json:"id"`
	UserID          uuid.UUID `json:"user_id"`
	Provider        string    `json:"provider"`
	ProviderSubject string    `json:"provider_subject"`
	ProviderEmail   string    `json:"provider_email"`
	EmailVerified   bool      `json:"email_verified"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// GitHubIntegration represents an authorized GitHub connection with repository access.
type GitHubIntegration struct {
	ID                   uuid.UUID `json:"id"`
	UserID               uuid.UUID `json:"user_id"`
	EncryptedAccessToken []byte    `json:"-"` // Never serialized to JSON
	GitHubUserID         string    `json:"github_user_id"`
	GitHubUsername       string    `json:"github_username"`
	Scope                string    `json:"scope"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// Project represents a registered project in ForgeLab.
// A project is an application that ForgeLab manages, containing one or more services.
type Project struct {
	ID                  uuid.UUID  `json:"id"`
	OwnerID             uuid.UUID  `json:"owner_id"`
	SourceID            *uuid.UUID `json:"source_id,omitempty"`
	Name                string     `json:"name"`
	Slug                string     `json:"slug"`
	SourceType          string     `json:"source_type"`      // "local_agent", "local_directory", "local_upload", "github", or legacy "local"
	SourceReference     string     `json:"source_reference"` // repo "owner/repo" or local upload source_id
	RepositoryPath      string     `json:"repository_path"`  // host path for local_directory projects
	Branch              string     `json:"branch"`
	DockerfilePath      string     `json:"dockerfile_path"`
	BuildContext        string     `json:"build_context"`
	BuildStrategy       string     `json:"build_strategy"` // "auto" or "dockerfile"
	BuildCommand        string     `json:"build_command"`
	StartCommand        string     `json:"start_command"`
	RuntimeType         string     `json:"runtime_type"`      // "nextjs", "nodejs", "python-fastapi", "go", etc.
	InternalPort        int        `json:"internal_port"`     // 3000, 8000, 8080, etc.
	HealthCheckPath     *string    `json:"health_check_path"` // Nullable
	HealthCheckEnabled  bool       `json:"health_check_enabled"`
	HealthStrategy      string     `json:"health_strategy"` // "auto", "http", "tcp", "none"
	Status              string     `json:"status"`          // inactive, deploying, running, partially_running, stopped, failed
	CurrentDeploymentID *uuid.UUID `json:"current_deployment_id"`
	Port                *int       `json:"port"`
	PreviewURL          *string    `json:"preview_url,omitempty"`
	Services            []*Service `json:"services,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// Source represents an abstracted code origin (local agent session, archive upload, or GitHub repo).
type Source struct {
	ID              uuid.UUID              `json:"id"`
	OwnerID         uuid.UUID              `json:"owner_id"`
	SourceType      string                 `json:"source_type"` // "local_agent", "local_upload", "github", "local_directory"
	SourceReference string                 `json:"source_reference"`
	AgentID         string                 `json:"agent_id"`
	Fingerprint     string                 `json:"fingerprint"`
	Metadata        map[string]interface{} `json:"metadata"`
	CreatedAt       time.Time              `json:"created_at"`
	UpdatedAt       time.Time              `json:"updated_at"`
}

// BuildCandidate represents a detected build/start option for a service.
type BuildCandidate struct {
	ID              string  `json:"id"`
	Strategy        string  `json:"strategy"` // "dockerfile", "auto", "custom"
	Name            string  `json:"name"`
	Description     string  `json:"description"`
	Confidence      float64 `json:"confidence"` // 0.0 - 1.0
	BuildCommand    string  `json:"build_command"`
	StartCommand    string  `json:"start_command"`
	DockerfilePath  string  `json:"dockerfile_path,omitempty"`
	PackageManager  string  `json:"package_manager,omitempty"`
	SuggestedPort   int     `json:"suggested_port"`
	HealthCheckPath string  `json:"health_check_path"`
	HealthStrategy  string  `json:"health_strategy"`
}

// Service represents an individual deployable service within a parent Project.
type Service struct {
	ID                         uuid.UUID        `json:"id"`
	ProjectID                  uuid.UUID        `json:"project_id"`
	SourceID                   *uuid.UUID       `json:"source_id,omitempty"`
	Name                       string           `json:"name"`
	Role                       string           `json:"role"`        // "frontend", "backend", "worker", "other"
	SourcePath                 string           `json:"source_path"` // relative path inside repo, e.g. ".", "./frontend"
	RuntimeType                string           `json:"runtime_type"`
	Framework                  string           `json:"framework"`
	PackageManager             string           `json:"package_manager"`
	BuildStrategy              string           `json:"build_strategy"`
	BuildCandidates            []BuildCandidate `json:"build_candidates"`
	BuildCommand               string           `json:"build_command"`
	StartCommand               string           `json:"start_command"`
	DockerfilePath             string           `json:"dockerfile_path"`
	BuildContext               string           `json:"build_context"`
	InternalPort               int              `json:"internal_port"`
	HostPort                   *int             `json:"host_port"`
	PublicExposed              bool             `json:"public_exposed"`
	HealthStrategy             string           `json:"health_strategy"`
	HealthCheckPath            *string          `json:"health_check_path"`
	HealthCheckEnabled         bool             `json:"health_check_enabled"`
	Status                     string           `json:"status"` // inactive, deploying, running, stopped, failed
	ContainerID                *string          `json:"container_id"`
	ImageTag                   *string          `json:"image_tag"`
	PreviewURL                 *string          `json:"preview_url,omitempty"`
	CurrentServiceDeploymentID *uuid.UUID       `json:"current_service_deployment_id"`
	CreatedAt                  time.Time        `json:"created_at"`
	UpdatedAt                  time.Time        `json:"updated_at"`
}

// ServiceDeployment represents the deployment record of an individual service.
// In a service-only deployment, DeploymentID is nil. In a release deployment, it references the parent release.
type ServiceDeployment struct {
	ID              uuid.UUID  `json:"id"`
	DeploymentID    *uuid.UUID `json:"deployment_id,omitempty"`
	ServiceID       uuid.UUID  `json:"service_id"`
	ServiceName     string     `json:"service_name,omitempty"`
	DeployNumber    int        `json:"deploy_number"`
	Status          string     `json:"status"`
	ImageTag        *string    `json:"image_tag"`
	ContainerID     *string    `json:"container_id"`
	HostPort        *int       `json:"host_port"`
	InternalPort    int        `json:"internal_port"`
	PreviewURL      *string    `json:"preview_url,omitempty"`
	BuildStrategy   string     `json:"build_strategy"`
	BuildCommand    string     `json:"build_command"`
	StartCommand    string     `json:"start_command"`
	RuntimeType     string     `json:"runtime_type"`
	DockerfilePath  string     `json:"dockerfile_path,omitempty"`
	BuildContext    string     `json:"build_context,omitempty"`
	HealthStrategy  string     `json:"health_strategy,omitempty"`
	HealthCheckPath *string    `json:"health_check_path,omitempty"`
	StartedAt       *time.Time `json:"started_at"`
	BuiltAt         *time.Time `json:"built_at"`
	DeployedAt      *time.Time `json:"deployed_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	DurationMs      *int64     `json:"duration_ms"`
	FailureReason   *string    `json:"failure_reason"`
	CreatedAt       time.Time  `json:"created_at"`
}

// Source provider constants
const (
	SourceTypeLocal          = "local"           // legacy alias
	SourceTypeLocalDirectory = "local_directory" // direct host project directory
	SourceTypeLocalUpload    = "local_upload"    // uploaded zip/tar.gz source workspace
	SourceTypeLocalAgent     = "local_agent"     // connected ForgeLAB local agent
	SourceTypeGitHub         = "github"          // GitHub repository
)

// Service roles
const (
	RoleFrontend = "frontend"
	RoleBackend  = "backend"
	RoleWorker   = "worker"
	RoleOther    = "other"
)

// Build strategy constants
const (
	BuildStrategyAuto       = "auto"
	BuildStrategyDockerfile = "dockerfile"
	BuildStrategyCustom     = "custom"
)

// Health strategy constants
const (
	HealthStrategyAuto = "auto"
	HealthStrategyHTTP = "http"
	HealthStrategyTCP  = "tcp"
	HealthStrategyNone = "none"
)

// ProjectStatus constants
const (
	ProjectStatusInactive         = "inactive"
	ProjectStatusDeploying        = "deploying"
	ProjectStatusRunning          = "running"
	ProjectStatusPartiallyRunning = "partially_running"
	ProjectStatusStopped          = "stopped"
	ProjectStatusFailed           = "failed"
)

// Deployment represents a single deployment release for a project.
// In multi-service projects, a deployment is a parent release containing service deployments.
type Deployment struct {
	ID                 uuid.UUID            `json:"id"`
	ProjectID          uuid.UUID            `json:"project_id"`
	DeployNumber       int                  `json:"deploy_number"`
	Status             string               `json:"status"`
	CommitSHA          *string              `json:"commit_sha"`
	Branch             string               `json:"branch"`
	ImageTag           *string              `json:"image_tag"`
	ContainerID        *string              `json:"container_id"`
	SourceRevision     *string              `json:"source_revision"`
	BuildStrategy      string               `json:"build_strategy"`
	BuildCommand       string               `json:"build_command"`
	StartCommand       string               `json:"start_command"`
	RuntimeType        string               `json:"runtime_type"`
	InternalPort       int                  `json:"internal_port"`
	HealthStrategy     string               `json:"health_strategy"`
	ServiceDeployments []*ServiceDeployment `json:"service_deployments,omitempty"`
	StartedAt          *time.Time           `json:"started_at"`
	BuiltAt            *time.Time           `json:"built_at"`
	DeployedAt         *time.Time           `json:"deployed_at"`
	FinishedAt         *time.Time           `json:"finished_at"`
	DurationMs         *int64               `json:"duration_ms"`
	FailureReason      *string              `json:"failure_reason"`
	CreatedAt          time.Time            `json:"created_at"`
}

// Deployment status constants — see state machine in docs/04-architecture-decisions.md
const (
	DeployStatusQueued         = "queued"
	DeployStatusCloning        = "cloning"
	DeployStatusBuilding       = "building"
	DeployStatusStarting       = "starting"
	DeployStatusHealthChecking = "health_checking"
	DeployStatusRunning        = "running"
	DeployStatusStopped        = "stopped"
	DeployStatusCrashed        = "crashed"
	DeployStatusFailed         = "failed"
)

// EnvironmentVariable represents a project environment variable or secret.
type EnvironmentVariable struct {
	ID             uuid.UUID  `json:"id"`
	ProjectID      uuid.UUID  `json:"project_id"`
	ServiceID      *uuid.UUID `json:"service_id,omitempty"` // Nullable: global or service-scoped
	Key            string     `json:"key"`
	EncryptedValue []byte     `json:"-"` // Never serialized
	IsSecret       bool       `json:"is_secret"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// DeploymentLog represents a single log entry for a deployment or service deployment.
type DeploymentLog struct {
	ID                  int64      `json:"id"`
	DeploymentID        *uuid.UUID `json:"deployment_id,omitempty"`
	ServiceDeploymentID *uuid.UUID `json:"service_deployment_id,omitempty"`
	ServiceID           *uuid.UUID `json:"service_id,omitempty"`
	Timestamp           time.Time  `json:"timestamp"`
	Phase               string     `json:"phase"`  // source, build, startup, health, runtime
	Stream              string     `json:"stream"` // stdout, stderr, system
	Message             string     `json:"message"`
}

// Log phase constants
const (
	LogPhaseSource  = "source"
	LogPhaseBuild   = "build"
	LogPhaseStartup = "startup"
	LogPhaseHealth  = "health"
	LogPhaseRuntime = "runtime"
)

// Log stream constants
const (
	LogStreamStdout = "stdout"
	LogStreamStderr = "stderr"
	LogStreamSystem = "system"
)

// RefreshToken represents a stored refresh token for JWT rotation.
type RefreshToken struct {
	ID        uuid.UUID `json:"id"`
	UserID    uuid.UUID `json:"user_id"`
	TokenHash string    `json:"-"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	Revoked   bool      `json:"revoked"`
}
