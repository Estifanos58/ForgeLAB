package proxy

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
)

var (
	ErrInvalidDomain    = errors.New("invalid custom domain name")
	ErrIPNotAllowed     = errors.New("IP addresses are not allowed as custom domains")
	ErrReservedDomain   = errors.New("domain name is reserved")
	ErrDomainTooLong    = errors.New("domain name exceeds maximum length of 253 characters")
	ErrLabelTooLong     = errors.New("domain label exceeds maximum length of 63 characters")
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
