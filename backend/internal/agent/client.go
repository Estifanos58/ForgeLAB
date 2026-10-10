package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/discovery"
)

// ErrAgentSessionUnavailable indicates the local agent session was not found or has expired.
var ErrAgentSessionUnavailable = errors.New("Local agent source session is unavailable or expired. Please reselect the folder.")

// ResolveBaseURL dynamically resolves the local agent URL, handling containerized backend setups.
func ResolveBaseURL() string {
	if u := os.Getenv("FORGELAB_AGENT_URL"); u != "" {
		trimmed := strings.TrimRight(strings.TrimSpace(u), "/")
		if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
			trimmed = "http://" + trimmed
		}
		return trimmed
	}
	if h := os.Getenv("FORGELAB_AGENT_HOST"); h != "" {
		trimmed := strings.TrimRight(strings.TrimSpace(h), "/")
		if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
			trimmed = "http://" + trimmed
		}
		return trimmed
	}

	// Check if running inside container (/.dockerenv exists)
	if _, err := os.Stat("/.dockerenv"); err == nil {
		conn, err := net.DialTimeout("tcp", "host.docker.internal:4142", 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return "http://host.docker.internal:4142"
		}
	}

	// Try 127.0.0.1:4142 first (for local non-docker backend)
	conn, err := net.DialTimeout("tcp", "127.0.0.1:4142", 200*time.Millisecond)
	if err == nil {
		conn.Close()
		return "http://127.0.0.1:4142"
	}
	// Fallback to host.docker.internal:4142 (for containerized backend accessing host agent)
	return "http://host.docker.internal:4142"
}

// FetchSourceDiscovery contacts the running local agent and retrieves the authoritative DiscoveryResult for a source.
func FetchSourceDiscovery(ctx context.Context, baseURL string, sourceID uuid.UUID, token string) (*discovery.DiscoveryResult, error) {
	if baseURL == "" {
		baseURL = ResolveBaseURL()
	}
	cleanBase := strings.TrimRight(baseURL, "/")

	client := &http.Client{Timeout: 10 * time.Second}

	// 1. Try GET /api/agent/sources/{source_id}
	agentURL := fmt.Sprintf("%s/api/agent/sources/%s", cleanBase, sourceID.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, agentURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create agent discovery request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Agent-Session-Token", token)
	}

	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("failed to connect to local agent", "url", agentURL, "error", err)
		return nil, fmt.Errorf("%w (%v)", ErrAgentSessionUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrAgentSessionUnavailable
	}

	if resp.StatusCode == http.StatusOK {
		var sessionResp struct {
			Status    string                     `json:"status"`
			Error     string                     `json:"error,omitempty"`
			Discovery *discovery.DiscoveryResult `json:"discovery,omitempty"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&sessionResp); err == nil && sessionResp.Discovery != nil && len(sessionResp.Discovery.Services) > 0 {
			return sessionResp.Discovery, nil
		}
	}

	// 2. Fallback: try dedicated endpoint GET /api/agent/sources/{source_id}/discovery
	discURL := fmt.Sprintf("%s/api/agent/sources/%s/discovery", cleanBase, sourceID.String())
	reqDisc, err := http.NewRequestWithContext(ctx, http.MethodGet, discURL, nil)
	if err == nil {
		if token != "" {
			reqDisc.Header.Set("Authorization", "Bearer "+token)
			reqDisc.Header.Set("X-Agent-Session-Token", token)
		}
		respDisc, err := client.Do(reqDisc)
		if err == nil {
			defer respDisc.Body.Close()
			if respDisc.StatusCode == http.StatusOK {
				var directDisc discovery.DiscoveryResult
				if err := json.NewDecoder(respDisc.Body).Decode(&directDisc); err == nil && len(directDisc.Services) > 0 {
					return &directDisc, nil
				}
			} else if respDisc.StatusCode == http.StatusNotFound || respDisc.StatusCode == http.StatusUnauthorized || respDisc.StatusCode == http.StatusForbidden {
				return nil, ErrAgentSessionUnavailable
			}
		}
	}

	return nil, ErrAgentSessionUnavailable
}

// FetchSourcePath contacts the running local agent and retrieves the authoritative local source path for a source.
func FetchSourcePath(ctx context.Context, baseURL string, sourceID uuid.UUID, token string) (string, error) {
	if baseURL == "" {
		baseURL = ResolveBaseURL()
	}
	cleanBase := strings.TrimRight(baseURL, "/")

	client := &http.Client{Timeout: 5 * time.Second}

	// 1. Try GET /api/agent/sources/{source_id}
	agentURL := fmt.Sprintf("%s/api/agent/sources/%s", cleanBase, sourceID.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, agentURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create agent path request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Agent-Session-Token", token)
	}

	resp, err := client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var sessionResp struct {
				SourcePath    string                 `json:"source_path"`
				CanonicalPath string                 `json:"canonical_path"`
				Source        map[string]interface{} `json:"source"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&sessionResp); err == nil {
				if sessionResp.SourcePath != "" {
					return sessionResp.SourcePath, nil
				}
				if sessionResp.CanonicalPath != "" {
					return sessionResp.CanonicalPath, nil
				}
				if sessionResp.Source != nil {
					if sp, ok := sessionResp.Source["source_path"].(string); ok && sp != "" {
						return sp, nil
					}
					if cp, ok := sessionResp.Source["canonical_path"].(string); ok && cp != "" {
						return cp, nil
					}
				}
			}
		}
	}

	// 2. Try GET /api/agent/sources/{source_id}/path
	pathURL := fmt.Sprintf("%s/api/agent/sources/%s/path", cleanBase, sourceID.String())
	reqPath, err := http.NewRequestWithContext(ctx, http.MethodGet, pathURL, nil)
	if err == nil {
		if token != "" {
			reqPath.Header.Set("Authorization", "Bearer "+token)
			reqPath.Header.Set("X-Agent-Session-Token", token)
		}
		respPath, err := client.Do(reqPath)
		if err == nil {
			defer respPath.Body.Close()
			if respPath.StatusCode == http.StatusOK {
				var pathResp struct {
					SourcePath string `json:"source_path"`
				}
				if err := json.NewDecoder(respPath.Body).Decode(&pathResp); err == nil && pathResp.SourcePath != "" {
					return pathResp.SourcePath, nil
				}
			}
		}
	}

	return "", ErrAgentSessionUnavailable
}
