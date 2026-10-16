package agent

import (
	"bufio"
	"context"
	"encoding/hex"
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

// getDefaultGateway extracts the IPv4 default gateway from /proc/net/route in containerized Linux environments.
func getDefaultGateway() string {
	file, err := os.Open("/proc/net/route")
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 3 && fields[1] == "00000000" {
			d, err := hex.DecodeString(fields[2])
			if err == nil && len(d) == 4 {
				// IPv4 little-endian order: 010012AC -> 172.18.0.1
				return fmt.Sprintf("%d.%d.%d.%d", d[3], d[2], d[1], d[0])
			}
		}
	}
	return ""
}

// GetDefaultGateway extracts the IPv4 default gateway from /proc/net/route in containerized Linux environments.
func GetDefaultGateway() string {
	return getDefaultGateway()
}

// ResolveCandidateBaseURLs returns prioritized candidate base URLs for reaching the local agent.
func ResolveCandidateBaseURLs() []string {
	var candidates []string
	add := func(u string) {
		trimmed := strings.TrimRight(strings.TrimSpace(u), "/")
		if trimmed == "" {
			return
		}
		if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
			trimmed = "http://" + trimmed
		}
		for _, c := range candidates {
			if c == trimmed {
				return
			}
		}
		candidates = append(candidates, trimmed)
	}

	if u := os.Getenv("FORGELAB_AGENT_URL"); u != "" {
		add(u)
	}
	if h := os.Getenv("FORGELAB_AGENT_HOST"); h != "" {
		add(h)
	}

	// 1. Container network default gateway from /proc/net/route (works on Linux containers)
	if gw := getDefaultGateway(); gw != "" {
		add(fmt.Sprintf("http://%s:4142", gw))
	}

	// 2. host.docker.internal (works on Docker Desktop & Linux when extra_hosts is configured)
	add("http://host.docker.internal:4142")

	// 3. Default bridge gateway
	add("http://172.17.0.1:4142")

	// 4. Docker Desktop for Windows / macOS host gateway IP (accessible from Linux containers)
	add("http://192.168.65.254:4142")
	add("http://192.168.65.2:4142")

	// 5. Direct localhost (for non-docker backend setups)
	add("http://127.0.0.1:4142")
	add("http://localhost:4142")

	return candidates
}

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

	candidates := ResolveCandidateBaseURLs()
	for _, cand := range candidates {
		raw := strings.TrimPrefix(strings.TrimPrefix(cand, "http://"), "https://")
		conn, err := net.DialTimeout("tcp", raw, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return cand
		}
	}

	// Fallback when no active probe succeeded:
	isContainer := false
	if _, err := os.Stat("/.dockerenv"); err == nil {
		isContainer = true
	}
	if !isContainer {
		if gw := getDefaultGateway(); gw != "" {
			isContainer = true
		}
	}

	if isContainer {
		if ips, err := net.LookupHost("host.docker.internal"); err == nil && len(ips) > 0 {
			return "http://host.docker.internal:4142"
		}
		if gw := getDefaultGateway(); gw != "" {
			return fmt.Sprintf("http://%s:4142", gw)
		}
		return "http://192.168.65.254:4142"
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

	candidateBases := make([]string, 0, 8)
	if baseURL != "" {
		candidateBases = append(candidateBases, strings.TrimRight(baseURL, "/"))
	}
	resolved := strings.TrimRight(ResolveBaseURL(), "/")
	if resolved != "" {
		found := false
		for _, cb := range candidateBases {
			if cb == resolved {
				found = true
				break
			}
		}
		if !found {
			candidateBases = append(candidateBases, resolved)
		}
	}
	for _, c := range ResolveCandidateBaseURLs() {
		found := false
		for _, cb := range candidateBases {
			if cb == c {
				found = true
				break
			}
		}
		if !found {
			candidateBases = append(candidateBases, c)
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
	candidateBases := make([]string, 0, 8)
	if baseURL != "" {
		candidateBases = append(candidateBases, strings.TrimRight(baseURL, "/"))
	}
	resolved := strings.TrimRight(ResolveBaseURL(), "/")
	if resolved != "" {
		found := false
		for _, cb := range candidateBases {
			if cb == resolved {
				found = true
				break
			}
		}
		if !found {
			candidateBases = append(candidateBases, resolved)
		}
	}
	for _, c := range ResolveCandidateBaseURLs() {
		found := false
		for _, cb := range candidateBases {
			if cb == c {
				found = true
				break
			}
		}
		if !found {
			candidateBases = append(candidateBases, c)
		}
	}

	client := &http.Client{Timeout: 5 * time.Second}

	for _, cleanBase := range candidateBases {
		// 1. Try GET /api/agent/sources/{source_id}
		agentURL := fmt.Sprintf("%s/api/agent/sources/%s", cleanBase, sourceID.String())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, agentURL, nil)
		if err == nil {
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
	}

	return "", ErrAgentSessionUnavailable
}
