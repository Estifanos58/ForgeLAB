package agent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	ErrSessionNotFound = errors.New("agent session not found")
	ErrSessionExpired  = errors.New("agent session has expired")
	ErrSessionConsumed = errors.New("agent session has already been consumed")
	ErrUnauthorized    = errors.New("unauthorized agent session access")
	ErrInvalidToken    = errors.New("invalid agent session token")
)

const (
	DefaultSessionTTL = 30 * time.Minute
)

// AgentSession represents a short-lived authenticated link between a user, agent, and source
type AgentSession struct {
	ID         uuid.UUID  `json:"id"`
	Token      string     `json:"token"`
	UserID     uuid.UUID  `json:"user_id"`
	AgentID    string     `json:"agent_id"`
	SourceID   *uuid.UUID `json:"source_id,omitempty"`
	FolderName string     `json:"folder_name,omitempty"`
	SourcePath string     `json:"source_path,omitempty"`
	Consumed   bool       `json:"consumed"`
	ExpiresAt  time.Time  `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

// IsExpired checks whether the session TTL has elapsed
func (s *AgentSession) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}

// Snapshot returns an immutable, detached copy of the session
func (s *AgentSession) Snapshot() *AgentSession {
	if s == nil {
		return nil
	}
	snap := *s
	if s.SourceID != nil {
		id := *s.SourceID
		snap.SourceID = &id
	}
	return &snap
}

// SessionManager manages active agent sessions in a thread-safe manner
type SessionManager struct {
	mu          sync.RWMutex
	sessions    map[uuid.UUID]*AgentSession
	tokens      map[string]uuid.UUID // token -> session_id
	stopCleanup chan struct{}
}

var (
	globalSessionManager *SessionManager
	sessionManagerOnce   sync.Once
)

// GetGlobalSessionManager returns the singleton session manager
func GetGlobalSessionManager() *SessionManager {
	sessionManagerOnce.Do(func() {
		globalSessionManager = NewSessionManager()
	})
	return globalSessionManager
}

// NewSessionManager creates and starts a new session manager
func NewSessionManager() *SessionManager {
	sm := &SessionManager{
		sessions:    make(map[uuid.UUID]*AgentSession),
		tokens:      make(map[string]uuid.UUID),
		stopCleanup: make(chan struct{}),
	}
	go sm.cleanupLoop(5 * time.Minute)
	return sm
}

// Stop terminates the background cleanup routine
func (sm *SessionManager) Stop() {
	close(sm.stopCleanup)
}

func (sm *SessionManager) cleanupLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-sm.stopCleanup:
			return
		case <-ticker.C:
			sm.CleanupExpired()
		}
	}
}

// CleanupExpired deletes expired sessions from memory
func (sm *SessionManager) CleanupExpired() int {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	removed := 0
	for id, s := range sm.sessions {
		if now.After(s.ExpiresAt) {
			delete(sm.tokens, s.Token)
			delete(sm.sessions, id)
			removed++
		}
	}
	return removed
}

// ExpireSession marks a session identified by token as expired immediately (useful for testing and revocation)
func (sm *SessionManager) ExpireSession(token string) bool {
	cleanToken := strings.TrimSpace(token)
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if id, ok := sm.tokens[cleanToken]; ok {
		if s, exists := sm.sessions[id]; exists {
			s.ExpiresAt = time.Now().Add(-1 * time.Minute)
			return true
		}
	}
	return false
}

// CreateSession generates a new cryptographically secure token and session for a user and agent
func (sm *SessionManager) CreateSession(userID uuid.UUID, agentID string, ttl time.Duration) (*AgentSession, error) {
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("failed to generate secure token: %w", err)
	}
	token := hex.EncodeToString(b)
	sessionID := uuid.New()

	session := &AgentSession{
		ID:        sessionID,
		Token:     token,
		UserID:    userID,
		AgentID:   strings.TrimSpace(agentID),
		ExpiresAt: time.Now().Add(ttl),
		CreatedAt: time.Now(),
	}

	sm.mu.Lock()
	sm.sessions[sessionID] = session
	sm.tokens[token] = sessionID
	sm.mu.Unlock()

	return session.Snapshot(), nil
}

// ValidateToken looks up a session by token and verifies it is not expired
func (sm *SessionManager) ValidateToken(token string) (*AgentSession, error) {
	cleanToken := strings.TrimSpace(token)
	if cleanToken == "" {
		return nil, ErrInvalidToken
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	sessionID, ok := sm.tokens[cleanToken]
	if !ok {
		return nil, ErrSessionNotFound
	}
	session, exists := sm.sessions[sessionID]
	if !exists {
		return nil, ErrSessionNotFound
	}
	if session.IsExpired() {
		return nil, ErrSessionExpired
	}
	if session.Consumed {
		return nil, ErrSessionConsumed
	}

	return session.Snapshot(), nil
}

// BindSource associates an authenticated session with a source ID and folder name
func (sm *SessionManager) BindSource(token string, sourceID uuid.UUID, folderName string, agentID string) (*AgentSession, error) {
	return sm.BindSourceWithPath(token, sourceID, folderName, agentID, "")
}

// BindSourceWithPath associates an authenticated session with a source ID, folder name, and optional source path
func (sm *SessionManager) BindSourceWithPath(token string, sourceID uuid.UUID, folderName string, agentID string, sourcePath string) (*AgentSession, error) {
	cleanToken := strings.TrimSpace(token)
	if cleanToken == "" {
		return nil, ErrInvalidToken
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	sessionID, ok := sm.tokens[cleanToken]
	if !ok {
		return nil, ErrSessionNotFound
	}
	session, exists := sm.sessions[sessionID]
	if !exists {
		return nil, ErrSessionNotFound
	}
	if session.IsExpired() {
		return nil, ErrSessionExpired
	}
	if session.Consumed {
		return nil, ErrSessionConsumed
	}

	// Do not allow re-binding to a different source
	if session.SourceID != nil && *session.SourceID != sourceID {
		return nil, fmt.Errorf("%w: session already bound to source %s", ErrUnauthorized, session.SourceID.String())
	}

	// If agentID was not specified during session creation, bind it now; otherwise verify match
	cleanAgentID := strings.TrimSpace(agentID)
	if session.AgentID != "" && (cleanAgentID == "" || session.AgentID != cleanAgentID) {
		return nil, fmt.Errorf("%w: agent ID mismatch", ErrUnauthorized)
	}
	if session.AgentID == "" && cleanAgentID != "" {
		session.AgentID = cleanAgentID
	}

	session.SourceID = &sourceID
	session.FolderName = folderName
	if strings.TrimSpace(sourcePath) != "" {
		session.SourcePath = strings.TrimSpace(sourcePath)
	}
	return session.Snapshot(), nil
}

// VerifyTokenForSource validates that a session token is authorized for a specific bound source.
// It allows a consumed session ONLY when:
// 1. token matches,
// 2. session is not expired,
// 3. session is already bound to exactly that sourceID,
// 4. agent ID matches.
// A consumed token cannot access or bind to any other source.
func (sm *SessionManager) VerifyTokenForSource(token string, sourceID uuid.UUID, agentID string) (*AgentSession, error) {
	cleanToken := strings.TrimSpace(token)
	if cleanToken == "" {
		return nil, ErrInvalidToken
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	sessionID, ok := sm.tokens[cleanToken]
	if !ok {
		return nil, ErrSessionNotFound
	}
	session, exists := sm.sessions[sessionID]
	if !exists {
		return nil, ErrSessionNotFound
	}

	if session.IsExpired() {
		return nil, ErrSessionExpired
	}

	cleanAgentID := strings.TrimSpace(agentID)
	if session.AgentID != "" && (cleanAgentID == "" || session.AgentID != cleanAgentID) {
		return nil, fmt.Errorf("%w: agent ID mismatch", ErrUnauthorized)
	}

	if session.SourceID == nil || *session.SourceID != sourceID {
		if session.Consumed {
			return nil, ErrSessionConsumed
		}
		return nil, fmt.Errorf("%w: session not bound to requested source", ErrUnauthorized)
	}

	return session.Snapshot(), nil
}

// VerifyForRegistration checks that user owns the session, it is unexpired, and source/agent match
func (sm *SessionManager) VerifyForRegistration(userID uuid.UUID, sessionID uuid.UUID, token string) (*AgentSession, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, exists := sm.sessions[sessionID]
	if !exists {
		return nil, ErrSessionNotFound
	}

	if session.IsExpired() {
		return nil, ErrSessionExpired
	}

	if session.Consumed {
		return nil, ErrSessionConsumed
	}

	if session.UserID != userID {
		return nil, ErrUnauthorized
	}

	if token != "" && session.Token != token {
		return nil, ErrInvalidToken
	}

	if session.SourceID == nil {
		return nil, errors.New("session has not been bound to a verified local source")
	}

	return session.Snapshot(), nil
}

// MarkConsumed flags a session as consumed so it cannot be reused
func (sm *SessionManager) MarkConsumed(sessionID uuid.UUID) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, exists := sm.sessions[sessionID]
	if !exists {
		return ErrSessionNotFound
	}
	session.Consumed = true
	return nil
}

// FindSessionBySourceID finds an active session by source ID
func (sm *SessionManager) FindSessionBySourceID(sourceID uuid.UUID) (*AgentSession, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for _, s := range sm.sessions {
		if s.SourceID != nil && *s.SourceID == sourceID {
			if s.IsExpired() {
				return nil, ErrSessionExpired
			}
			return s.Snapshot(), nil
		}
	}
	return nil, ErrSessionNotFound
}
