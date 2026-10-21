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
	"github.com/forgelab/backend/internal/tunnel"
)

var (
	ErrShareNotFound      = errors.New("public share not found")
	ErrShareNotConfigured = errors.New("public sharing is not configured")
	ErrInvalidShareRoute  = errors.New("invalid route mapping")
	ErrServiceNotEligible = errors.New("selected service is not running or not eligible for sharing")
)

// TunnelRegistrar is an interface implemented by the tunnel manager to register and deregister shares (for backward compatibility).
type TunnelRegistrar interface {
	RegisterShare(shareToken string) error
	DeregisterShare(shareToken string) error
	IsConnected() bool
	GetTunnelState() string
}

// ShareService handles CRUD and lifecycle operations for public project shares.
type ShareService struct {
	pool             *pgxpool.Pool
	projectService   *ProjectService
	serviceService   *ServiceService
	provider         tunnel.ShareProvider
	upstreamResolver tunnel.UpstreamResolver
	tunnelRegistrar  TunnelRegistrar
	enabled          bool
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

// SetProvider binds a public share provider (e.g. CloudflareProvider or GatewayProvider).
func (s *ShareService) SetProvider(p tunnel.ShareProvider) {
	s.provider = p
}

// SetUpstreamResolver binds the upstream resolver used by share-scoped routers.
func (s *ShareService) SetUpstreamResolver(ur tunnel.UpstreamResolver) {
	s.upstreamResolver = ur
}

// GetProvider returns the currently active ShareProvider.
func (s *ShareService) GetProvider() tunnel.ShareProvider {
	return s.provider
}

// SetTunnelRegistrar binds the tunnel manager to the share service (legacy/fallback).
func (s *ShareService) SetTunnelRegistrar(tr TunnelRegistrar) {
	s.tunnelRegistrar = tr
}

// IsConfigured returns true if the public sharing feature is configured.
func (s *ShareService) IsConfigured() bool {
	if !s.enabled {
		return false
	}
	if s.provider != nil {
		return s.provider.IsConfigured()
	}
	return s.gatewayTunnelURL != "" && s.gatewayPublicURL != ""
}

// GetProjectShare retrieves the active public share for a project, verifying ownership.
func (s *ShareService) GetProjectShare(ctx context.Context, projectID, userID uuid.UUID) (*models.PublicShare, error) {
	if s.projectService != nil {
		if _, err := s.projectService.GetProject(ctx, projectID, userID); err != nil {
			return nil, err
		}
	}

	if s.pool == nil {
		return nil, ErrShareNotFound
	}

	query := `
		SELECT id, project_id, owner_id, share_token, public_url, public_host,
		       primary_service_id, route_mappings, provider, local_port, status,
		       revoked_at, expires_at, created_at, updated_at
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
		&share.Provider,
		&share.LocalPort,
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
		if s.provider != nil {
			_ = s.provider.StopShare(ctx, share.ShareToken)
		} else if s.tunnelRegistrar != nil {
			_ = s.tunnelRegistrar.DeregisterShare(share.ShareToken)
		}
	}

	// Attach primary service if available
	if s.serviceService != nil {
		if svc, err := s.serviceService.GetService(ctx, share.PrimaryServiceID); err == nil {
			share.PrimaryService = svc
		}
	}

	return &share, nil
}

// GetShareByToken returns an active share by token without requiring owner authentication (for routers).
func (s *ShareService) GetShareByToken(ctx context.Context, shareToken string) (*models.PublicShare, error) {
	cleanToken := strings.TrimSpace(shareToken)
	if cleanToken == "" || s.pool == nil {
		return nil, ErrShareNotFound
	}

	query := `
		SELECT id, project_id, owner_id, share_token, public_url, public_host,
		       primary_service_id, route_mappings, provider, local_port, status,
		       revoked_at, expires_at, created_at, updated_at
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
		&share.Provider,
		&share.LocalPort,
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
		if s.provider != nil {
			_ = s.provider.StopShare(ctx, share.ShareToken)
		}
		return &share, nil
	}

	return &share, nil
}

// ListActiveShareTokens returns all currently active and non-expired share tokens.
func (s *ShareService) ListActiveShareTokens(ctx context.Context) ([]string, error) {
	if s.pool == nil {
		return nil, nil
	}
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
		return nil, fmt.Errorf("%w: public sharing is disabled or unconfigured", ErrShareNotConfigured)
	}

	if s.projectService == nil || s.serviceService == nil || s.pool == nil {
		return nil, errors.New("share service dependencies not initialized")
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
		// Auto-generate standard route mappings: "/" -> primary service
		finalizedMappings = append(finalizedMappings, models.RouteMapping{
			PathPrefix:  "/",
			ServiceID:   primarySvc.ID,
			ServiceName: primarySvc.Name,
		})

		// If backend service exists distinct from frontend, route "/api" to backend
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

	// Generate cryptographically secure token (16 random bytes = 32 hex chars)
	randBytes := make([]byte, 16)
	if _, err := rand.Read(randBytes); err != nil {
		return nil, fmt.Errorf("failed to generate secure share token: %w", err)
	}
	shareToken := fmt.Sprintf("share-%s", hex.EncodeToString(randBytes))

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

	var share models.PublicShare
	share.ProjectID = projectID
	share.OwnerID = userID
	share.ShareToken = shareToken
	share.PrimaryServiceID = primarySvc.ID
	share.PrimaryService = primarySvc
	share.RouteMappings = finalizedMappings
	share.Status = models.ShareStatusActive
	share.ExpiresAt = expiresAt

	// Start tunnel via active provider or legacy registrar
	if s.provider != nil {
		ready, guide, err := s.provider.CheckPrerequisites()
		if !ready {
			return nil, fmt.Errorf("%w:\n\n%s", err, guide)
		}

		if s.provider.Name() == models.ProviderCloudflare {
			scopedRouter := tunnel.NewShareScopedRouter(&share, s.serviceService, s.upstreamResolver)
			endpoint, err := s.provider.StartShare(ctx, &share, scopedRouter)
			if err != nil {
				return nil, fmt.Errorf("failed to start quick tunnel: %w", err)
			}
			share.PublicURL = endpoint.PublicURL
			share.PublicHost = endpoint.PublicHost
			share.LocalPort = &endpoint.LocalPort
			share.Provider = models.ProviderCloudflare
		} else if s.provider.Name() == models.ProviderGateway {
			endpoint, err := s.provider.StartShare(ctx, &share, nil)
			if err != nil {
				return nil, fmt.Errorf("failed to register with public gateway: %w", err)
			}
			share.PublicURL = endpoint.PublicURL
			share.PublicHost = endpoint.PublicHost
			share.Provider = models.ProviderGateway
		}
	} else if s.tunnelRegistrar != nil {
		// Legacy registrar fallback
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
		share.PublicURL = fmt.Sprintf("%s://%s", scheme, publicHost)
		share.PublicHost = publicHost
		share.Provider = models.ProviderGateway

		if regErr := s.tunnelRegistrar.RegisterShare(shareToken); regErr != nil {
			return nil, fmt.Errorf("failed to register with tunnel manager: %w", regErr)
		}
	} else {
		return nil, ErrShareNotConfigured
	}

	// Persist atomically in PostgreSQL transaction
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		if s.provider != nil {
			_ = s.provider.StopShare(ctx, shareToken)
		}
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Revoke any previous active share for this project
	_, err = tx.Exec(ctx, `
		UPDATE public_shares
		SET status = 'revoked', revoked_at = NOW(), updated_at = NOW()
		WHERE project_id = $1 AND status = 'active'
	`, projectID)
	if err != nil {
		if s.provider != nil {
			_ = s.provider.StopShare(ctx, shareToken)
		}
		return nil, fmt.Errorf("failed to revoke previous shares: %w", err)
	}

	insertQuery := `
		INSERT INTO public_shares (
			project_id, owner_id, share_token, public_url, public_host,
			primary_service_id, route_mappings, provider, local_port, status, expires_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, 'active', $10
		)
		RETURNING id, created_at, updated_at
	`
	err = tx.QueryRow(ctx, insertQuery,
		projectID, userID, shareToken, share.PublicURL, share.PublicHost,
		primarySvc.ID, mappingsJSON, share.Provider, share.LocalPort, expiresAt,
	).Scan(&share.ID, &share.CreatedAt, &share.UpdatedAt)
	if err != nil {
		if s.provider != nil {
			_ = s.provider.StopShare(ctx, shareToken)
		}
		return nil, fmt.Errorf("failed to persist public share: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		if s.provider != nil {
			_ = s.provider.StopShare(ctx, shareToken)
		}
		return nil, fmt.Errorf("failed to commit share transaction: %w", err)
	}

	return &share, nil
}

// RevokeShare immediately revokes an active share for a project.
func (s *ShareService) RevokeShare(ctx context.Context, projectID, userID uuid.UUID) error {
	if s.projectService != nil {
		if _, err := s.projectService.GetProject(ctx, projectID, userID); err != nil {
			return err
		}
	}

	if s.pool == nil {
		return ErrShareNotFound
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

	if s.provider != nil && shareToken != "" {
		_ = s.provider.StopShare(ctx, shareToken)
	} else if s.tunnelRegistrar != nil && shareToken != "" {
		_ = s.tunnelRegistrar.DeregisterShare(shareToken)
	}

	return nil
}

// GetShareStatus returns detailed status information including provider and tunnel connectivity.
func (s *ShareService) GetShareStatus(ctx context.Context, projectID, userID uuid.UUID) (*models.ShareStatusResponse, error) {
	providerName := models.ProviderCloudflare
	if s.provider != nil {
		providerName = s.provider.Name()
	} else if s.gatewayTunnelURL != "" {
		providerName = models.ProviderGateway
	}

	resp := &models.ShareStatusResponse{
		Enabled:    s.enabled,
		Configured: s.IsConfigured(),
		Provider:   providerName,
		GatewayURL: s.gatewayPublicURL,
	}

	if !s.IsConfigured() {
		resp.TunnelState = models.TunnelStateUnavailable
		resp.Reason = "Public sharing is unavailable because FORGELAB_SHARING_ENABLED is false or gateway configuration is missing."
		return resp, nil
	}

	// Check provider prerequisites
	if s.provider != nil {
		ready, guide, err := s.provider.CheckPrerequisites()
		if !ready {
			resp.TunnelState = models.TunnelStateUnavailable
			if err != nil {
				resp.Reason = err.Error()
			}
			resp.InstallGuide = guide
			return resp, nil
		}
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

	// Active share: query current status from provider
	if s.provider != nil {
		state, reason, err := s.provider.GetShareStatus(ctx, share.ShareToken)
		if err == nil && state != "" {
			resp.TunnelState = state
			resp.Reason = reason
		} else {
			resp.TunnelState = models.TunnelStateDisconnected
			resp.Reason = "Tunnel endpoint is currently disconnected."
		}
	} else if s.tunnelRegistrar != nil {
		resp.TunnelState = s.tunnelRegistrar.GetTunnelState()
		if resp.TunnelState == models.TunnelStateDisconnected {
			resp.Reason = "The tunnel connection to the public gateway is currently disconnected. Attempting to reconnect..."
		}
	} else {
		resp.TunnelState = models.TunnelStateConnecting
	}

	return resp, nil
}

// ReconcileStaleShares revokes database shares if their provider process is no longer alive after a server restart.
func (s *ShareService) ReconcileStaleShares(ctx context.Context) error {
	if s.pool == nil {
		return nil
	}
	// Cloudflare Quick Tunnel processes terminate when the server stops; their ephemeral trycloudflare URLs
	// cannot be re-used. Clean them up so stale links are not shown as active.
	_, err := s.pool.Exec(ctx, `
		UPDATE public_shares
		SET status = 'revoked', revoked_at = NOW(), updated_at = NOW()
		WHERE status = 'active' AND provider = 'cloudflare'
	`)
	return err
}

// Close gracefully terminates all provider tunnels.
func (s *ShareService) Close() error {
	if s.provider != nil {
		return s.provider.Close()
	}
	return nil
}
