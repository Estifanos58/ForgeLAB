package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	// 1. Check if running inside container (/.dockerenv exists)
	if _, err := os.Stat("/.dockerenv"); err == nil {
		conn, err := net.DialTimeout("tcp", "host.docker.internal:4142", 1000*time.Millisecond)
		if err == nil {
			conn.Close()
			return "http://host.docker.internal:4142"
		}
	}

	// 2. Try 127.0.0.1:4142 (local non-docker backend)
	conn, err := net.DialTimeout("tcp", "127.0.0.1:4142", 1000*time.Millisecond)
	if err == nil {
		conn.Close()
		return "http://127.0.0.1:4142"
	}

	// 3. Try localhost:4142
	conn, err = net.DialTimeout("tcp", "localhost:4142", 1000*time.Millisecond)
	if err == nil {
		conn.Close()
		return "http://localhost:4142"
	}

	// 4. Try host.docker.internal:4142
	conn, err = net.DialTimeout("tcp", "host.docker.internal:4142", 1000*time.Millisecond)
	if err == nil {
		conn.Close()
		return "http://host.docker.internal:4142"
	}

	// Final container check
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "http://host.docker.internal:4142"
	}
	return "http://127.0.0.1:4142"
}

// FetchSourceDiscovery contacts the running local agent and retrieves the authoritative DiscoveryResult for a source.
func FetchSourceDiscovery(ctx context.Context, baseURL string, sourceID uuid.UUID, token string) (*discovery.DiscoveryResult, error) {
	// 0. Check in-memory local source session first if available in the same runtime
	if sess, ok := LookupLocalSourceSession(sourceID); ok && sess != nil {
		if sess.Discovery != nil {
			return sess.Discovery, nil
		}
		if sess.Analysis != nil && sess.Analysis.Discovery != nil {
			return sess.Analysis.Discovery, nil
		}
	}

	candidateBases := make([]string, 0, 4)
	if baseURL != "" {
		candidateBases = append(candidateBases, strings.TrimRight(baseURL, "/"))
	}
	resolved := strings.TrimRight(ResolveBaseURL(), "/")
	if resolved != "" && (len(candidateBases) == 0 || candidateBases[0] != resolved) {
		candidateBases = append(candidateBases, resolved)
	}
	for _, fallback := range []string{"http://127.0.0.1:4142", "http://localhost:4142", "http://host.docker.internal:4142"} {
		found := false
		for _, cb := range candidateBases {
			if cb == fallback {
				found = true
				break
			}
		}
		if !found {
			candidateBases = append(candidateBases, fallback)
		}
	}

	client := &http.Client{Timeout: 8 * time.Second}

	for _, cleanBase := range candidateBases {
		// 1. Try GET /api/agent/sources/{source_id}
		agentURL := fmt.Sprintf("%s/api/agent/sources/%s", cleanBase, sourceID.String())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, agentURL, nil)
		if err != nil {
			continue
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
					Status    string                        `json:"status"`
					Error     string                        `json:"error,omitempty"`
					Discovery *discovery.DiscoveryResult    `json:"discovery,omitempty"`
					Services  []discovery.DiscoveredService `json:"services,omitempty"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&sessionResp); err == nil {
					if sessionResp.Discovery != nil {
						return sessionResp.Discovery, nil
					}
					if len(sessionResp.Services) > 0 {
						return &discovery.DiscoveryResult{
							Services:   sessionResp.Services,
							TotalFiles: 1,
						}, nil
					}
				}
			}
		}

		// 2. Try dedicated endpoint GET /api/agent/sources/{source_id}/discovery
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
					if err := json.NewDecoder(respDisc.Body).Decode(&directDisc); err == nil {
						return &directDisc, nil
					}
				}
			}
		}
	}

	// Final fallback: check in-memory global registry once more
	if sess, ok := LookupLocalSourceSession(sourceID); ok && sess != nil {
		if sess.Discovery != nil {
			return sess.Discovery, nil
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
