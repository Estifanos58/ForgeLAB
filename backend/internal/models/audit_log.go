package models

import (
	"time"

	"github.com/google/uuid"
)

// AuditStatus constants for operational audit trail entries.
const (
	AuditStatusSuccess = "success"
	AuditStatusFailure = "failure"
	AuditStatusWarning = "warning"
)

// AuditLog represents an immutable operational audit event recorded for security and lifecycle actions.
type AuditLog struct {
	ID           uuid.UUID      `json:"id"`
	ActorID      uuid.UUID      `json:"actor_id"`
	ActorLabel   string         `json:"actor_label,omitempty"`
	IsGuest      bool           `json:"is_guest"`
	ProjectID    *uuid.UUID     `json:"project_id,omitempty"`
	ProjectName  string         `json:"project_name,omitempty"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   string         `json:"resource_id,omitempty"`
	Status       string         `json:"status"`
	Message      string         `json:"message"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	IPAddress    string         `json:"ip_address,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}
