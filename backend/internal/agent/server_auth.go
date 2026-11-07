package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

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

// handleVerifyIdentity allows the backend to verify the agent's identity without exposing credentials.
func (s *AgentServer) handleVerifyIdentity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		AgentID string `json:"agent_id"`
		Nonce   string `json:"nonce,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"verified": false,
			"error":    "invalid JSON payload",
		})
		return
	}

	reqID := strings.TrimSpace(req.AgentID)
	if reqID == "" || reqID != s.agentID {
		writeJSON(w, http.StatusForbidden, map[string]interface{}{
			"verified": false,
			"error":    "agent ID mismatch",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"verified": true,
		"agent_id": s.agentID,
		"version":  s.version,
		"commit":   s.commitSHA,
	})
}
