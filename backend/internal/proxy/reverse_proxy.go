package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalidDomain  = errors.New("invalid custom domain name")
	ErrIPNotAllowed   = errors.New("IP addresses are not allowed as custom domains")
	ErrReservedDomain = errors.New("domain name is reserved")
	ErrDomainTooLong  = errors.New("domain name exceeds maximum length of 253 characters")
	ErrLabelTooLong   = errors.New("domain label exceeds maximum length of 63 characters")
)

var validDomainRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

// ValidateCustomDomain validates and normalizes a candidate custom domain.
func ValidateCustomDomain(candidate string) (string, error) {
	trimmed := strings.TrimSpace(candidate)
	if trimmed == "" {
		return "", ErrInvalidDomain
	}

	// Strip common prefixes if accidentally provided
	lower := strings.ToLower(trimmed)
	lower = strings.TrimPrefix(lower, "https://")
	lower = strings.TrimPrefix(lower, "http://")
	lower = strings.TrimRight(lower, "/")

	// Reject ports, paths, or query params
	if strings.Contains(lower, ":") || strings.Contains(lower, "/") || strings.Contains(lower, "?") {
		return "", fmt.Errorf("%w: domain must not contain ports, paths, or protocols", ErrInvalidDomain)
	}

	if len(lower) > 253 {
		return "", ErrDomainTooLong
	}

	// Reject raw IP addresses
	if ip := net.ParseIP(lower); ip != nil {
		return "", ErrIPNotAllowed
	}

	// Check domain format
	if !strings.Contains(lower, ".") {
		return "", fmt.Errorf("%w: domain must contain at least one dot (e.g. example.com)", ErrInvalidDomain)
	}

	if !validDomainRegex.MatchString(lower) {
		return "", fmt.Errorf("%w: domain contains invalid characters or malformed labels", ErrInvalidDomain)
	}

	labels := strings.Split(lower, ".")
	for _, label := range labels {
		if len(label) == 0 {
			return "", ErrInvalidDomain
		}
		if len(label) > 63 {
			return "", ErrLabelTooLong
		}
	}

	// Reject localhost and reserved local domains
	tld := labels[len(labels)-1]
	if tld == "local" || tld == "internal" || tld == "example" || tld == "test" || tld == "localhost" {
		return "", ErrReservedDomain
	}

	return lower, nil
}

// ProxyRoute represents a reverse-proxy routing entry from a custom domain to a service backend.
type ProxyRoute struct {
	Domain      string `json:"domain"`
	TargetHost  string `json:"target_host"`
	TargetPort  int    `json:"target_port"`
	TLSEnabled  bool   `json:"tls_enabled"`
	ServiceName string `json:"service_name,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
}

// GenerateCaddyfile renders an idiomatic Caddy configuration for all defined routes.
func GenerateCaddyfile(routes []ProxyRoute) string {
	if len(routes) == 0 {
		return "# ForgeLAB Reverse Proxy - No routes configured\n"
	}

	// Sort routes deterministically by domain
	sortedRoutes := make([]ProxyRoute, len(routes))
	copy(sortedRoutes, routes)
	sort.Slice(sortedRoutes, func(i, j int) bool {
		return sortedRoutes[i].Domain < sortedRoutes[j].Domain
	})

	var sb strings.Builder
	sb.WriteString("# ForgeLAB Reverse Proxy Configuration (Auto-Generated)\n\n")

	for _, r := range sortedRoutes {
		targetHost := r.TargetHost
		if targetHost == "" {
			targetHost = "localhost"
		}
		targetPort := r.TargetPort
		if targetPort <= 0 {
			targetPort = 8080
		}

		sb.WriteString(fmt.Sprintf("%s {\n", r.Domain))
		if !r.TLSEnabled {
			sb.WriteString("\ttls off\n")
		}
		sb.WriteString(fmt.Sprintf("\treverse_proxy %s:%d {\n", targetHost, targetPort))
		sb.WriteString("\t\theader_up Host {host}\n")
		sb.WriteString("\t\theader_up X-Real-IP {remote_host}\n")
		sb.WriteString("\t\theader_up X-Forwarded-For {remote_host}\n")
		sb.WriteString("\t\theader_up X-Forwarded-Proto {scheme}\n")
		sb.WriteString("\t}\n")
		sb.WriteString("}\n\n")
	}

	return sb.String()
}

// BuildCaddyConfigJSON renders a Caddy v2 JSON configuration payload suitable for POST /load.
// Verified routes with TLSEnabled use automated ACME certificate issuance.
func BuildCaddyConfigJSON(routes []ProxyRoute, acmeEmail string) ([]byte, error) {
	sortedRoutes := make([]ProxyRoute, len(routes))
	copy(sortedRoutes, routes)
	sort.Slice(sortedRoutes, func(i, j int) bool {
		return sortedRoutes[i].Domain < sortedRoutes[j].Domain
	})

	caddyRoutes := make([]map[string]any, 0, len(sortedRoutes))
	tlsDomains := make([]string, 0, len(sortedRoutes))
	skipHTTPSDomains := make([]string, 0)

	for _, r := range sortedRoutes {
		domain := strings.TrimSpace(strings.ToLower(r.Domain))
		if domain == "" {
			continue
		}
		targetHost := strings.TrimSpace(r.TargetHost)
		if targetHost == "" {
			targetHost = "127.0.0.1"
		}
		targetPort := r.TargetPort
		if targetPort <= 0 {
			targetPort = 8080
		}

		if r.TLSEnabled {
			tlsDomains = append(tlsDomains, domain)
		} else {
			skipHTTPSDomains = append(skipHTTPSDomains, domain)
		}

		caddyRoutes = append(caddyRoutes, map[string]any{
			"match": []map[string]any{
				{"host": []string{domain}},
			},
			"handle": []map[string]any{
				{
					"handler": "reverse_proxy",
					"upstreams": []map[string]any{
						{"dial": net.JoinHostPort(targetHost, fmt.Sprintf("%d", targetPort))},
					},
					"headers": map[string]any{
						"request": map[string]any{
							"set": map[string][]string{
								"X-Real-IP":         {"{http.request.remote.host}"},
								"X-Forwarded-Proto": {"{http.request.scheme}"},
							},
						},
					},
				},
			},
			"terminal": true,
		})
	}

	serverCfg := map[string]any{
		"listen": []string{":80", ":443"},
		"routes": caddyRoutes,
	}
	if len(skipHTTPSDomains) > 0 {
		serverCfg["automatic_https"] = map[string]any{
			"skip": skipHTTPSDomains,
		}
	}

	apps := map[string]any{
		"http": map[string]any{
			"servers": map[string]any{
				"forgelab": serverCfg,
			},
		},
	}

	if len(tlsDomains) > 0 {
		issuer := map[string]any{
			"module": "acme",
		}
		if trimmedEmail := strings.TrimSpace(acmeEmail); trimmedEmail != "" {
			issuer["email"] = trimmedEmail
		}
		apps["tls"] = map[string]any{
			"automation": map[string]any{
				"policies": []map[string]any{
					{
						"subjects": tlsDomains,
						"issuers":  []map[string]any{issuer},
					},
				},
			},
		}
	}

	payload := map[string]any{
		"apps": apps,
	}
	return json.MarshalIndent(payload, "", "  ")
}

// CaddySyncer synchronizes verified custom domain routes with a Caddy Admin API endpoint.
type CaddySyncer struct {
	adminURL   string
	acmeEmail  string
	httpClient *http.Client
}

// NewCaddySyncer constructs a CaddySyncer. When adminURL is empty, SyncRoutes operates as a safe no-op.
func NewCaddySyncer(adminURL, acmeEmail string, client *http.Client) *CaddySyncer {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &CaddySyncer{
		adminURL:   strings.TrimRight(strings.TrimSpace(adminURL), "/"),
		acmeEmail:  strings.TrimSpace(acmeEmail),
		httpClient: client,
	}
}

// IsConfigured returns true if a Caddy Admin API URL has been provided.
func (c *CaddySyncer) IsConfigured() bool {
	return c != nil && c.adminURL != ""
}

// SyncRoutes pushes the full verified route table to Caddy's /load endpoint.
func (c *CaddySyncer) SyncRoutes(ctx context.Context, routes []ProxyRoute) error {
	if !c.IsConfigured() {
		return nil
	}

	cfgBytes, err := BuildCaddyConfigJSON(routes, c.acmeEmail)
	if err != nil {
		return fmt.Errorf("failed to build Caddy JSON config: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.adminURL+"/load", bytes.NewReader(cfgBytes))
	if err != nil {
		return fmt.Errorf("failed to build Caddy reload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to contact Caddy admin API at %s: %w", c.adminURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("Caddy admin API returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	return nil
}
