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
	ExpiresAt  time.Time  `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

// IsExpired checks whether the session TTL has elapsed
func (s *AgentSession) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}

// SessionManager manages active agent sessions in a thread-safe manner
type SessionManager struct {
	mu           sync.RWMutex
	sessions     map[uuid.UUID]*AgentSession
	tokens       map[string]uuid.UUID // token -> session_id
	stopCleanup  chan struct{}
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

	return session, nil
}

// ValidateToken looks up a session by token and verifies it is not expired
func (sm *SessionManager) ValidateToken(token string) (*AgentSession, error) {
	cleanToken := strings.TrimSpace(token)
	if cleanToken == "" {
		return nil, ErrInvalidToken
	}

	sm.mu.RLock()
	sessionID, ok := sm.tokens[cleanToken]
	if !ok {
		sm.mu.RUnlock()
		return nil, ErrSessionNotFound
	}
	session, exists := sm.sessions[sessionID]
	sm.mu.RUnlock()

	if !exists {
		return nil, ErrSessionNotFound
	}
	if session.IsExpired() {
		return nil, ErrSessionExpired
	}

	return session, nil
}

// BindSource associates an authenticated session with a source ID and folder name
func (sm *SessionManager) BindSource(token string, sourceID uuid.UUID, folderName string, agentID string) (*AgentSession, error) {
	session, err := sm.ValidateToken(token)
	if err != nil {
		return nil, err
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	// If agentID was not specified during session creation, bind it now; otherwise verify match
	if session.AgentID != "" && agentID != "" && session.AgentID != agentID {
		return nil, fmt.Errorf("%w: agent ID mismatch", ErrUnauthorized)
	}
	if session.AgentID == "" && agentID != "" {
		session.AgentID = agentID
	}

	session.SourceID = &sourceID
	session.FolderName = folderName
	return session, nil
}

// VerifyForRegistration checks that user owns the session, it is unexpired, and source/agent match
func (sm *SessionManager) VerifyForRegistration(userID uuid.UUID, sessionID uuid.UUID, token string) (*AgentSession, error) {
	sm.mu.RLock()
	session, exists := sm.sessions[sessionID]
	sm.mu.RUnlock()

	if !exists {
		return nil, ErrSessionNotFound
	}

	if session.IsExpired() {
		return nil, ErrSessionExpired
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

	return session, nil
}
