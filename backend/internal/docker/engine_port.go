package docker

import (
	"strings"
)

// isPortConflictError returns true if the error indicates a host port allocation conflict.
func isPortConflictError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "already allocated") ||
		strings.Contains(s, "already been allocated") ||
		strings.Contains(s, "address already in use") ||
		strings.Contains(s, "port already in use") ||
		strings.Contains(s, "bind: address already in use") ||
		strings.Contains(s, "failed to allocate gateway")
}

// SetPublicHost sets the host address used for browser-facing API URLs when accessed remotely.
func (e *Engine) SetPublicHost(host string) {
	trimmed := strings.TrimSpace(host)
	if trimmed != "" {
		e.publicHost = trimmed
	}
}

// GetPublicHost returns the configured public host or "localhost".
func (e *Engine) GetPublicHost() string {
	if e.publicHost != "" {
		return e.publicHost
	}
	return "localhost"
}

// SetAppHostIP sets the host IP to bind application container ports to (defaults to 127.0.0.1 for local isolation).
func (e *Engine) SetAppHostIP(ip string) {
	trimmed := strings.TrimSpace(ip)
	if trimmed != "" {
		e.appHostIP = trimmed
	}
}

// GetAppHostIP returns the configured host IP or "127.0.0.1".
func (e *Engine) GetAppHostIP() string {
	if e.appHostIP != "" {
		return e.appHostIP
	}
	return "127.0.0.1"
}
