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

// Project represents a registered project in ForgeLab.
// A project is an application that ForgeLab manages.
// Project registration does NOT imply a running application.
type Project struct {
	ID                  uuid.UUID  `json:"id"`
	OwnerID             uuid.UUID  `json:"owner_id"`
	Name                string     `json:"name"`
	Slug                string     `json:"slug"`
	SourceType          string     `json:"source_type"` // "local" or "github" (future)
	RepositoryPath      string     `json:"repository_path"`
	Branch              string     `json:"branch"`
	DockerfilePath      string     `json:"dockerfile_path"`
	BuildContext        string     `json:"build_context"`
	HealthCheckPath     *string    `json:"health_check_path"` // Nullable
	HealthCheckEnabled  bool       `json:"health_check_enabled"`
	Status              string     `json:"status"` // inactive, deploying, running, stopped, failed
	CurrentDeploymentID *uuid.UUID `json:"current_deployment_id"`
	Port                *int       `json:"port"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// ProjectStatus constants
const (
	ProjectStatusInactive  = "inactive"
	ProjectStatusDeploying = "deploying"
	ProjectStatusRunning   = "running"
	ProjectStatusStopped   = "stopped"
	ProjectStatusFailed    = "failed"
)

// Deployment represents a single deployment attempt for a project.
// Each deployment is a durable record of operational history.
type Deployment struct {
	ID            uuid.UUID  `json:"id"`
	ProjectID     uuid.UUID  `json:"project_id"`
	DeployNumber  int        `json:"deploy_number"`
	Status        string     `json:"status"`
	CommitSHA     *string    `json:"commit_sha"`
	Branch        string     `json:"branch"`
	ImageTag      *string    `json:"image_tag"`
	ContainerID   *string    `json:"container_id"`
	StartedAt     *time.Time `json:"started_at"`
	BuiltAt       *time.Time `json:"built_at"`
	DeployedAt    *time.Time `json:"deployed_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	DurationMs    *int64     `json:"duration_ms"`
	FailureReason *string    `json:"failure_reason"`
	CreatedAt     time.Time  `json:"created_at"`
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
	ID             uuid.UUID `json:"id"`
	ProjectID      uuid.UUID `json:"project_id"`
	Key            string    `json:"key"`
	EncryptedValue []byte    `json:"-"` // Never serialized
	IsSecret       bool      `json:"is_secret"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// DeploymentLog represents a single log entry for a deployment.
type DeploymentLog struct {
	ID           int64     `json:"id"`
	DeploymentID uuid.UUID `json:"deployment_id"`
	Timestamp    time.Time `json:"timestamp"`
	Phase        string    `json:"phase"`  // source, build, startup, health, runtime
	Stream       string    `json:"stream"` // stdout, stderr, system
	Message      string    `json:"message"`
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
