package websocket

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"

	"github.com/forgelab/backend/internal/auth"
	"github.com/forgelab/backend/internal/middleware"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/ratelimit"
)

// TicketInfo stores single-use ticket claims and expiration.
type TicketInfo struct {
	UserID    uuid.UUID `json:"user_id"`
	UserEmail string    `json:"user_email"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Client struct {
	hub           *Hub
	conn          *websocket.Conn
	userID        uuid.UUID
	userEmail     string
	subscriptions map[string]bool
	send          chan []byte // preserved for mock/direct test channel inspection
	pending       [][]byte
	notify        chan struct{}
	closed        bool
	mu            sync.RWMutex
}

type ClientMessage struct {
	Type          string `json:"type"`                     // subscribe | unsubscribe | ping
	Channel       string `json:"channel"`                  // deployment:<uuid> | project:<uuid>
	LastSequence  int64  `json:"last_sequence,omitempty"`  // replay logs after this sequence
	AfterSequence int64  `json:"after_sequence,omitempty"` // alias for last_sequence
}

type EventMessage struct {
	Type     string      `json:"type"` // subscribed | error | log | status_change | project_event | pong
	Channel  string      `json:"channel,omitempty"`
	Sequence int64       `json:"sequence,omitempty"` // reliable event sequence
	Code     string      `json:"code,omitempty"`
	Message  string      `json:"message,omitempty"`
	Data     interface{} `json:"data,omitempty"`
}

type RedisEnvelope struct {
	NodeID  string        `json:"node_id"`
	Channel string        `json:"channel"`
	Event   *EventMessage `json:"event"`
}

// ProjectAuthorizer defines the interface required by the Hub for verifying project ownership.
type ProjectAuthorizer interface {
	GetProject(ctx context.Context, id, ownerID uuid.UUID) (*models.Project, error)
}

// DeploymentResolver defines the interface required by the Hub for resolving deployment metadata and replay logs.
type DeploymentResolver interface {
	GetDeployment(ctx context.Context, id uuid.UUID) (*models.Deployment, error)
	GetDeploymentLogsAfter(ctx context.Context, deploymentID uuid.UUID, serviceID *uuid.UUID, afterID int64, limit int) ([]*models.DeploymentLog, error)
	GetProjectLogsAfter(ctx context.Context, projectID uuid.UUID, afterID int64, limit int) ([]*models.DeploymentLog, error)
}

// ServiceDeploymentResolver defines the interface for resolving service deployments and their logs for WS replay.
type ServiceDeploymentResolver interface {
	GetServiceDeployment(ctx context.Context, id uuid.UUID) (*models.ServiceDeployment, error)
	GetServiceDeploymentLogsAfter(ctx context.Context, serviceDeploymentID uuid.UUID, afterID int64, limit int) ([]*models.DeploymentLog, error)
}

// ServiceResolver defines the interface for resolving service metadata (service → project mapping).
type ServiceResolver interface {
	GetServiceByID(ctx context.Context, serviceID uuid.UUID) (*models.Service, error)
}

type Hub struct {
	nodeID                   string
	clients                  map[*Client]bool
	channels                 map[string]map[*Client]bool
	register                 chan *Client
	unregister               chan *Client
	mu                       sync.RWMutex
	jwtManager               *auth.JWTManager
	projectService           ProjectAuthorizer
	deploymentService        DeploymentResolver
	serviceDeploymentService ServiceDeploymentResolver
	serviceService           ServiceResolver
	redisClient              *redis.Client
	allowedOrigins           []string
	rateLimiter              *ratelimit.Limiter
	subscriptionLimit        int
	allowGuestMode           bool
	tickets                  map[string]TicketInfo
	ticketMu                 sync.Mutex
	ctx                      context.Context
	cancel                   context.CancelFunc
}

func NewHub(jwtManager *auth.JWTManager, projectService ProjectAuthorizer, deploymentService DeploymentResolver, redisClient *redis.Client, allowedOrigins ...string) *Hub {
	ctx, cancel := context.WithCancel(context.Background())
	h := &Hub{
		nodeID:            uuid.New().String(),
		clients:           make(map[*Client]bool),
		channels:          make(map[string]map[*Client]bool),
		register:          make(chan *Client),
		unregister:        make(chan *Client),
		jwtManager:        jwtManager,
		projectService:    projectService,
		deploymentService: deploymentService,
		redisClient:       redisClient,
		allowedOrigins:    allowedOrigins,
		allowGuestMode:    true,
		tickets:           make(map[string]TicketInfo),
		ctx:               ctx,
		cancel:            cancel,
	}

	go h.run()
	if redisClient != nil {
		go h.listenRedisPubSub()
	}
	return h
}

// SetAllowGuestMode sets whether anonymous connections can connect to WebSocket as guest.
func (h *Hub) SetAllowGuestMode(allow bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.allowGuestMode = allow
}

// SetRateLimiter configures rate limiting for subscription messages.
func (h *Hub) SetRateLimiter(limiter *ratelimit.Limiter, subscriptionLimit int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rateLimiter = limiter
	h.subscriptionLimit = subscriptionLimit
}

// SetServiceDeploymentResolver sets the resolver used for service-deployment channel authorization and replay.
func (h *Hub) SetServiceDeploymentResolver(resolver ServiceDeploymentResolver) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.serviceDeploymentService = resolver
}

// SetServiceResolver sets the resolver used for service → project mapping during authorization.
func (h *Hub) SetServiceResolver(resolver ServiceResolver) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.serviceService = resolver
}

// SetAllowedOrigins configures the allowed origin domains for WebSocket connections.
func (h *Hub) SetAllowedOrigins(origins []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.allowedOrigins = origins
}

// CheckOrigin validates the incoming HTTP request Origin header against allowed origins.
func (h *Hub) CheckOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // Direct non-browser clients or same-origin
	}
	origin = strings.TrimRight(strings.ToLower(origin), "/")

	// Same-origin check: if origin matches request Host
	host := strings.ToLower(r.Host)
	if origin == "http://"+host || origin == "https://"+host {
		return true
	}

	h.mu.RLock()
	origins := h.allowedOrigins
	h.mu.RUnlock()

	if len(origins) == 0 {
		// Strict default development allowlist
		if origin == "http://localhost:3000" || origin == "http://127.0.0.1:3000" ||
			origin == "http://localhost:8080" || origin == "http://127.0.0.1:8080" {
			return true
		}
		return false
	}

	for _, o := range origins {
		if strings.TrimRight(strings.ToLower(o), "/") == origin {
			return true
		}
	}
	return false
}

// NodeID returns the unique runtime identifier of this backend instance.
func (h *Hub) NodeID() string {
	return h.nodeID
}

// Close stops the Hub and background goroutines.
func (h *Hub) Close() {
	h.cancel()
}

func (h *Hub) run() {
	for {
		select {
		case <-h.ctx.Done():
			return
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			slog.Info("ws client registered", "user_id", client.userID)

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				// Remove client from all channel subscriptions
				client.mu.Lock()
				client.closed = true
				client.pending = nil
				for channel := range client.subscriptions {
					if clients, exists := h.channels[channel]; exists {
						delete(clients, client)
						if len(clients) == 0 {
							delete(h.channels, channel)
						}
					}
				}
				client.mu.Unlock()
				if client.send != nil {
					close(client.send)
				}
				select {
				case client.notify <- struct{}{}:
				default:
				}
				slog.Info("ws client unregistered", "user_id", client.userID)
			}
			h.mu.Unlock()
		}
	}
}

// enqueue queues a payload for writing to the client without dropping events on full buffer.
func (c *Client) enqueue(payload []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	// For mock test clients without a live WebSocket connection, push to c.send
	if c.conn == nil && c.send != nil {
		select {
		case c.send <- payload:
		default:
		}
	}
	// Safety ceiling of 50,000 pending messages prevents unbounded memory exhaustion if socket completely hangs
	if len(c.pending) > 50000 {
		c.closed = true
		if c.conn != nil {
			_ = c.conn.Close()
		}
		return false
	}
	c.pending = append(c.pending, payload)
	if c.notify != nil {
		select {
		case c.notify <- struct{}{}:
		default:
		}
	}
	return true
}

func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	// 1. Strictly enforce configured WebSocket origin policies on ALL connections
	if !h.CheckOrigin(r) {
		slog.Warn("ws connection rejected: untrusted origin", "origin", r.Header.Get("Origin"), "remote_addr", r.RemoteAddr)
		http.Error(w, "forbidden: untrusted origin", http.StatusForbidden)
		return
	}

	var (
		userID    uuid.UUID
		userEmail string
	)

	// 2. Single-use ticket authentication (?ticket=<ticketID>)
	if ticket := r.URL.Query().Get("ticket"); ticket != "" {
		info, ok := h.ConsumeTicket(ticket)
		if !ok {
			slog.Warn("ws connection rejected: invalid or expired ticket", "remote_addr", r.RemoteAddr)
			http.Error(w, "unauthorized: invalid or expired ticket", http.StatusUnauthorized)
			return
		}
		userID = info.UserID
		userEmail = info.UserEmail
	} else {
		// 3. Authenticate via secure Cookie, Authorization Bearer header, or deprecated query token fallback
		var tokenString string
		if cookie, err := r.Cookie("forgelab_access_token"); err == nil && cookie.Value != "" {
			tokenString = cookie.Value
		}
		if tokenString == "" {
			authHeader := r.Header.Get("Authorization")
			if authHeader != "" {
				parts := strings.SplitN(authHeader, " ", 2)
				if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
					tokenString = parts[1]
				}
			}
		}
		if tokenString == "" {
			if queryToken := r.URL.Query().Get("token"); queryToken != "" {
				slog.Warn("ws connection using deprecated query parameter token fallback; recommend upgrading to tickets or cookies", "remote_addr", r.RemoteAddr)
				tokenString = queryToken
			}
		}

		if tokenString != "" {
			if h.jwtManager == nil {
				http.Error(w, "unauthorized: auth manager unavailable", http.StatusUnauthorized)
				return
			}
			claims, err := h.jwtManager.ValidateAccessToken(tokenString)
			if err != nil {
				slog.Warn("ws authentication failure", "remote_addr", r.RemoteAddr, "error", err, "reason", "invalid token")
				http.Error(w, "unauthorized: invalid token", http.StatusUnauthorized)
				return
			}
			userID = claims.UserID
			userEmail = claims.Email
		} else {
			// If an access token is absent but a refresh token cookie is present, this client is an
			// authenticated user whose access token has expired (or was evicted by the browser).
			// We MUST NOT silently downgrade them to guest identity, as doing so causes legitimate
			// project subscriptions to fail with "UNAUTHORIZED: access denied to this resource".
			if cookie, err := r.Cookie("forgelab_refresh_token"); err == nil && cookie.Value != "" {
				slog.Warn("ws authentication failure: refresh token present but access token missing", "remote_addr", r.RemoteAddr)
				http.Error(w, "unauthorized: access token expired or missing, refresh required", http.StatusUnauthorized)
				return
			}

			h.mu.RLock()
			allowGuest := h.allowGuestMode
			h.mu.RUnlock()

			if !allowGuest {
				slog.Warn("ws authentication failure", "remote_addr", r.RemoteAddr, "reason", "token required")
				http.Error(w, "unauthorized: token required", http.StatusUnauthorized)
				return
			}

			guestID := middleware.ResolveGuestSession(w, r)
			userID = guestID
			userEmail = models.MakeGuestUserEmail(guestID)
		}
	}

	slog.Info("ws authentication success", "user_id", userID)

	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     h.CheckOrigin,
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("ws upgrade failed", "error", err, "remote_addr", r.RemoteAddr)
		return
	}

	client := &Client{
		hub:           h,
		conn:          conn,
		userID:        userID,
		userEmail:     userEmail,
		subscriptions: make(map[string]bool),
		send:          make(chan []byte, 256),
		pending:       make([][]byte, 0, 32),
		notify:        make(chan struct{}, 1),
	}

	h.register <- client
	slog.Info("ws connection accepted", "user_id", client.userID, "remote_addr", r.RemoteAddr)

	go client.writePump()
	go client.readPump()
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
		slog.Info("ws connection closed", "user_id", c.userID)
	}()

	c.conn.SetReadLimit(4096)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure, websocket.CloseNormalClosure) {
				slog.Warn("ws read failure", "user_id", c.userID, "error", err)
			}
			break
		}

		var req ClientMessage
		if err := json.Unmarshal(message, &req); err != nil {
			c.sendError("INVALID_MESSAGE", "invalid message payload")
			continue
		}

		switch req.Type {
		case "ping":
			c.sendEvent(&EventMessage{Type: "pong"})

		case "subscribe":
			lastSeq := req.LastSequence
			if lastSeq == 0 && req.AfterSequence > 0 {
				lastSeq = req.AfterSequence
			}
			c.handleSubscribe(req.Channel, lastSeq)

		case "unsubscribe":
			c.handleUnsubscribe(req.Channel)

		default:
			c.sendError("UNKNOWN_TYPE", "unknown message type")
		}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				slog.Warn("ws write failure", "user_id", c.userID, "error", err)
				return
			}

		case <-c.notify:
			c.mu.Lock()
			if c.closed || len(c.pending) == 0 {
				c.mu.Unlock()
				continue
			}
			messages := c.pending
			c.pending = nil
			c.mu.Unlock()

			for _, msg := range messages {
				c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					slog.Warn("ws write failure", "user_id", c.userID, "error", err)
					return
				}
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				slog.Warn("ws ping write failure", "user_id", c.userID, "error", err)
				return
			}
		}
	}
}
