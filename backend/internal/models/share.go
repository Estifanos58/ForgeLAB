package models

import (
	"time"

	"github.com/google/uuid"
)

// ShareStatus constants
const (
	ShareStatusActive  = "active"
	ShareStatusRevoked = "revoked"
	ShareStatusExpired = "expired"
)

// TunnelConnectionState constants
const (
	TunnelStateNotShared    = "not_shared"
	TunnelStateConnecting   = "connecting"
	TunnelStateOnline       = "online"
	TunnelStateDisconnected = "disconnected"
	TunnelStateExpired      = "expired"
	TunnelStateRevoked      = "revoked"
	TunnelStateUnavailable  = "unavailable"
)

// RouteMapping maps an incoming HTTP path prefix to an approved service.
type RouteMapping struct {
	PathPrefix  string    `json:"path_prefix"`
	ServiceID   uuid.UUID `json:"service_id"`
	ServiceName string    `json:"service_name,omitempty"`
	StripPrefix bool      `json:"strip_prefix,omitempty"`
}

// PublicShare represents a secure public sharing link for a running project.
type PublicShare struct {
	ID               uuid.UUID      `json:"id"`
	ProjectID        uuid.UUID      `json:"project_id"`
	OwnerID          uuid.UUID      `json:"owner_id"`
	ShareToken       string         `json:"share_token"` // Cryptographically secure identifier
	PublicURL        string         `json:"public_url"`
	PublicHost       string         `json:"public_host"`
	PrimaryServiceID uuid.UUID      `json:"primary_service_id"`
	PrimaryService   *Service       `json:"primary_service,omitempty"`
	RouteMappings    []RouteMapping `json:"route_mappings"`
	Status           string         `json:"status"` // active, revoked, expired
	RevokedAt        *time.Time     `json:"revoked_at,omitempty"`
	ExpiresAt        *time.Time     `json:"expires_at,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

// IsActive returns true if the share is active and not expired.
func (s *PublicShare) IsActive() bool {
	if s == nil || s.Status != ShareStatusActive {
		return false
	}
	if s.ExpiresAt != nil && time.Now().After(*s.ExpiresAt) {
		return false
	}
	return true
}

// CreateShareInput represents input payload for creating or updating a project public share.
type CreateShareInput struct {
	PrimaryServiceID uuid.UUID      `json:"primary_service_id"`
	RouteMappings    []RouteMapping `json:"route_mappings,omitempty"`
	ExpiresInHours   *int           `json:"expires_in_hours,omitempty"`
}

// ShareStatusResponse provides detailed status for project public sharing.
type ShareStatusResponse struct {
	Enabled     bool         `json:"enabled"`
	Configured  bool         `json:"configured"`
	Share       *PublicShare `json:"share,omitempty"`
	TunnelState string       `json:"tunnel_state"` // not_shared, connecting, online, disconnected, expired, revoked, unavailable
	GatewayURL  string       `json:"gateway_url,omitempty"`
	Reason      string       `json:"reason,omitempty"`
}
