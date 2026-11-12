package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/client"

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
	if session.CanonicalPath != "" {
		if canon, err := canonicalizeWithAncestor(session.CanonicalPath); err == nil {
			session.CanonicalPath = canon
		}
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

// DockerHealthStatus captures the cached Docker daemon health state monitored in the background.
type DockerHealthStatus struct {
	Available     bool      `json:"available"`
	Status        string    `json:"status"` // "checking", "available", "unavailable"
	Error         string    `json:"error,omitempty"`
	LastCheckedAt time.Time `json:"last_checked_at,omitempty"`
}

type AgentServerConfig struct {
	AgentID                string
	Version                string
	CommitSHA              string
	Host                   string
	Port                   int
	AllowedRoots           []string
	BackendURL             string
	AllowedOrigins         []string
	SessionValidator       func(token, agentID string) (*AgentSession, error)
	SourceSessionValidator func(token string, sourceID uuid.UUID, agentID string) (*AgentSession, error)
	SessionTTL             time.Duration
	Picker                 *NativeFolderPicker
	DockerPinger           func(context.Context) error
	DockerCheckInterval    time.Duration
	DockerPingTimeout      time.Duration
}

type AgentServer struct {
	agentID                string
	version                string
	commitSHA              string
	host                   string
	port                   int
	boundAddr              string
	backendURL             string
	allowedOrigins         []string
	pathValidator          *PathValidator
	picker                 *NativeFolderPicker
	sessions               map[uuid.UUID]*LocalSourceSession
	mu                     sync.RWMutex
	dockerClient           *client.Client
	dockerPinger           func(context.Context) error
	dockerAvailInterval    time.Duration
	dockerRetryInterval    time.Duration
	dockerPingTimeout      time.Duration
	dockerMu               sync.RWMutex
	dockerStatus           DockerHealthStatus
	sessionValidator       func(token, agentID string) (*AgentSession, error)
	sourceSessionValidator func(token string, sourceID uuid.UUID, agentID string) (*AgentSession, error)
	sessionTTL             time.Duration
	monitorCtx             context.Context
	monitorCancel          context.CancelFunc
	stopCleanup            chan struct{}
	closeOnce              sync.Once
	wg                     sync.WaitGroup
}

func NewAgentServer(cfg AgentServerConfig) *AgentServer {
	if cfg.Port <= 0 {
		cfg.Port = 4142
	}
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "127.0.0.1"
	}

	var cli *client.Client
	pinger := cfg.DockerPinger
	if pinger == nil {
		var cliErr error
		cli, cliErr = client.New(client.FromEnv)
		if cliErr == nil && cli != nil {
			pinger = func(ctx context.Context) error {
				_, err := cli.Ping(ctx, client.PingOptions{})
				return err
			}
		} else {
			pinger = func(ctx context.Context) error {
				if cliErr != nil {
					return cliErr
				}
				return errors.New("docker client not initialized")
			}
		}
	}

	pingTimeout := cfg.DockerPingTimeout
	if pingTimeout <= 0 {
		pingTimeout = 2 * time.Second
	}
	availInterval := 10 * time.Second
	retryInterval := 3 * time.Second
	if cfg.DockerCheckInterval > 0 {
		availInterval = cfg.DockerCheckInterval
		retryInterval = cfg.DockerCheckInterval
	}

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

	picker := cfg.Picker
	if picker == nil {
		picker = NewNativeFolderPicker()
	}

	monitorCtx, monitorCancel := context.WithCancel(context.Background())

	s := &AgentServer{
		agentID:                cfg.AgentID,
		version:                cfg.Version,
		commitSHA:              cfg.CommitSHA,
		host:                   host,
		port:                   cfg.Port,
		backendURL:             cfg.BackendURL,
		allowedOrigins:         origins,
		pathValidator:          NewPathValidator(cfg.AllowedRoots),
		picker:                 picker,
		sessions:               make(map[uuid.UUID]*LocalSourceSession),
		dockerClient:           cli,
		dockerPinger:           pinger,
		dockerAvailInterval:    availInterval,
		dockerRetryInterval:    retryInterval,
		dockerPingTimeout:      pingTimeout,
		dockerStatus:           DockerHealthStatus{Available: false, Status: "checking"},
		sessionValidator:       cfg.SessionValidator,
		sourceSessionValidator: cfg.SourceSessionValidator,
		sessionTTL:             ttl,
		monitorCtx:             monitorCtx,
		monitorCancel:          monitorCancel,
		stopCleanup:            make(chan struct{}),
	}

	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		s.cleanupLoop(5 * time.Minute)
	}()
	go func() {
		defer s.wg.Done()
		s.dockerMonitorLoop()
	}()

	return s
}

func (s *AgentServer) Close() {
	s.closeOnce.Do(func() {
		if s.monitorCancel != nil {
			s.monitorCancel()
		}
		if s.stopCleanup != nil {
			close(s.stopCleanup)
		}
		s.wg.Wait()
		if s.dockerClient != nil {
			_ = s.dockerClient.Close()
		}
	})
}

func sanitizeDockerError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(err.Error(), "\r", " "), "\n", " "))
	if msg == "" {
		return "Docker daemon is unavailable"
	}
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "pipe/docker_engine") ||
		strings.Contains(lower, "the system cannot find the file specified") ||
		strings.Contains(lower, "cannot connect to the docker daemon") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "error during connect") {
		return "Docker daemon is unreachable (start Docker Desktop or the Docker service)"
	}
	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}
	return msg
}

func (s *AgentServer) checkDockerOnce() DockerHealthStatus {
	if s.dockerPinger == nil {
		status := DockerHealthStatus{
			Available:     false,
			Status:        "unavailable",
			Error:         "Docker health check not configured",
			LastCheckedAt: time.Now().UTC(),
		}
		s.dockerMu.Lock()
		s.dockerStatus = status
		s.dockerMu.Unlock()
		return status
	}

	parentCtx := s.monitorCtx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(parentCtx, s.dockerPingTimeout)
	defer cancel()

	err := s.dockerPinger(ctx)
	if parentCtx.Err() != nil {
		return s.GetDockerStatus()
	}

	now := time.Now().UTC()
	var status DockerHealthStatus
	if err == nil {
		status = DockerHealthStatus{
			Available:     true,
			Status:        "available",
			LastCheckedAt: now,
		}
	} else {
		status = DockerHealthStatus{
			Available:     false,
			Status:        "unavailable",
			Error:         sanitizeDockerError(err),
			LastCheckedAt: now,
		}
	}

	s.dockerMu.Lock()
	s.dockerStatus = status
	s.dockerMu.Unlock()
	return status
}

func (s *AgentServer) dockerMonitorLoop() {
	status := s.checkDockerOnce()

	for {
		interval := s.dockerAvailInterval
		if !status.Available {
			interval = s.dockerRetryInterval
		}
		if interval <= 0 {
			interval = 10 * time.Second
		}

		timer := time.NewTimer(interval)
		select {
		case <-s.monitorCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
			status = s.checkDockerOnce()
		}
	}
}

// GetDockerStatus returns a thread-safe snapshot of the cached Docker daemon health state.
func (s *AgentServer) GetDockerStatus() DockerHealthStatus {
	s.dockerMu.RLock()
	defer s.dockerMu.RUnlock()
	return s.dockerStatus
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
		session.mu.RLock()
		expired := now.After(session.ExpiresAt)
		session.mu.RUnlock()
		if expired {
			delete(s.sessions, id)
			DeleteLocalSourceSession(id)
			expiredCount++
		}
	}

	return expiredCount
}

// SetBoundAddr records the actual listener address once bound (useful for :0 ephemeral tests and diagnostics).
func (s *AgentServer) SetBoundAddr(addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.boundAddr = strings.TrimSpace(addr)
}

func (s *AgentServer) ListenAddr() string {
	s.mu.RLock()
	bound := s.boundAddr
	host := s.host
	port := s.port
	s.mu.RUnlock()
	if bound != "" {
		return bound
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
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

	dockerStatus := s.GetDockerStatus()
	dockerObj := map[string]interface{}{
		"available": dockerStatus.Available,
		"status":    dockerStatus.Status,
	}
	if dockerStatus.Error != "" {
		dockerObj["error"] = dockerStatus.Error
	}
	if !dockerStatus.LastCheckedAt.IsZero() {
		dockerObj["last_checked_at"] = dockerStatus.LastCheckedAt.UTC().Format(time.RFC3339)
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

		resp := map[string]interface{}{
			"status":            "online",
			"version":           s.version,
			"commit":            s.commitSHA,
			"os":                runtime.GOOS,
			"arch":              runtime.GOARCH,
			"docker_available":  dockerStatus.Available,
			"docker_status":     dockerStatus.Status,
			"docker":            dockerObj,
			"agent_id":          verifiedAgentID,
			"agent_id_verified": idVerified,
			"active_sessions":   0,
		}
		if dockerStatus.Error != "" {
			resp["docker_error"] = dockerStatus.Error
		}
		if !dockerStatus.LastCheckedAt.IsZero() {
			resp["docker_checked_at"] = dockerStatus.LastCheckedAt.UTC().Format(time.RFC3339)
		}

		writeJSON(w, http.StatusOK, resp)
		return
	}

	s.mu.RLock()
	sessionCount := len(s.sessions)
	s.mu.RUnlock()

	resp := map[string]interface{}{
		"status":           "online",
		"agent_id":         s.agentID,
		"version":          s.version,
		"commit":           s.commitSHA,
		"os":               runtime.GOOS,
		"arch":             runtime.GOARCH,
		"docker_available": dockerStatus.Available,
		"docker_status":    dockerStatus.Status,
		"docker":           dockerObj,
		"active_sessions":  sessionCount,
	}
	if dockerStatus.Error != "" {
		resp["docker_error"] = dockerStatus.Error
	}
	if !dockerStatus.LastCheckedAt.IsZero() {
		resp["docker_checked_at"] = dockerStatus.LastCheckedAt.UTC().Format(time.RFC3339)
	}

	writeJSON(w, http.StatusOK, resp)
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
	s.mu.RLock()
	session, exists := s.sessions[sourceID]
	s.mu.RUnlock()
	if !exists {
		return ErrSessionNotFound
	}
	session.mu.Lock()
	session.Status = "consumed"
	session.Phase = "consumed"
	session.UpdatedAt = time.Now()
	session.mu.Unlock()
	return nil
}

// ExpireSession marks a local agent source session as expired immediately (for revocation/testing)
func (s *AgentServer) ExpireSession(sourceID uuid.UUID) bool {
	s.mu.RLock()
	session, exists := s.sessions[sourceID]
	s.mu.RUnlock()
	if !exists {
		return false
	}
	session.mu.Lock()
	session.ExpiresAt = time.Now().Add(-1 * time.Minute)
	session.UpdatedAt = time.Now()
	session.mu.Unlock()
	return true
}
