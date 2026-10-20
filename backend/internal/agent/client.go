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

// VerifyAgentEndpoint probes a candidate URL to confirm it is an authentic ForgeLAB agent.
// If expectedAgentID is non-empty, it verifies that the agent matches the expected identity.
// Crucially, this verification is performed WITHOUT sending any session credentials.
func VerifyAgentEndpoint(ctx context.Context, baseURL string, expectedAgentID string) (bool, error) {
	cleanBase := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if cleanBase == "" {
		return false, errors.New("empty agent base URL")
	}

	probeURL := cleanBase + "/api/agent/status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return false, err
	}
	if expectedAgentID != "" {
		req.Header.Set("X-Expected-Agent-ID", expectedAgentID)
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("agent status probe returned %d", resp.StatusCode)
	}

	var statusResp struct {
		Status          string `json:"status"`
		AgentID         string `json:"agent_id"`
		AgentIDVerified bool   `json:"agent_id_verified"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&statusResp); err != nil {
		return false, fmt.Errorf("invalid agent status response: %w", err)
	}

	if statusResp.Status != "online" {
		return false, fmt.Errorf("agent status is %q, expected 'online'", statusResp.Status)
	}

	if expectedAgentID != "" {
		if statusResp.AgentIDVerified || statusResp.AgentID == expectedAgentID {
			return true, nil
		}
		return false, fmt.Errorf("agent ID mismatch: endpoint reported %q, expected %q", statusResp.AgentID, expectedAgentID)
	}

	return true, nil
}

// ResolveAndVerifyAgentBaseURL discovers and verifies the authentic agent endpoint without sending credentials.
func ResolveAndVerifyAgentBaseURL(ctx context.Context, preferredBaseURL string, expectedAgentID string) (string, error) {
	candidateBases := make([]string, 0, 8)
	add := func(u string) {
		trimmed := strings.TrimRight(strings.TrimSpace(u), "/")
		if trimmed == "" {
			return
		}
		if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
			trimmed = "http://" + trimmed
		}
		for _, c := range candidateBases {
			if c == trimmed {
				return
			}
		}
		candidateBases = append(candidateBases, trimmed)
	}

	if preferredBaseURL != "" {
		add(preferredBaseURL)
	}
	if u := os.Getenv("FORGELAB_AGENT_URL"); u != "" {
		add(u)
	}
	if h := os.Getenv("FORGELAB_AGENT_HOST"); h != "" {
		add(h)
	}
	for _, c := range ResolveCandidateBaseURLs() {
		add(c)
	}

	for _, cand := range candidateBases {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		verified, err := VerifyAgentEndpoint(probeCtx, cand, expectedAgentID)
		cancel()
		if err == nil && verified {
			return cand, nil
		}
	}

	return "", fmt.Errorf("no verified local agent found matching expected agent ID %q", expectedAgentID)
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

	// 1. Verify endpoint first without credentials
	verifiedBase, err := ResolveAndVerifyAgentBaseURL(ctx, baseURL, "")
	if err != nil {
		// Final fallback: check in-memory global registry once more
		if sess, ok := LookupLocalSourceSession(sourceID); ok && sess != nil {
			if sess.Discovery != nil {
				return sess.Discovery, nil
			}
		}
		return nil, ErrAgentSessionUnavailable
	}

	client := &http.Client{Timeout: 8 * time.Second}

	// 2. Try GET /api/agent/sources/{source_id} ONLY against verified endpoint
	agentURL := fmt.Sprintf("%s/api/agent/sources/%s", verifiedBase, sourceID.String())
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
	}

	// 3. Try dedicated endpoint GET /api/agent/sources/{source_id}/discovery ONLY against verified endpoint
	discURL := fmt.Sprintf("%s/api/agent/sources/%s/discovery", verifiedBase, sourceID.String())
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
	// Verify endpoint first without credentials
	verifiedBase, err := ResolveAndVerifyAgentBaseURL(ctx, baseURL, "")
	if err != nil {
		return "", ErrAgentSessionUnavailable
	}

	client := &http.Client{Timeout: 5 * time.Second}

	// 1. Try GET /api/agent/sources/{source_id} ONLY against verified endpoint
	agentURL := fmt.Sprintf("%s/api/agent/sources/%s", verifiedBase, sourceID.String())
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

	// 2. Try GET /api/agent/sources/{source_id}/path ONLY against verified endpoint
	pathURL := fmt.Sprintf("%s/api/agent/sources/%s/path", verifiedBase, sourceID.String())
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
