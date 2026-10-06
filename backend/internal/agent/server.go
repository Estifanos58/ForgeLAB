package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/client"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/analyzer"
	"github.com/forgelab/backend/internal/detector"
	"github.com/forgelab/backend/internal/dockerignore"
	"github.com/forgelab/backend/internal/envparser"
	"github.com/forgelab/backend/internal/security"
)

// LocalSourceSession stores the in-memory mapping between an opaque source ID and the local host path
type LocalSourceSession struct {
	SourceID      uuid.UUID                `json:"source_id"`
	CanonicalPath string                   `json:"-"` // Never exposed over HTTP/JSON
	FolderName    string                   `json:"folder_name"`
	Status        string                   `json:"status"` // "scanning", "detecting", "ready", "failed"
	Phase         string                   `json:"phase"`  // "scanning", "detecting", "ready", "failed"
	FilesScanned  int                      `json:"files_scanned"`
	TotalFiles    int                      `json:"total_files"`
	TotalBytes    int64                    `json:"total_bytes"`
	DetectedCount int                      `json:"detected_count"`
	Error         string                   `json:"error,omitempty"`
	Analysis      *analyzer.AnalysisResult `json:"analysis,omitempty"`
	CreatedAt     time.Time                `json:"created_at"`
	UpdatedAt     time.Time                `json:"updated_at"`
	ExpiresAt     time.Time                `json:"expires_at"`
	Token         string                   `json:"-"`
	mu            sync.RWMutex             `json:"-"`
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
		trimmed := strings.TrimSpace(o)
		if trimmed != "" {
			origins = append(origins, trimmed)
		}
	}
	if envOrigins := os.Getenv("FORGELAB_ALLOWED_ORIGINS"); envOrigins != "" {
		for _, o := range strings.Split(envOrigins, ",") {
			trimmed := strings.TrimSpace(o)
			if trimmed != "" {
				origins = append(origins, trimmed)
			}
		}
	}

	ttl := cfg.SessionTTL
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}

	agentID := strings.TrimSpace(cfg.AgentID)
	if agentID == "" {
		agentID = uuid.New().String()
	}

	version := strings.TrimSpace(cfg.Version)
	if version == "" {
		version = "1.0.0"
	}
	commitSHA := strings.TrimSpace(cfg.CommitSHA)
	if commitSHA == "" {
		commitSHA = "dev"
	}

	srv := &AgentServer{
		agentID:                agentID,
		version:                version,
		commitSHA:              commitSHA,
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

	go srv.cleanupLoop(5 * time.Minute)
	return srv
}

// Close gracefully stops background tasks in the agent server
func (s *AgentServer) Close() {
	select {
	case <-s.stopCleanup:
	default:
		close(s.stopCleanup)
	}
}

func (s *AgentServer) cleanupLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCleanup:
			return
		case <-ticker.C:
			s.CleanupExpiredSessions()
		}
	}
}

// CleanupExpiredSessions removes sessions whose TTL has elapsed
func (s *AgentServer) CleanupExpiredSessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	removed := 0
	for id, sess := range s.sessions {
		if now.After(sess.ExpiresAt) {
			delete(s.sessions, id)
			removed++
		}
	}
	return removed
}

// ListenAddr returns the strictly loopback bound address
func (s *AgentServer) ListenAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", s.port)
}

// AgentID returns the current agent instance ID
func (s *AgentServer) AgentID() string {
	return s.agentID
}

// Router returns an http.Handler with all agent endpoints and CORS support
func (s *AgentServer) Router() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/agent/status", s.handleStatus)
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

func extractToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	if tok := r.Header.Get("X-Agent-Session-Token"); tok != "" {
		return strings.TrimSpace(tok)
	}
	return ""
}

func (s *AgentServer) validateToken(ctx context.Context, token string) (*AgentSession, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("missing session token")
	}

	// 1. Custom configured validator
	if s.sessionValidator != nil {
		return s.sessionValidator(token, s.agentID)
	}

	// 2. Global session manager (same-host / local runtime)
	if sm := GetGlobalSessionManager(); sm != nil {
		if sess, err := sm.ValidateToken(token); err == nil {
			return sess, nil
		}
	}

	// 3. Fallback: call backend HTTP verification endpoint
	if s.backendURL != "" {
		validateURL := fmt.Sprintf("%s/api/sources/agent/session/validate", strings.TrimRight(s.backendURL, "/"))
		payload, _ := json.Marshal(map[string]string{
			"token":    token,
			"agent_id": s.agentID,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, validateURL, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("backend session validation failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			var errPayload struct {
				Error  string `json:"error"`
				Reason string `json:"reason"`
			}
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = json.Unmarshal(bodyBytes, &errPayload)
			errMsg := errPayload.Reason
			if errMsg == "" {
				errMsg = errPayload.Error
			}
			if errMsg == "" {
				errMsg = strings.TrimSpace(string(bodyBytes))
			}
			if errMsg == "" {
				errMsg = fmt.Sprintf("status %d", resp.StatusCode)
			}

			lower := strings.ToLower(errMsg)
			if strings.Contains(lower, "expired") {
				return nil, fmt.Errorf("%w: %s", ErrSessionExpired, errMsg)
			}
			if strings.Contains(lower, "consumed") {
				return nil, fmt.Errorf("%w: %s", ErrSessionConsumed, errMsg)
			}
			if strings.Contains(lower, "agent id mismatch") || strings.Contains(lower, "agent mismatch") || resp.StatusCode == http.StatusForbidden {
				return nil, fmt.Errorf("%w: %s", ErrUnauthorized, errMsg)
			}
			if strings.Contains(lower, "not found") {
				return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, errMsg)
			}
			return nil, fmt.Errorf("backend session validation rejected (%d): %s", resp.StatusCode, errMsg)
		}

		var res struct {
			Valid     bool      `json:"valid"`
			SessionID string    `json:"session_id"`
			UserID    string    `json:"user_id"`
			AgentID   string    `json:"agent_id"`
			ExpiresAt time.Time `json:"expires_at"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			return nil, err
		}
		sID, _ := uuid.Parse(res.SessionID)
		uID, _ := uuid.Parse(res.UserID)
		return &AgentSession{
			ID:        sID,
			Token:     token,
			UserID:    uID,
			AgentID:   res.AgentID,
			ExpiresAt: res.ExpiresAt,
		}, nil
	}

	return nil, ErrSessionNotFound
}

func (s *AgentServer) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := extractToken(r)
		if token == "" {
			slog.Warn("agent authorization rejected: token missing",
				"path", r.URL.Path,
				"agent_id", s.agentID,
			)
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error":  "unauthorized: agent session token is required",
				"reason": "token missing",
			})
			return
		}

		sess, err := s.validateToken(r.Context(), token)
		if err != nil {
			diagReason := "backend validation rejected"
			statusCode := http.StatusUnauthorized

			if errors.Is(err, ErrSessionExpired) {
				diagReason = "token expired"
				statusCode = http.StatusUnauthorized
			} else if errors.Is(err, ErrSessionConsumed) {
				diagReason = "consumed session"
				statusCode = http.StatusUnauthorized
			} else if errors.Is(err, ErrUnauthorized) {
				diagReason = "agent mismatch"
				statusCode = http.StatusForbidden
			} else if errors.Is(err, ErrSessionNotFound) {
				diagReason = "session not found"
				statusCode = http.StatusUnauthorized
			}

			slog.Warn("agent authorization rejected",
				"path", r.URL.Path,
				"agent_id", s.agentID,
				"reason", diagReason,
				"detail", err.Error(),
			)

			writeJSON(w, statusCode, map[string]string{
				"error":  "unauthorized: " + err.Error(),
				"reason": diagReason,
			})
			return
		}

		// Store session in context if needed
		ctx := context.WithValue(r.Context(), "agent_session", sess)
		ctx = context.WithValue(ctx, "agent_token", token)
		next(w, r.WithContext(ctx))
	}
}

func (s *AgentServer) validateTokenForSource(ctx context.Context, token string, sourceID uuid.UUID) (*AgentSession, error) {
	cleanToken := strings.TrimSpace(token)
	if cleanToken == "" {
		return nil, errors.New("missing session token")
	}

	// 1. Custom configured source session validator
	if s.sourceSessionValidator != nil {
		return s.sourceSessionValidator(cleanToken, sourceID, s.agentID)
	}

	// 2. Global session manager (same-host / local runtime)
	if sm := GetGlobalSessionManager(); sm != nil {
		if sess, err := sm.VerifyTokenForSource(cleanToken, sourceID, s.agentID); err == nil {
			return sess, nil
		} else if !errors.Is(err, ErrSessionNotFound) && s.backendURL == "" && s.sessionValidator == nil {
			return nil, err
		}
	}

	// 3. Fallback to custom sessionValidator if configured (e.g. unit tests without global session manager)
	if s.sessionValidator != nil {
		sess, err := s.sessionValidator(cleanToken, s.agentID)
		if err != nil {
			return nil, err
		}
		if sess.SourceID != nil && *sess.SourceID != sourceID {
			return nil, fmt.Errorf("%w: session not bound to requested source", ErrUnauthorized)
		}
		return sess, nil
	}

	// 4. Fallback: call backend HTTP verification endpoint with source_id
	if s.backendURL != "" {
		validateURL := fmt.Sprintf("%s/api/sources/agent/session/validate", strings.TrimRight(s.backendURL, "/"))
		payload, _ := json.Marshal(map[string]string{
			"token":     cleanToken,
			"source_id": sourceID.String(),
			"agent_id":  s.agentID,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, validateURL, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("backend session validation failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			var errPayload struct {
				Error  string `json:"error"`
				Reason string `json:"reason"`
			}
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = json.Unmarshal(bodyBytes, &errPayload)
			errMsg := errPayload.Reason
			if errMsg == "" {
				errMsg = errPayload.Error
			}
			if errMsg == "" {
				errMsg = strings.TrimSpace(string(bodyBytes))
			}
			if errMsg == "" {
				errMsg = fmt.Sprintf("status %d", resp.StatusCode)
			}

			lower := strings.ToLower(errMsg)
			if strings.Contains(lower, "expired") {
				return nil, fmt.Errorf("%w: %s", ErrSessionExpired, errMsg)
			}
			if strings.Contains(lower, "consumed") {
				return nil, fmt.Errorf("%w: %s", ErrSessionConsumed, errMsg)
			}
			if strings.Contains(lower, "agent id mismatch") || strings.Contains(lower, "agent mismatch") || strings.Contains(lower, "not bound to requested source") || strings.Contains(lower, "source mismatch") || resp.StatusCode == http.StatusForbidden {
				return nil, fmt.Errorf("%w: %s", ErrUnauthorized, errMsg)
			}
			if strings.Contains(lower, "not found") {
				return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, errMsg)
			}
			return nil, fmt.Errorf("backend validation rejected (%d): %s", resp.StatusCode, errMsg)
		}

		var res struct {
			Valid     bool      `json:"valid"`
			SessionID string    `json:"session_id"`
			UserID    string    `json:"user_id"`
			AgentID   string    `json:"agent_id"`
			SourceID  string    `json:"source_id"`
			ExpiresAt time.Time `json:"expires_at"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			return nil, err
		}
		sID, _ := uuid.Parse(res.SessionID)
		uID, _ := uuid.Parse(res.UserID)
		sess := &AgentSession{
			ID:        sID,
			Token:     cleanToken,
			UserID:    uID,
			AgentID:   res.AgentID,
			ExpiresAt: res.ExpiresAt,
		}
		if srcUUID, parseErr := uuid.Parse(res.SourceID); parseErr == nil && srcUUID != uuid.Nil {
			sess.SourceID = &srcUUID
		}
		return sess, nil
	}

	return nil, ErrSessionNotFound
}

func (s *AgentServer) requireSourceAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := extractToken(r)
		if token == "" {
			slog.Warn("agent source authorization rejected: token missing",
				"path", r.URL.Path,
				"agent_id", s.agentID,
			)
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error":  "unauthorized: agent session token is required",
				"reason": "token missing",
			})
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/api/agent/sources/")
		parts := strings.Split(path, "/")
		if len(parts) == 0 || parts[0] == "" {
			http.Error(w, "source_id is required", http.StatusBadRequest)
			return
		}

		sourceUUID, err := uuid.Parse(parts[0])
		if err != nil {
			http.Error(w, "invalid source UUID format", http.StatusBadRequest)
			return
		}

		sess, err := s.validateTokenForSource(r.Context(), token, sourceUUID)
		if err != nil {
			diagReason := "backend validation rejected"
			statusCode := http.StatusUnauthorized

			if errors.Is(err, ErrSessionExpired) {
				diagReason = "token expired"
				statusCode = http.StatusUnauthorized
			} else if errors.Is(err, ErrSessionConsumed) {
				diagReason = "consumed session"
				statusCode = http.StatusUnauthorized
			} else if errors.Is(err, ErrUnauthorized) {
				statusCode = http.StatusForbidden
				lower := strings.ToLower(err.Error())
				if strings.Contains(lower, "agent id mismatch") || strings.Contains(lower, "agent mismatch") {
					diagReason = "agent mismatch"
				} else if strings.Contains(lower, "not bound to requested source") || strings.Contains(lower, "source mismatch") {
					diagReason = "source mismatch"
				} else {
					diagReason = "source mismatch"
				}
			} else if errors.Is(err, ErrSessionNotFound) {
				diagReason = "session not found"
				statusCode = http.StatusUnauthorized
			}

			slog.Warn("agent source authorization rejected",
				"source_id", sourceUUID.String(),
				"agent_id", s.agentID,
				"reason", diagReason,
				"detail", err.Error(),
			)

			writeJSON(w, statusCode, map[string]string{
				"error":  fmt.Sprintf("unauthorized: %s", err.Error()),
				"reason": diagReason,
			})
			return
		}

		ctx := context.WithValue(r.Context(), "agent_session", sess)
		ctx = context.WithValue(ctx, "agent_token", token)
		ctx = context.WithValue(ctx, "agent_source_id", sourceUUID)
		next(w, r.WithContext(ctx))
	}
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

// handleSelectFolder opens the native OS directory chooser dialog
func (s *AgentServer) handleSelectFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Title string `json:"title"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	selectedPath, err := s.picker.PickFolder(r.Context(), req.Title)
	if err != nil {
		if errors.Is(err, ErrPickerCancelled) {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"cancelled": true,
			})
			return
		}
		if errors.Is(err, ErrPickerBusy) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "folder picker is already in progress",
			})
			return
		}
		http.Error(w, fmt.Sprintf("Failed to open native folder picker: %v", err), http.StatusInternalServerError)
		return
	}

	token, _ := r.Context().Value("agent_token").(string)
	session, err := s.registerDirectory(selectedPath, token)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to inspect selected directory: %v", err), http.StatusBadRequest)
		return
	}

	s.renderSessionResponse(w, session)
}

// handleSelectPath directly validates and analyzes a path (e.g. for automated CLI/tests)
func (s *AgentServer) handleSelectPath(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Path) == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}

	token, _ := r.Context().Value("agent_token").(string)
	session, err := s.registerDirectory(req.Path, token)
	if err != nil {
		http.Error(w, fmt.Sprintf("Path validation failed: %v", err), http.StatusBadRequest)
		return
	}

	s.renderSessionResponse(w, session)
}

func (s *AgentServer) notifyBackendSession(token string, sourceID uuid.UUID, folderName string) {
	if s.backendURL == "" {
		return
	}
	validateURL := fmt.Sprintf("%s/api/sources/agent/session/validate", strings.TrimRight(s.backendURL, "/"))
	payload, _ := json.Marshal(map[string]string{
		"token":       token,
		"source_id":   sourceID.String(),
		"agent_id":    s.agentID,
		"folder_name": folderName,
	})
	req, err := http.NewRequest(http.MethodPost, validateURL, bytes.NewReader(payload))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			slog.Warn("failed to notify backend session binding", "error", err, "backend", s.backendURL)
		} else {
			resp.Body.Close()
		}
	}
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

func (s *AgentServer) registerDirectory(rawPath, token string) (*LocalSourceSession, error) {
	canonicalPath, err := s.pathValidator.ValidateSourcePath(rawPath)
	if err != nil {
		return nil, err
	}

	sourceID := uuid.New()
	folderName := filepath.Base(canonicalPath)
	if folderName == "" || folderName == "." || folderName == "/" {
		folderName = "local-project"
	}

	// Avoid repeatedly scanning the same repository if a recent completed analysis exists
	s.mu.RLock()
	var cachedAnalysis *analyzer.AnalysisResult
	for _, sess := range s.sessions {
		if sess.CanonicalPath == canonicalPath && (sess.Status == "ready" || sess.Status == "consumed") && sess.Analysis != nil && time.Since(sess.UpdatedAt) < 10*time.Minute {
			cachedAnalysis = sess.Analysis
			break
		}
	}
	s.mu.RUnlock()

	if cachedAnalysis != nil {
		session := &LocalSourceSession{
			SourceID:      sourceID,
			CanonicalPath: canonicalPath,
			FolderName:    folderName,
			Status:        "ready",
			Phase:         "ready",
			TotalFiles:    cachedAnalysis.TotalFiles,
			TotalBytes:    cachedAnalysis.TotalBytes,
			DetectedCount: len(cachedAnalysis.Services),
			Analysis:      cachedAnalysis,
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
			ExpiresAt:     time.Now().Add(s.sessionTTL),
			Token:         token,
		}
		s.mu.Lock()
		s.sessions[sourceID] = session
		s.mu.Unlock()
		RegisterLocalSourceSession(session)

		if token != "" {
			if sm := GetGlobalSessionManager(); sm != nil {
				_, _ = sm.BindSource(token, sourceID, folderName, s.agentID)
			}
			s.notifyBackendSession(token, sourceID, folderName)
		}
		return session, nil
	}

	session := &LocalSourceSession{
		SourceID:      sourceID,
		CanonicalPath: canonicalPath,
		FolderName:    folderName,
		Status:        "created",
		Phase:         "scanning",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		ExpiresAt:     time.Now().Add(s.sessionTTL),
		Token:         token,
	}

	s.mu.Lock()
	s.sessions[sourceID] = session
	s.mu.Unlock()
	RegisterLocalSourceSession(session)

	// Bind source and folder on backend session
	if token != "" {
		if sm := GetGlobalSessionManager(); sm != nil {
			_, _ = sm.BindSource(token, sourceID, folderName, s.agentID)
		}
		s.notifyBackendSession(token, sourceID, folderName)
	}

	// Channel to signal quick completion
	doneCh := make(chan struct{})

	go func() {
		analysis, err := analyzer.AnalyzeRepositoryWithProgress(canonicalPath, func(phase string, filesScanned, totalFiles, detectedCount int) {
			session.mu.Lock()
			session.Phase = phase
			if phase == "scanning" || phase == "detecting" {
				session.Status = phase
			}
			session.FilesScanned = filesScanned
			if totalFiles > 0 {
				session.TotalFiles = totalFiles
			}
			session.DetectedCount = detectedCount
			session.UpdatedAt = time.Now()
			session.mu.Unlock()
		})

		session.mu.Lock()
		if err != nil {
			session.Status = "failed"
			session.Phase = "failed"
			session.Error = err.Error()
			slog.Error("local agent analysis failed", "source_id", sourceID, "error", err)
		} else {
			session.Status = "ready"
			session.Phase = "ready"
			session.TotalFiles = analysis.TotalFiles
			session.TotalBytes = analysis.TotalBytes
			session.DetectedCount = len(analysis.Services)
			session.Analysis = analysis
			slog.Info("registered local source session",
				"source_id", sourceID.String(),
				"folder_name", folderName,
				"services_count", len(analysis.Services),
				"total_files", analysis.TotalFiles,
			)
		}
		session.UpdatedAt = time.Now()
		session.mu.Unlock()
		close(doneCh)
	}()

	// Grace window: if analysis finishes quickly (< 120ms), return ready immediately;
	// otherwise return initial scanning session and let frontend poll
	select {
	case <-doneCh:
	case <-time.After(120 * time.Millisecond):
	}

	return session, nil
}

func (s *AgentServer) renderSessionResponse(w http.ResponseWriter, session *LocalSourceSession) {
	session.mu.RLock()
	defer session.mu.RUnlock()

	totalFiles := session.TotalFiles
	totalBytes := session.TotalBytes
	var services []analyzer.ServiceDefinition
	if session.Analysis != nil {
		totalFiles = session.Analysis.TotalFiles
		totalBytes = session.Analysis.TotalBytes
		services = session.Analysis.Services
	}
	if services == nil {
		services = []analyzer.ServiceDefinition{}
	}

	status := session.Status
	phase := session.Phase
	if status != "consumed" {
		if session.Analysis != nil || len(services) > 0 {
			status = "ready"
			phase = "ready"
		}
		if status == "" {
			if session.Error != "" {
				status = "failed"
				phase = "failed"
			} else {
				status = "scanning"
				phase = "scanning"
			}
		}
	}

	resp := map[string]interface{}{
		"source": map[string]interface{}{
			"id":               session.SourceID.String(),
			"source_type":      "local_agent",
			"source_reference": session.SourceID.String(),
			"agent_id":         s.agentID,
			"folder_name":      session.FolderName,
			"status":           status,
			"phase":            phase,
			"total_files":      totalFiles,
			"total_bytes":      totalBytes,
		},
		"source_id":      session.SourceID.String(),
		"agent_id":       s.agentID,
		"folder_name":    session.FolderName,
		"status":         status,
		"phase":          phase,
		"files_scanned":  session.FilesScanned,
		"total_files":    totalFiles,
		"total_bytes":    totalBytes,
		"detected_count": session.DetectedCount,
		"services":       services,
		"registered_at":  session.CreatedAt.Format(time.RFC3339),
	}

	if session.Error != "" {
		resp["error"] = session.Error
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *AgentServer) handleSourcesRoutes(w http.ResponseWriter, r *http.Request) {
	// Subpaths:
	// GET /api/agent/sources/:source_id
	// DELETE /api/agent/sources/:source_id
	// POST /api/agent/sources/:source_id/analyze
	// GET /api/agent/sources/:source_id/stream-context
	path := strings.TrimPrefix(r.URL.Path, "/api/agent/sources/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "source_id is required", http.StatusBadRequest)
		return
	}

	sourceUUID, err := uuid.Parse(parts[0])
	if err != nil {
		http.Error(w, "invalid source UUID format", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	session, exists := s.sessions[sourceUUID]
	s.mu.RUnlock()

	if !exists {
		slog.Warn("agent source session not found in local memory",
			"source_id", sourceUUID.String(),
			"agent_id", s.agentID,
		)
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "source session not found or expired",
			"reason": "session not found",
		})
		return
	}

	if time.Now().After(session.ExpiresAt) {
		slog.Warn("agent source session expired in local memory",
			"source_id", sourceUUID.String(),
			"agent_id", s.agentID,
		)
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "source session not found or expired",
			"reason": "token expired",
		})
		return
	}

	// Verify token match
	token, _ := r.Context().Value("agent_token").(string)
	if session.Token != "" && token != "" && session.Token != token {
		slog.Warn("agent source session token mismatch",
			"source_id", sourceUUID.String(),
			"agent_id", s.agentID,
		)
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "forbidden: token does not match source session",
			"reason": "source mismatch",
		})
		return
	}

	if len(parts) == 1 {
		if r.Method == http.MethodGet {
			s.renderSessionResponse(w, session)
			return
		}
		if r.Method == http.MethodDelete {
			s.mu.Lock()
			delete(s.sessions, sourceUUID)
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]string{"message": "session removed"})
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	action := parts[1]
	switch action {
	case "consume":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.mu.Lock()
		session.Status = "consumed"
		session.Phase = "consumed"
		session.UpdatedAt = time.Now()
		s.mu.Unlock()
		s.renderSessionResponse(w, session)

	case "analyze":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		analysis, err := analyzer.AnalyzeRepository(session.CanonicalPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("re-analysis failed: %v", err), http.StatusInternalServerError)
			return
		}
		s.mu.Lock()
		session.Analysis = analysis
		session.UpdatedAt = time.Now()
		s.mu.Unlock()
		s.renderSessionResponse(w, session)

	case "environment", "env":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleSourceEnvironment(w, r, session)

	case "stream-context":
		// Streams tarball of a service's source directory (respects .dockerignore)
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		serviceRelPath := r.URL.Query().Get("service_path")
		cleanRelPath, pathErr := security.ValidateRelativeServicePath(serviceRelPath)
		if pathErr != nil {
			http.Error(w, fmt.Sprintf("invalid service path traversal: %v", pathErr), http.StatusForbidden)
			return
		}

		serviceDir := filepath.Join(session.CanonicalPath, filepath.FromSlash(cleanRelPath))
		evalServiceDir, err := filepath.EvalSymlinks(serviceDir)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid service directory: %v", err), http.StatusBadRequest)
			return
		}
		relCheck, err := filepath.Rel(session.CanonicalPath, evalServiceDir)
		if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
			http.Error(w, "invalid service path traversal", http.StatusForbidden)
			return
		}

		fi, err := os.Stat(evalServiceDir)
		if err != nil {
			http.Error(w, fmt.Sprintf("service directory not accessible: %v", err), http.StatusBadRequest)
			return
		}
		if !fi.IsDir() {
			http.Error(w, "service path is not a directory", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("Transfer-Encoding", "chunked")

		tarWriter := tar.NewWriter(w)
		defer tarWriter.Close()

		matcher, _ := dockerignore.LoadDockerignore(evalServiceDir)

		walkErr := filepath.WalkDir(evalServiceDir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(evalServiceDir, p)
			if err != nil || rel == "." || rel == "" {
				return nil
			}

			// Boundary check: symlinks must not escape evalServiceDir
			if d.Type()&os.ModeSymlink != 0 {
				target, evalErr := filepath.EvalSymlinks(p)
				if evalErr != nil {
					return nil // Skip unresolvable broken symlinks safely
				}
				targetRel, err := filepath.Rel(evalServiceDir, target)
				if err != nil || targetRel == ".." || strings.HasPrefix(targetRel, ".."+string(filepath.Separator)) {
					return fmt.Errorf("symlink %s escapes service directory boundary", p)
				}
			}

			slashRel := filepath.ToSlash(rel)
			isDir := d.IsDir()

			if analyzer.IsPrunedDir(d.Name()) || analyzer.IsSecretFile(d.Name()) {
				if isDir {
					return filepath.SkipDir
				}
				return nil
			}

			if matcher != nil {
				if isDir && matcher.CanSkipDir(slashRel) {
					return filepath.SkipDir
				}
				if matcher.Matches(slashRel, isDir) {
					if isDir {
						return filepath.SkipDir
					}
					return nil
				}
			}

			info, err := d.Info()
			if err != nil {
				return err
			}

			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			header.Name = slashRel
			if isDir {
				header.Name += "/"
			}

			if err := tarWriter.WriteHeader(header); err != nil {
				return err
			}

			if !isDir && info.Mode().IsRegular() {
				f, err := os.Open(p)
				if err != nil {
					return err
				}
				defer f.Close()
				if _, err := io.Copy(tarWriter, f); err != nil {
					return err
				}
			}
			return nil
		})
		if walkErr != nil {
			slog.Error("error during stream-context WalkDir", "source_id", session.SourceID, "error", walkErr)
			return
		}

		// If runtime is specified for auto build, inject virtual Dockerfile.forgelab into the stream
		genRuntime := r.URL.Query().Get("runtime")
		if genRuntime != "" {
			genPort := 8080
			if p, err := strconv.Atoi(r.URL.Query().Get("port")); err == nil && p > 0 {
				genPort = p
			}
			genStartCmd := r.URL.Query().Get("start_cmd")
			dockerfileContent := detector.GenerateDockerfile(genRuntime, genPort, genStartCmd)

			header := &tar.Header{
				Name:    "Dockerfile.forgelab",
				Mode:    0644,
				Size:    int64(len(dockerfileContent)),
				ModTime: time.Now(),
			}
			if err := tarWriter.WriteHeader(header); err == nil {
				_, _ = tarWriter.Write([]byte(dockerfileContent))
			}
		}

	default:
		http.Error(w, "unknown action", http.StatusNotFound)
	}
}

// SourceEnvironmentResponse represents the parsed .env variables returned to the authenticated backend.
type SourceEnvironmentResponse struct {
	SourceID uuid.UUID                    `json:"source_id"`
	Root     map[string]string            `json:"root"`
	Services map[string]map[string]string `json:"services"`
}

func (s *AgentServer) handleSourceEnvironment(w http.ResponseWriter, r *http.Request, session *LocalSourceSession) {
	resp := SourceEnvironmentResponse{
		SourceID: session.SourceID,
		Root:     make(map[string]string),
		Services: make(map[string]map[string]string),
	}

	// 1. Root .env
	rootEnvPath := filepath.Join(session.CanonicalPath, ".env")
	if fi, err := os.Stat(rootEnvPath); err == nil && !fi.IsDir() {
		if rootVars, err := envparser.ParseFile(rootEnvPath); err == nil {
			resp.Root = rootVars
		} else {
			slog.Warn("error parsing root .env", "source_id", session.SourceID, "error", err)
		}
	}

	// 2. Service-specific .env
	servicePaths := r.URL.Query()["service_path"]
	if len(servicePaths) == 0 {
		if sp := r.URL.Query().Get("service_paths"); sp != "" {
			servicePaths = strings.Split(sp, ",")
		} else if sp := r.URL.Query().Get("services"); sp != "" {
			servicePaths = strings.Split(sp, ",")
		}
	}

	if len(servicePaths) > 0 {
		for _, sp := range servicePaths {
			trimmed := strings.TrimSpace(sp)
			if trimmed == "" || trimmed == "." {
				continue
			}
			cleanRel, err := security.ValidateRelativeServicePath(trimmed)
			if err != nil {
				continue
			}
			svcDir := filepath.Join(session.CanonicalPath, filepath.FromSlash(cleanRel))
			evalSvcDir, err := filepath.EvalSymlinks(svcDir)
			if err != nil {
				continue
			}
			relCheck, err := filepath.Rel(session.CanonicalPath, evalSvcDir)
			if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
				continue
			}

			svcEnvPath := filepath.Join(evalSvcDir, ".env")
			if fi, err := os.Stat(svcEnvPath); err == nil && !fi.IsDir() {
				if svcVars, err := envparser.ParseFile(svcEnvPath); err == nil && len(svcVars) > 0 {
					resp.Services[cleanRel] = svcVars
				}
			}
		}
	} else {
		// Discover immediate subdirectories under root for .env
		entries, err := os.ReadDir(session.CanonicalPath)
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() || analyzer.IsPrunedDir(entry.Name()) {
					continue
				}
				subDir := filepath.Join(session.CanonicalPath, entry.Name())
				evalSubDir, err := filepath.EvalSymlinks(subDir)
				if err != nil {
					continue
				}
				relCheck, err := filepath.Rel(session.CanonicalPath, evalSubDir)
				if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
					continue
				}
				subEnvPath := filepath.Join(evalSubDir, ".env")
				if fi, err := os.Stat(subEnvPath); err == nil && !fi.IsDir() {
					if subVars, err := envparser.ParseFile(subEnvPath); err == nil && len(subVars) > 0 {
						resp.Services[entry.Name()] = subVars
					}
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
