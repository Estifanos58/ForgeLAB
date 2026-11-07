package agent

import (
	"context"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/client"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/analyzer"
	"github.com/forgelab/backend/internal/discovery"
)

// LocalSourceSession stores the in-memory mapping between an opaque source ID and the local host path
type LocalSourceSession struct {
	SourceID      uuid.UUID                  `json:"source_id"`
	CanonicalPath string                     `json:"-"` // Never exposed over HTTP/JSON
	FolderName    string                     `json:"folder_name"`
	Status        string                     `json:"status"` // "scanning", "detecting", "ready", "failed"
	Phase         string                     `json:"phase"`  // "scanning", "detecting", "ready", "failed"
	FilesScanned  int                        `json:"files_scanned"`
	TotalFiles    int                        `json:"total_files"`
	TotalBytes    int64                      `json:"total_bytes"`
	DetectedCount int                        `json:"detected_count"`
	Error         string                     `json:"error,omitempty"`
	Analysis      *analyzer.AnalysisResult   `json:"analysis,omitempty"`
	Discovery     *discovery.DiscoveryResult `json:"discovery,omitempty"`
	CreatedAt     time.Time                  `json:"created_at"`
	UpdatedAt     time.Time                  `json:"updated_at"`
	ExpiresAt     time.Time                  `json:"expires_at"`
	Token         string                     `json:"-"`
	mu            sync.RWMutex               `json:"-"`
}

var (
	globalSourceSessionsMu sync.RWMutex
	globalSourceSessions   = make(map[uuid.UUID]*LocalSourceSession)
)

// RegisterLocalSourceSession registers a local source session in the global registry.
func RegisterLocalSourceSession(session *LocalSourceSession) {
	if session == nil {
		return
	}
	globalSourceSessionsMu.Lock()
	defer globalSourceSessionsMu.Unlock()
	globalSourceSessions[session.SourceID] = session
}

// LookupLocalSourceSession finds a registered local source session by source ID.
func LookupLocalSourceSession(sourceID uuid.UUID) (*LocalSourceSession, bool) {
	globalSourceSessionsMu.RLock()
	defer globalSourceSessionsMu.RUnlock()
	sess, ok := globalSourceSessions[sourceID]
	return sess, ok
}

// DeleteLocalSourceSession removes a registered local source session from the global registry.
func DeleteLocalSourceSession(sourceID uuid.UUID) {
	globalSourceSessionsMu.Lock()
	defer globalSourceSessionsMu.Unlock()
	delete(globalSourceSessions, sourceID)
}

type AgentServerConfig struct {
	AgentID                string
	Version                string
	CommitSHA              string
	Port                   int
	AllowedRoots           []string
	BackendURL             string
	AllowedOrigins         []string
	SessionValidator       func(token, agentID string) (*AgentSession, error)
	SourceSessionValidator func(token string, sourceID uuid.UUID, agentID string) (*AgentSession, error)
	SessionTTL             time.Duration
}

type AgentServer struct {
	agentID                string
	version                string
	commitSHA              string
	port                   int
	backendURL             string
	allowedOrigins         []string
	pathValidator          *PathValidator
	picker                 *NativeFolderPicker
	sessions               map[uuid.UUID]*LocalSourceSession
	mu                     sync.RWMutex
	dockerClient           *client.Client
	sessionValidator       func(token, agentID string) (*AgentSession, error)
	sourceSessionValidator func(token string, sourceID uuid.UUID, agentID string) (*AgentSession, error)
	sessionTTL             time.Duration
	stopCleanup            chan struct{}
}

func NewAgentServer(cfg AgentServerConfig) *AgentServer {
	if cfg.Port <= 0 {
		cfg.Port = 4142
	}
	cli, _ := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())

	origins := []string{
		"http://localhost:3000",
		"http://127.0.0.1:3000",
		"http://localhost:8080",
		"http://127.0.0.1:8080",
	}
	if cfg.BackendURL != "" {
		origins = append(origins, strings.TrimRight(cfg.BackendURL, "/"))
	}
	for _, o := range cfg.AllowedOrigins {
		trimmed := strings.TrimRight(o, "/")
		if trimmed != "" {
			origins = append(origins, trimmed)
		}
	}

	ttl := cfg.SessionTTL
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}

	s := &AgentServer{
		agentID:                cfg.AgentID,
		version:                cfg.Version,
		commitSHA:              cfg.CommitSHA,
		port:                   cfg.Port,
		backendURL:             cfg.BackendURL,
		allowedOrigins:         origins,
		pathValidator:          NewPathValidator(cfg.AllowedRoots),
		picker:                 NewNativeFolderPicker(),
		sessions:               make(map[uuid.UUID]*LocalSourceSession),
		dockerClient:           cli,
		sessionValidator:       cfg.SessionValidator,
		sourceSessionValidator: cfg.SourceSessionValidator,
		sessionTTL:             ttl,
		stopCleanup:            make(chan struct{}),
	}

	go s.cleanupLoop(5 * time.Minute)

	return s
}

func (s *AgentServer) Close() {
	if s.stopCleanup != nil {
		select {
		case <-s.stopCleanup:
		default:
			close(s.stopCleanup)
		}
	}
}

func (s *AgentServer) cleanupLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.CleanupExpiredSessions()
		case <-s.stopCleanup:
			return
		}
	}
}

func (s *AgentServer) CleanupExpiredSessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	expiredCount := 0

	for id, session := range s.sessions {
		if now.After(session.ExpiresAt) {
			delete(s.sessions, id)
			DeleteLocalSourceSession(id)
			expiredCount++
		}
	}

	return expiredCount
}

func (s *AgentServer) ListenAddr() string {
	return "127.0.0.1:" + string(rune(s.port))
}

func (s *AgentServer) AgentID() string {
	return s.agentID
}

func (s *AgentServer) Router() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/agent/status", s.handleStatus)
	mux.HandleFunc("/api/agent/verify-identity", s.handleVerifyIdentity)
	mux.HandleFunc("/api/agent/select-folder", s.requireAuth(s.handleSelectFolder))
	mux.HandleFunc("/api/agent/select-path", s.requireAuth(s.handleSelectPath))
	mux.HandleFunc("/api/agent/sources/", s.requireSourceAuth(s.handleSourcesRoutes))

	return s.corsMiddleware(mux)
}

func (s *AgentServer) isOriginAllowed(origin string) bool {
	if origin == "" {
		return true // Same-host or non-browser client (e.g. backend container dialing agent)
	}
	norm := strings.ToLower(strings.TrimRight(origin, "/"))
	for _, o := range s.allowedOrigins {
		if strings.ToLower(strings.TrimRight(o, "/")) == norm {
			return true
		}
	}
	return false
}

func (s *AgentServer) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !s.isOriginAllowed(origin) {
				http.Error(w, "origin not allowed by agent security policy", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, X-ForgeLAB-Agent, X-Agent-Session-Token")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *AgentServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	dockerAvailable := false
	if s.dockerClient != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if _, err := s.dockerClient.Ping(ctx); err == nil {
			dockerAvailable = true
		}
	}

	// Check authentication to avoid leaking agent credentials or internal session counts
	token := extractToken(r)
	authenticated := false
	if token != "" {
		if _, err := s.validateToken(r.Context(), token); err == nil {
			authenticated = true
		}
	}

	if !authenticated {
		expectedID := strings.TrimSpace(r.Header.Get("X-Expected-Agent-ID"))
		if expectedID == "" {
			expectedID = strings.TrimSpace(r.URL.Query().Get("expected_agent_id"))
		}

		verifiedAgentID := ""
		idVerified := false
		if expectedID != "" && expectedID == s.agentID {
			verifiedAgentID = s.agentID
			idVerified = true
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":            "online",
			"version":           s.version,
			"commit":            s.commitSHA,
			"os":                runtime.GOOS,
			"arch":              runtime.GOARCH,
			"docker_available":  dockerAvailable,
			"agent_id":          verifiedAgentID,
			"agent_id_verified": idVerified,
			"active_sessions":   0,
		})
		return
	}

	s.mu.RLock()
	sessionCount := len(s.sessions)
	s.mu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":           "online",
		"agent_id":         s.agentID,
		"version":          s.version,
		"commit":           s.commitSHA,
		"os":               runtime.GOOS,
		"arch":             runtime.GOARCH,
		"docker_available": dockerAvailable,
		"active_sessions":  sessionCount,
	})
}

// RevokeSession explicitly revokes and cleans up a session in the agent server.
func (s *AgentServer) RevokeSession(sourceID uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.sessions[sourceID]; exists {
		delete(s.sessions, sourceID)
		DeleteLocalSourceSession(sourceID)
		return true
	}
	return false
}

// ConsumeSession marks a local source session as consumed by a created project
func (s *AgentServer) ConsumeSession(sourceID uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, exists := s.sessions[sourceID]
	if !exists {
		return ErrSessionNotFound
	}
	session.Status = "consumed"
	session.Phase = "consumed"
	session.UpdatedAt = time.Now()
	return nil
}

// ExpireSession marks a local agent source session as expired immediately (for revocation/testing)
func (s *AgentServer) ExpireSession(sourceID uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, exists := s.sessions[sourceID]
	if !exists {
		return false
	}
	session.ExpiresAt = time.Now().Add(-1 * time.Minute)
	session.UpdatedAt = time.Now()
	return true
}
