package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/forgelab/backend/internal/models"
)

var (
	ErrGatewayNotConnected = errors.New("outbound tunnel to public gateway is not connected")
	ErrInvalidGatewayURL   = errors.New("invalid or placeholder GATEWAY_PUBLIC_URL configuration")
)

// GatewayProvider implements ShareProvider for self-hosted ForgeLAB public gateways.
type GatewayProvider struct {
	tunnelManager    *TunnelManager
	gatewayPublicURL string
	gatewayTunnelURL string
	gatewayAuthToken string
	enabled          bool
}

// NewGatewayProvider creates a new GatewayProvider.
func NewGatewayProvider(
	tm *TunnelManager,
	enabled bool,
	gatewayPublicURL string,
	gatewayTunnelURL string,
	gatewayAuthToken string,
) *GatewayProvider {
	return &GatewayProvider{
		tunnelManager:    tm,
		enabled:          enabled,
		gatewayPublicURL: strings.TrimRight(gatewayPublicURL, "/"),
		gatewayTunnelURL: gatewayTunnelURL,
		gatewayAuthToken: gatewayAuthToken,
	}
}

func (p *GatewayProvider) Name() string {
	return models.ProviderGateway
}

func (p *GatewayProvider) IsConfigured() bool {
	return p.enabled && strings.TrimSpace(p.gatewayTunnelURL) != "" && strings.TrimSpace(p.gatewayAuthToken) != ""
}

func (p *GatewayProvider) CheckPrerequisites() (bool, string, error) {
	if !p.enabled {
		return false, "FORGELAB_SHARING_ENABLED is false", errors.New("public sharing is disabled")
	}
	if strings.TrimSpace(p.gatewayTunnelURL) == "" {
		return false, "GATEWAY_TUNNEL_URL is not set", errors.New("missing GATEWAY_TUNNEL_URL")
	}
	if strings.TrimSpace(p.gatewayAuthToken) == "" {
		return false, "GATEWAY_AUTH_TOKEN is not set", errors.New("missing GATEWAY_AUTH_TOKEN")
	}

	u, err := url.Parse(p.gatewayPublicURL)
	if err != nil || u.Host == "" {
		return false, "Invalid GATEWAY_PUBLIC_URL", fmt.Errorf("%w: %v", ErrInvalidGatewayURL, err)
	}

	host := strings.ToLower(u.Hostname())
	if host == "example.com" || host == "yourdomain.com" || host == "forgelab.local" || host == "localhost" {
		return false, fmt.Sprintf("GATEWAY_PUBLIC_URL contains a placeholder hostname (%s)", host),
			fmt.Errorf("%w: placeholder hostname %q must be replaced with your actual public domain", ErrInvalidGatewayURL, host)
	}

	return true, "", nil
}

func (p *GatewayProvider) StartShare(
	ctx context.Context,
	share *models.PublicShare,
	localHandler http.Handler,
) (*ShareEndpoint, error) {
	if !p.IsConfigured() {
		return nil, errors.New("custom public gateway is not configured")
	}

	ready, guide, err := p.CheckPrerequisites()
	if !ready {
		return nil, fmt.Errorf("%w: %s", err, guide)
	}

	if p.tunnelManager == nil || !p.tunnelManager.IsConnected() {
		return nil, ErrGatewayNotConnected
	}

	gatewayParsed, err := url.Parse(p.gatewayPublicURL)
	if err != nil {
		return nil, fmt.Errorf("invalid GATEWAY_PUBLIC_URL %q: %w", p.gatewayPublicURL, err)
	}
	baseDomain := gatewayParsed.Hostname()
	publicHost := fmt.Sprintf("%s.%s", share.ShareToken, baseDomain)
	scheme := gatewayParsed.Scheme
	if scheme == "" {
		scheme = "https"
	}
	publicURL := fmt.Sprintf("%s://%s", scheme, publicHost)

	// Register with gateway with bounded acknowledgment timeout
	if err := p.tunnelManager.RegisterShare(share.ShareToken); err != nil {
		return nil, fmt.Errorf("failed to register share route with gateway: %w", err)
	}

	return &ShareEndpoint{
		PublicURL:  publicURL,
		PublicHost: publicHost,
		State:      models.TunnelStateOnline,
	}, nil
}

func (p *GatewayProvider) StopShare(ctx context.Context, shareToken string) error {
	if p.tunnelManager != nil {
		return p.tunnelManager.DeregisterShare(shareToken)
	}
	return nil
}

func (p *GatewayProvider) GetShareStatus(ctx context.Context, shareToken string) (string, string, error) {
	if p.tunnelManager == nil {
		return models.TunnelStateUnavailable, "gateway tunnel manager is not initialized", nil
	}

	state := p.tunnelManager.GetTunnelState()
	var reason string
	if state == models.TunnelStateDisconnected {
		reason = "The outbound tunnel to the public gateway is disconnected. Attempting to reconnect..."
	}
	return state, reason, nil
}

func (p *GatewayProvider) Close() error {
	if p.tunnelManager != nil {
		p.tunnelManager.Stop()
	}
	return nil
}
