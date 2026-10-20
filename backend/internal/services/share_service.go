package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/models"
)

var (
	ErrShareNotFound      = errors.New("public share not found")
	ErrShareNotConfigured = errors.New("public sharing gateway is not configured")
	ErrInvalidShareRoute  = errors.New("invalid route mapping")
	ErrServiceNotEligible = errors.New("selected service is not running or not eligible for sharing")
)

// TunnelRegistrar is an interface implemented by the tunnel manager to register and deregister shares.
type TunnelRegistrar interface {
	RegisterShare(shareToken string) error
	DeregisterShare(shareToken string) error
	IsConnected() bool
	GetTunnelState() string
}

// ShareService handles CRUD and lifecycle operations for public project shares.
type ShareService struct {
	pool            *pgxpool.Pool
	projectService  *ProjectService
	serviceService  *ServiceService
	tunnelRegistrar TunnelRegistrar
	enabled         bool
	gatewayPublicURL string
	gatewayTunnelURL string
}

// NewShareService creates a new ShareService.
func NewShareService(
	pool *pgxpool.Pool,
	projectService *ProjectService,
	serviceService *ServiceService,
	enabled bool,
	gatewayPublicURL string,
	gatewayTunnelURL string,
) *ShareService {
	return &ShareService{
		pool:             pool,
		projectService:   projectService,
		serviceService:   serviceService,
		enabled:          enabled,
		gatewayPublicURL: strings.TrimRight(gatewayPublicURL, "/"),
		gatewayTunnelURL: gatewayTunnelURL,
	}
}

// SetTunnelRegistrar binds the tunnel manager to the share service.
func (s *ShareService) SetTunnelRegistrar(tr TunnelRegistrar) {
	s.tunnelRegistrar = tr
}

// IsConfigured returns true if the public gateway is configured.
func (s *ShareService) IsConfigured() bool {
	return s.enabled && s.gatewayTunnelURL != "" && s.gatewayPublicURL != ""
}

// GetProjectShare retrieves the active public share for a project, verifying ownership.
func (s *ShareService) GetProjectShare(ctx context.Context, projectID, userID uuid.UUID) (*models.PublicShare, error) {
	if _, err := s.projectService.GetProject(ctx, projectID, userID); err != nil {
		return nil, err
	}

	query := `
		SELECT id, project_id, owner_id, share_token, public_url, public_host,
		       primary_service_id, route_mappings, status, revoked_at, expires_at, created_at, updated_at
		FROM public_shares
		WHERE project_id = $1 AND status = 'active'
		ORDER BY created_at DESC
		LIMIT 1
	`

	var share models.PublicShare
	var mappingsJSON []byte
	err := s.pool.QueryRow(ctx, query, projectID).Scan(
		&share.ID,
		&share.ProjectID,
		&share.OwnerID,
		&share.ShareToken,
		&share.PublicURL,
		&share.PublicHost,
		&share.PrimaryServiceID,
		&mappingsJSON,
		&share.Status,
		&share.RevokedAt,
		&share.ExpiresAt,
		&share.CreatedAt,
		&share.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrShareNotFound
		}
		return nil, fmt.Errorf("failed to query public share: %w", err)
	}

	if len(mappingsJSON) > 0 {
		_ = json.Unmarshal(mappingsJSON, &share.RouteMappings)
	}

	// Check if share expired
	if share.ExpiresAt != nil && time.Now().After(*share.ExpiresAt) {
		share.Status = models.ShareStatusExpired
		_, _ = s.pool.Exec(ctx, "UPDATE public_shares SET status = 'expired', updated_at = NOW() WHERE id = $1", share.ID)
		if s.tunnelRegistrar != nil {
			_ = s.tunnelRegistrar.DeregisterShare(share.ShareToken)
		}
	}

	// Attach primary service if available
	if svc, err := s.serviceService.GetService(ctx, share.PrimaryServiceID); err == nil {
		share.PrimaryService = svc
	}

	return &share, nil
}

// GetShareByToken returns an active share by token without requiring owner authentication (for local router).
func (s *ShareService) GetShareByToken(ctx context.Context, shareToken string) (*models.PublicShare, error) {
	cleanToken := strings.TrimSpace(shareToken)
	if cleanToken == "" {
		return nil, ErrShareNotFound
	}

	query := `
		SELECT id, project_id, owner_id, share_token, public_url, public_host,
		       primary_service_id, route_mappings, status, revoked_at, expires_at, created_at, updated_at
		FROM public_shares
		WHERE share_token = $1 AND status = 'active'
		LIMIT 1
	`

	var share models.PublicShare
	var mappingsJSON []byte
	err := s.pool.QueryRow(ctx, query, cleanToken).Scan(
		&share.ID,
		&share.ProjectID,
		&share.OwnerID,
		&share.ShareToken,
		&share.PublicURL,
		&share.PublicHost,
		&share.PrimaryServiceID,
		&mappingsJSON,
		&share.Status,
		&share.RevokedAt,
		&share.ExpiresAt,
		&share.CreatedAt,
		&share.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrShareNotFound
		}
		return nil, fmt.Errorf("failed to query public share: %w", err)
	}

	if len(mappingsJSON) > 0 {
		_ = json.Unmarshal(mappingsJSON, &share.RouteMappings)
	}

	if share.ExpiresAt != nil && time.Now().After(*share.ExpiresAt) {
		share.Status = models.ShareStatusExpired
		_, _ = s.pool.Exec(ctx, "UPDATE public_shares SET status = 'expired', updated_at = NOW() WHERE id = $1", share.ID)
		return &share, nil
	}

	return &share, nil
}

// ListActiveShareTokens returns all currently active and non-expired share tokens (for initial tunnel sync).
func (s *ShareService) ListActiveShareTokens(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT share_token
		FROM public_shares
		WHERE status = 'active' AND (expires_at IS NULL OR expires_at > NOW())
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []string
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err == nil {
			tokens = append(tokens, token)
		}
	}
	return tokens, rows.Err()
}

// CreateShare creates or replaces a public share for a project.
func (s *ShareService) CreateShare(ctx context.Context, projectID, userID uuid.UUID, input models.CreateShareInput) (*models.PublicShare, error) {
	if !s.IsConfigured() {
		return nil, fmt.Errorf("%w: set FORGELAB_SHARING_ENABLED=true, GATEWAY_TUNNEL_URL, and GATEWAY_PUBLIC_URL", ErrShareNotConfigured)
	}

	project, err := s.projectService.GetProject(ctx, projectID, userID)
	if err != nil {
		return nil, err
	}

	services, err := s.serviceService.ListServices(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list project services: %w", err)
	}
	if len(services) == 0 {
		return nil, errors.New("project has no services configured")
	}

	// Verify project has at least one running service
	hasRunningService := false
	servicesMap := make(map[uuid.UUID]*models.Service)
	for _, svc := range services {
		servicesMap[svc.ID] = svc
		if svc.Status == models.ServiceStatusRunning {
			hasRunningService = true
		}
	}
	if !hasRunningService && project.Status != models.ProjectStatusRunning {
		return nil, ErrServiceNotEligible
	}

	// Validate primary service
	primarySvc, ok := servicesMap[input.PrimaryServiceID]
	if !ok {
		return nil, fmt.Errorf("primary service %s does not belong to project %s", input.PrimaryServiceID, projectID)
	}

	// Build & validate route mappings
	var finalizedMappings []models.RouteMapping
	if len(input.RouteMappings) > 0 {
		for _, m := range input.RouteMappings {
			targetSvc, exists := servicesMap[m.ServiceID]
			if !exists {
				return nil, fmt.Errorf("target service %s in route mapping does not belong to project", m.ServiceID)
			}
			cleanPrefix := path.Clean("/" + strings.Trim(m.PathPrefix, "/"))
			if cleanPrefix == "." {
				cleanPrefix = "/"
			}
			finalizedMappings = append(finalizedMappings, models.RouteMapping{
				PathPrefix:  cleanPrefix,
				ServiceID:   targetSvc.ID,
				ServiceName: targetSvc.Name,
				StripPrefix: m.StripPrefix,
			})
		}
	} else {
		// Auto-generate standard route mappings:
		// Default rule: "/" -> primary service
		finalizedMappings = append(finalizedMappings, models.RouteMapping{
			PathPrefix:  "/",
			ServiceID:   primarySvc.ID,
			ServiceName: primarySvc.Name,
		})

		// If there is a backend service distinct from primary frontend service, route "/api" to backend
		for _, svc := range services {
			if svc.ID != primarySvc.ID && (svc.Role == models.RoleBackend || strings.Contains(strings.ToLower(svc.Name), "backend") || strings.Contains(strings.ToLower(svc.Name), "api")) {
				finalizedMappings = append([]models.RouteMapping{
					{
						PathPrefix:  "/api",
						ServiceID:   svc.ID,
						ServiceName: svc.Name,
					},
				}, finalizedMappings...)
				break
			}
		}
	}

	// Generate cryptographically secure unpredictable token (16 random bytes = 32 hex chars)
	randBytes := make([]byte, 16)
	if _, err := rand.Read(randBytes); err != nil {
		return nil, fmt.Errorf("failed to generate secure share token: %w", err)
	}
	shareToken := fmt.Sprintf("share-%s", hex.EncodeToString(randBytes))

	// Construct public host and URL
	gatewayParsed, err := url.Parse(s.gatewayPublicURL)
	if err != nil {
		return nil, fmt.Errorf("invalid GATEWAY_PUBLIC_URL %q: %w", s.gatewayPublicURL, err)
	}
	baseDomain := gatewayParsed.Hostname()
	publicHost := fmt.Sprintf("%s.%s", shareToken, baseDomain)
	scheme := gatewayParsed.Scheme
	if scheme == "" {
		scheme = "https"
	}
	publicURL := fmt.Sprintf("%s://%s", scheme, publicHost)

	// Calculate optional expiration
	var expiresAt *time.Time
	if input.ExpiresInHours != nil && *input.ExpiresInHours > 0 {
		exp := time.Now().Add(time.Duration(*input.ExpiresInHours) * time.Hour)
		expiresAt = &exp
	}

	mappingsJSON, err := json.Marshal(finalizedMappings)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize route mappings: %w", err)
	}

	// Revoke any existing active share for this project
	_, _ = s.pool.Exec(ctx, `
		UPDATE public_shares
		SET status = 'revoked', revoked_at = NOW(), updated_at = NOW()
		WHERE project_id = $1 AND status = 'active'
	`, projectID)

	// Insert new share
	insertQuery := `
		INSERT INTO public_shares (
			project_id, owner_id, share_token, public_url, public_host,
			primary_service_id, route_mappings, status, expires_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, 'active', $8
		)
		RETURNING id, created_at, updated_at
	`

	var share models.PublicShare
	share.ProjectID = projectID
	share.OwnerID = userID
	share.ShareToken = shareToken
	share.PublicURL = publicURL
	share.PublicHost = publicHost
	share.PrimaryServiceID = primarySvc.ID
	share.PrimaryService = primarySvc
	share.RouteMappings = finalizedMappings
	share.Status = models.ShareStatusActive
	share.ExpiresAt = expiresAt

	err = s.pool.QueryRow(ctx, insertQuery,
		projectID, userID, shareToken, publicURL, publicHost,
		primarySvc.ID, mappingsJSON, expiresAt,
	).Scan(&share.ID, &share.CreatedAt, &share.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to persist public share: %w", err)
	}

	// Register with active tunnel manager if available
	if s.tunnelRegistrar != nil {
		if regErr := s.tunnelRegistrar.RegisterShare(shareToken); regErr != nil {
			// Do not fail share creation if tunnel is temporarily offline; the background reconciler will sync it
		}
	}

	return &share, nil
}

// RevokeShare immediately revokes an active share for a project.
func (s *ShareService) RevokeShare(ctx context.Context, projectID, userID uuid.UUID) error {
	if _, err := s.projectService.GetProject(ctx, projectID, userID); err != nil {
		return err
	}

	var shareToken string
	err := s.pool.QueryRow(ctx, `
		UPDATE public_shares
		SET status = 'revoked', revoked_at = NOW(), updated_at = NOW()
		WHERE project_id = $1 AND status = 'active'
		RETURNING share_token
	`, projectID).Scan(&shareToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrShareNotFound
		}
		return fmt.Errorf("failed to revoke share: %w", err)
	}

	if s.tunnelRegistrar != nil && shareToken != "" {
		_ = s.tunnelRegistrar.DeregisterShare(shareToken)
	}

	return nil
}

// GetShareStatus returns detailed status information including tunnel connectivity.
func (s *ShareService) GetShareStatus(ctx context.Context, projectID, userID uuid.UUID) (*models.ShareStatusResponse, error) {
	resp := &models.ShareStatusResponse{
		Enabled:    s.enabled,
		Configured: s.IsConfigured(),
		GatewayURL: s.gatewayPublicURL,
	}

	if !s.IsConfigured() {
		resp.TunnelState = models.TunnelStateUnavailable
		resp.Reason = "Public sharing is unavailable because FORGELAB_SHARING_ENABLED is false or gateway configuration is missing."
		return resp, nil
	}

	share, err := s.GetProjectShare(ctx, projectID, userID)
	if err != nil {
		if errors.Is(err, ErrShareNotFound) {
			resp.TunnelState = models.TunnelStateNotShared
			return resp, nil
		}
		return nil, err
	}

	resp.Share = share

	if share.Status == models.ShareStatusRevoked {
		resp.TunnelState = models.TunnelStateRevoked
		return resp, nil
	}
	if share.Status == models.ShareStatusExpired {
		resp.TunnelState = models.TunnelStateExpired
		return resp, nil
	}

	// If active, check tunnel registrar status
	if s.tunnelRegistrar != nil {
		resp.TunnelState = s.tunnelRegistrar.GetTunnelState()
		if resp.TunnelState == models.TunnelStateDisconnected {
			resp.Reason = "The tunnel connection to the public gateway is currently disconnected. Attempting to reconnect..."
		}
	} else {
		resp.TunnelState = models.TunnelStateConnecting
	}

	return resp, nil
}
