package websocket

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/middleware"
	"github.com/forgelab/backend/internal/models"
)

// CreateTicket generates a short-lived, single-use ticket for WebSocket authentication.
func (h *Hub) CreateTicket(userID uuid.UUID, userEmail string, ttl time.Duration) (string, time.Time, error) {
	if ttl == 0 {
		ttl = 60 * time.Second
	}
	expiresAt := time.Now().Add(ttl)
	ticketID := uuid.New().String()

	info := TicketInfo{
		UserID:    userID,
		UserEmail: userEmail,
		ExpiresAt: expiresAt,
	}

	if h.redisClient != nil {
		data, err := json.Marshal(info)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = h.redisClient.Set(ctx, "forgelab:ws_ticket:"+ticketID, data, ttl).Err()
		}
	}

	h.ticketMu.Lock()
	now := time.Now()
	for k, v := range h.tickets {
		if now.After(v.ExpiresAt) {
			delete(h.tickets, k)
		}
	}
	h.tickets[ticketID] = info
	h.ticketMu.Unlock()

	return ticketID, expiresAt, nil
}

// ConsumeTicket validates and consumes a single-use WebSocket ticket.
func (h *Hub) ConsumeTicket(ticketID string) (*TicketInfo, bool) {
	if ticketID == "" {
		return nil, false
	}

	if h.redisClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		key := "forgelab:ws_ticket:" + ticketID
		val, err := h.redisClient.GetDel(ctx, key).Result()
		if err == nil && val != "" {
			var info TicketInfo
			if json.Unmarshal([]byte(val), &info) == nil {
				if time.Now().Before(info.ExpiresAt) {
					h.ticketMu.Lock()
					delete(h.tickets, ticketID)
					h.ticketMu.Unlock()
					return &info, true
				}
			}
		}
	}

	h.ticketMu.Lock()
	defer h.ticketMu.Unlock()
	info, exists := h.tickets[ticketID]
	if !exists {
		return nil, false
	}
	delete(h.tickets, ticketID)
	if time.Now().After(info.ExpiresAt) {
		return nil, false
	}
	return &info, true
}

// CreateTicketHandler handles POST /api/ws/ticket.
func (h *Hub) CreateTicketHandler(w http.ResponseWriter, r *http.Request) {
	var (
		userID    uuid.UUID
		userEmail string
	)

	if uID, ok := middleware.GetUserID(r.Context()); ok && uID != uuid.Nil {
		userID = uID
		if middleware.IsGuestRequest(r.Context()) {
			userEmail = models.MakeGuestUserEmail(uID)
		} else {
			userEmail = "user@forgelab"
			if email, ok := middleware.GetUserEmail(r.Context()); ok && email != "" {
				userEmail = email
			}
		}
	} else {
		var tokenString string
		if cookie, err := r.Cookie("forgelab_access_token"); err == nil && cookie.Value != "" {
			tokenString = cookie.Value
		} else if authHeader := r.Header.Get("Authorization"); authHeader != "" {
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				tokenString = parts[1]
			}
		}

		if tokenString != "" {
			if h.jwtManager == nil {
				http.Error(w, `{"error": "unauthorized"}`, http.StatusUnauthorized)
				return
			}
			claims, err := h.jwtManager.ValidateAccessToken(tokenString)
			if err != nil {
				http.Error(w, `{"error": "invalid or expired token"}`, http.StatusUnauthorized)
				return
			}
			userID = claims.UserID
			userEmail = claims.Email
		} else {
			// If access token is absent but refresh token cookie is present, user is authenticated
			// but expired. Reject with 401 instead of downgrading to guest.
			if cookie, err := r.Cookie("forgelab_refresh_token"); err == nil && cookie.Value != "" {
				http.Error(w, `{"error": "access token expired or missing, refresh required"}`, http.StatusUnauthorized)
				return
			}

			h.mu.RLock()
			allowGuest := h.allowGuestMode
			h.mu.RUnlock()

			if !allowGuest {
				http.Error(w, `{"error": "unauthorized"}`, http.StatusUnauthorized)
				return
			}

			guestID := middleware.ResolveGuestSession(w, r)
			userID = guestID
			userEmail = models.MakeGuestUserEmail(guestID)
		}
	}

	if userID == uuid.Nil {
		http.Error(w, `{"error": "unauthorized"}`, http.StatusUnauthorized)
		return
	}

	ticket, expiresAt, err := h.CreateTicket(userID, userEmail, 60*time.Second)
	if err != nil {
		http.Error(w, `{"error": "failed to create ticket"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ticket":     ticket,
		"expires_at": expiresAt.Format(time.RFC3339),
	})
}
