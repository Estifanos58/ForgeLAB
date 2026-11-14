package models

import (
	"context"
	"io"
	"time"

	"github.com/google/uuid"
)

// --- Extension Point 1: GitHub Pull Request Preview Environments ---

// PRPreviewEnvironmentStatus defines the state of an ephemeral PR preview environment.
type PRPreviewEnvironmentStatus string

const (
	PRPreviewStatusPending      PRPreviewEnvironmentStatus = "pending"
	PRPreviewStatusProvisioning PRPreviewEnvironmentStatus = "provisioning"
	PRPreviewStatusActive       PRPreviewEnvironmentStatus = "active"
	PRPreviewStatusDestroying   PRPreviewEnvironmentStatus = "destroying"
	PRPreviewStatusDestroyed    PRPreviewEnvironmentStatus = "destroyed"
	PRPreviewStatusFailed       PRPreviewEnvironmentStatus = "failed"
)

// PRPreviewEnvironment represents an ephemeral preview deployment tied to a pull request.
type PRPreviewEnvironment struct {
	ID                uuid.UUID                  `json:"id"`
	ProjectID         uuid.UUID                  `json:"project_id"`
	PullRequestNumber int                        `json:"pull_request_number"`
	Branch            string                     `json:"branch"`
	HeadSHA           string                     `json:"head_sha"`
	Status            PRPreviewEnvironmentStatus `json:"status"`
	PreviewURL        string                     `json:"preview_url,omitempty"`
	ShareToken        string                     `json:"share_token,omitempty"`
	ExpiresAt         *time.Time                 `json:"expires_at,omitempty"`
	CreatedAt         time.Time                  `json:"created_at"`
	UpdatedAt         time.Time                  `json:"updated_at"`
}

// PRPreviewEnvironmentManager is the extension interface for managing ephemeral PR preview environments.
type PRPreviewEnvironmentManager interface {
	CreatePreviewEnvironment(ctx context.Context, projectID uuid.UUID, prNumber int, branch, headSHA string) (*PRPreviewEnvironment, error)
	GetPreviewEnvironment(ctx context.Context, prID uuid.UUID) (*PRPreviewEnvironment, error)
	ListPreviewEnvironments(ctx context.Context, projectID uuid.UUID) ([]*PRPreviewEnvironment, error)
	DestroyPreviewEnvironment(ctx context.Context, prID uuid.UUID) error
	SynchronizePRStatus(ctx context.Context, projectID uuid.UUID, prNumber int, action string) error
}

// --- Extension Point 2: Multi-Environment Immutable Image Promotion ---

// EnvironmentTier defines the promotion tier for a deployment release.
type EnvironmentTier string

const (
	EnvironmentDevelopment EnvironmentTier = "development"
	EnvironmentStaging     EnvironmentTier = "staging"
	EnvironmentProduction  EnvironmentTier = "production"
)

// EnvironmentPromotionRequest specifies parameters to promote an immutable image digest across tiers.
type EnvironmentPromotionRequest struct {
	SourceTier       EnvironmentTier   `json:"source_tier"`
	TargetTier       EnvironmentTier   `json:"target_tier"`
	ServiceID        uuid.UUID         `json:"service_id"`
	SourceDeployID   uuid.UUID         `json:"source_deploy_id"`
	ImageDigest      string            `json:"image_digest"`
	ApprovedByUserID uuid.UUID         `json:"approved_by_user_id"`
	ConfigOverrides  map[string]string `json:"config_overrides,omitempty"`
}

// EnvironmentPromotionRecord tracks the immutable audit log of an environment promotion.
type EnvironmentPromotionRecord struct {
	ID               uuid.UUID       `json:"id"`
	ServiceID        uuid.UUID       `json:"service_id"`
	SourceTier       EnvironmentTier `json:"source_tier"`
	TargetTier       EnvironmentTier `json:"target_tier"`
	ImageDigest      string          `json:"image_digest"`
	SourceDeployID   uuid.UUID       `json:"source_deploy_id"`
	TargetDeployID   uuid.UUID       `json:"target_deploy_id"`
	PromotedByUserID uuid.UUID       `json:"promoted_by_user_id"`
	Status           string          `json:"status"`
	CreatedAt        time.Time       `json:"created_at"`
}

// EnvironmentPromotionService is the extension interface for multi-stage immutable promotions.
type EnvironmentPromotionService interface {
	PromoteImage(ctx context.Context, req EnvironmentPromotionRequest) (*EnvironmentPromotionRecord, error)
	ListPromotionHistory(ctx context.Context, serviceID uuid.UUID) ([]*EnvironmentPromotionRecord, error)
	ValidatePromotionGate(ctx context.Context, req EnvironmentPromotionRequest) (bool, []string, error)
}

// --- Extension Point 3: SBOM & Vulnerability Scanning ---

// VulnerabilitySeverity denotes the severity of a discovered vulnerability.
type VulnerabilitySeverity string

const (
	SeverityUnknown  VulnerabilitySeverity = "UNKNOWN"
	SeverityLow      VulnerabilitySeverity = "LOW"
	SeverityMedium   VulnerabilitySeverity = "MEDIUM"
	SeverityHigh     VulnerabilitySeverity = "HIGH"
	SeverityCritical VulnerabilitySeverity = "CRITICAL"
)

// VulnerabilityFinding represents a single CVE or security advisory found in an image.
type VulnerabilityFinding struct {
	VulnerabilityID  string                `json:"vulnerability_id"` // e.g. CVE-2026-1234
	PkgName          string                `json:"pkg_name"`
	InstalledVersion string                `json:"installed_version"`
	FixedVersion     string                `json:"fixed_version,omitempty"`
	Severity         VulnerabilitySeverity `json:"severity"`
	Title            string                `json:"title,omitempty"`
	Description      string                `json:"description,omitempty"`
	PrimaryURL       string                `json:"primary_url,omitempty"`
}

// ImageScanReport holds the result of an automated image vulnerability scan and SBOM generation.
type ImageScanReport struct {
	ImageDigest     string                        `json:"image_digest"`
	ScannerName     string                        `json:"scanner_name"`
	ScannedAt       time.Time                     `json:"scanned_at"`
	Vulnerabilities []VulnerabilityFinding        `json:"vulnerabilities"`
	Summary         map[VulnerabilitySeverity]int `json:"summary"`
	PassesGate      bool                          `json:"passes_gate"`
}

// SBOMAndVulnerabilityScanner is the extension interface for image security scanning and SBOM generation.
type SBOMAndVulnerabilityScanner interface {
	GenerateSBOM(ctx context.Context, imageTagOrDigest string, outputFormat string) ([]byte, error)
	ScanImage(ctx context.Context, imageTagOrDigest string) (*ImageScanReport, error)
	EnforceDeploymentSecurityGate(ctx context.Context, report *ImageScanReport, maxSeverityAllowed VulnerabilitySeverity) (bool, error)
}

// --- Extension Point 4: Control-Plane Backup & Disaster Recovery ---

// BackupMetadata describes a control-plane snapshot.
type BackupMetadata struct {
	BackupID          string    `json:"backup_id"`
	CreatedAt         time.Time `json:"created_at"`
	PostgresDumpBytes int64     `json:"postgres_dump_bytes"`
	ProjectsCount     int       `json:"projects_count"`
	ServicesCount     int       `json:"services_count"`
	ChecksumSHA256    string    `json:"checksum_sha256"`
	Encrypted         bool      `json:"encrypted"`
}

// ControlPlaneDisasterRecoveryManager is the extension interface for control-plane backups and recovery.
type ControlPlaneDisasterRecoveryManager interface {
	CreateSnapshot(ctx context.Context, w io.Writer) (*BackupMetadata, error)
	RestoreSnapshot(ctx context.Context, r io.Reader, verifyChecksum string) error
	ListAvailableSnapshots(ctx context.Context) ([]BackupMetadata, error)
	VerifyControlPlaneIntegrity(ctx context.Context) (bool, []string, error)
}
