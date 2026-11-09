package tunnel

import (
	"fmt"
	"net/url"
	"strings"
)

var (
	// ErrForbiddenUpstream indicates the target upstream URL points to internal infrastructure.
	ErrForbiddenUpstream = fmt.Errorf("upstream target points to forbidden management or infrastructure destination")
)

// IsForbiddenUpstream checks whether the target URL points to a forbidden ForgeLAB management or database infrastructure port.
// Both LocalRouter and ShareScopedRouter share this validation to enforce identical security boundaries.
func IsForbiddenUpstream(upstreamURL string) bool {
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return true
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()

	// Databases must never receive proxied HTTP traffic
	if port == "5432" || port == "6379" {
		return true
	}

	// ForgeLAB control plane (8080), frontend (3000), local agent (4142), or core management containers
	isManagementHost := host == "127.0.0.1" ||
		host == "localhost" ||
		host == "::1" ||
		host == "0.0.0.0" ||
		host == "host.docker.internal" ||
		host == "forgelab-backend" ||
		host == "forgelab-frontend" ||
		host == "forgelab-postgres" ||
		host == "forgelab-redis"

	if isManagementHost && (port == "8080" || port == "4142" || port == "3000") {
		return true
	}

	return false
}

// ValidateUpstreamSecurity returns an error if the upstream URL is invalid or targets forbidden management ports.
func ValidateUpstreamSecurity(upstreamURL string) error {
	if strings.TrimSpace(upstreamURL) == "" {
		return fmt.Errorf("upstream URL cannot be empty")
	}
	if IsForbiddenUpstream(upstreamURL) {
		return ErrForbiddenUpstream
	}
	return nil
}
