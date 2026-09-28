package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"

	"github.com/forgelab/backend/internal/auth"
	"github.com/forgelab/backend/internal/models"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow development frontend origins
	},
}

type Client struct {
	hub           *Hub
	conn          *websocket.Conn
	userID        uuid.UUID
	userEmail     string
	subscriptions map[string]bool
	send          chan []byte
	mu            sync.RWMutex
}

type ClientMessage struct {
	Type    string `json:"type"`    // subscribe | unsubscribe | ping
	Channel string `json:"channel"` // deployment:<uuid> | project:<uuid>
}

type EventMessage struct {
	Type    string      `json:"type"`    // subscribed | error | log | status_change | project_event | pong
	Channel string      `json:"channel,omitempty"`
	Code    string      `json:"code,omitempty"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

// ProjectAuthorizer defines the interface required by the Hub for verifying project ownership.
type ProjectAuthorizer interface {
	GetProject(ctx context.Context, id, ownerID uuid.UUID) (*models.Project, error)
}

// DeploymentResolver defines the interface required by the Hub for resolving deployment metadata.
type DeploymentResolver interface {
	GetDeployment(ctx context.Context, id uuid.UUID) (*models.Deployment, error)
}

type Hub struct {
	clients           map[*Client]bool
	channels          map[string]map[*Client]bool
	register          chan *Client
	unregister        chan *Client
	mu                sync.RWMutex
	jwtManager        *auth.JWTManager
	projectService    ProjectAuthorizer
	deploymentService DeploymentResolver
	redisClient       *redis.Client
	ctx               context.Context
	cancel            context.CancelFunc
}

func NewHub(jwtManager *auth.JWTManager, projectService ProjectAuthorizer, deploymentService DeploymentResolver, redisClient *redis.Client) *Hub {
	ctx, cancel := context.WithCancel(context.Background())
	h := &Hub{
		clients:           make(map[*Client]bool),
		channels:          make(map[string]map[*Client]bool),
		register:          make(chan *Client),
		unregister:        make(chan *Client),
		jwtManager:        jwtManager,
		projectService:    projectService,
		deploymentService: deploymentService,
		redisClient:       redisClient,
		ctx:               ctx,
		cancel:            cancel,
	}

	go h.run()
	if redisClient != nil {
		go h.listenRedisPubSub()
	}
	return h
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
				for channel := range client.subscriptions {
					if clients, exists := h.channels[channel]; exists {
						delete(clients, client)
						if len(clients) == 0 {
							delete(h.channels, channel)
						}
					}
				}
				client.mu.Unlock()
				close(client.send)
				slog.Info("ws client unregistered", "user_id", client.userID)
			}
			h.mu.Unlock()
		}
	}
}

// broadcastLocally sends an event payload to all local connected clients subscribed to the channel.
func (h *Hub) broadcastLocally(channel string, payload []byte) {
	h.mu.RLock()
	subscribers, exists := h.channels[channel]
	if exists {
		for client := range subscribers {
			select {
			case client.send <- payload:
				slog.Debug("ws event delivered", "user_id", client.userID, "channel", channel)
			default:
				slog.Warn("ws client send buffer full, dropping event", "user_id", client.userID, "channel", channel)
			}
		}
	}
	h.mu.RUnlock()
}

// PublishEvent publishes a realtime event. In single- or multi-instance setups with Redis,
// Redis acts as the event distribution layer. When Redis is enabled, PublishEvent publishes
// to Redis and the Redis pub/sub listener delivers the event to local subscribers exactly once.
// If Redis is not configured, direct local delivery is used.
func (h *Hub) PublishEvent(channel string, event *EventMessage) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event payload: %w", err)
	}

	slog.Info("ws event published", "channel", channel, "event_type", event.Type)

	// 1. If Redis is configured, use it as the distribution layer
	if h.redisClient != nil {
		redisChan := "forgelab:pubsub:" + channel
		if err := h.redisClient.Publish(h.ctx, redisChan, payload).Err(); err != nil {
			slog.Error("redis publish error, falling back to direct local delivery", "channel", redisChan, "error", err)
			h.broadcastLocally(channel, payload)
			return fmt.Errorf("failed to publish event to redis: %w", err)
		}
		// Redis listener delivers to local subscribers — do NOT broadcast here to prevent double delivery
		return nil
	}

	// 2. Standalone / test fallback when Redis is nil: deliver directly to local subscribers
	h.broadcastLocally(channel, payload)
	return nil
}

func (h *Hub) listenRedisPubSub() {
	for {
		if h.ctx.Err() != nil {
			return
		}

		pubsub := h.redisClient.PSubscribe(h.ctx, "forgelab:pubsub:*")
		ch := pubsub.Channel()
		slog.Info("ws redis pubsub listener connected")

	readLoop:
		for {
			select {
			case <-h.ctx.Done():
				pubsub.Close()
				return
			case msg, ok := <-ch:
				if !ok {
					break readLoop
				}
				channel := strings.TrimPrefix(msg.Channel, "forgelab:pubsub:")
				h.broadcastLocally(channel, []byte(msg.Payload))
			}
		}

		pubsub.Close()
		if h.ctx.Err() != nil {
			return
		}
		slog.Warn("ws redis pubsub channel closed, reconnecting in 1s...")
		select {
		case <-h.ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
}

// ServeWS handles GET /api/ws endpoint.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	// Authenticate JWT token from query param or header or cookie
	tokenString := r.URL.Query().Get("token")
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
		if cookie, err := r.Cookie("forgelab_access_token"); err == nil {
			tokenString = cookie.Value
		}
	}

	if tokenString == "" {
		slog.Warn("ws authentication failure", "remote_addr", r.RemoteAddr, "reason", "token required")
		http.Error(w, "unauthorized: token required", http.StatusUnauthorized)
		return
	}

	claims, err := h.jwtManager.ValidateAccessToken(tokenString)
	if err != nil {
		slog.Warn("ws authentication failure", "remote_addr", r.RemoteAddr, "error", err, "reason", "invalid token")
		http.Error(w, "unauthorized: invalid token", http.StatusUnauthorized)
		return
	}

	slog.Info("ws authentication success", "user_id", claims.UserID)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("ws upgrade failed", "error", err, "remote_addr", r.RemoteAddr)
		return
	}

	client := &Client{
		hub:           h,
		conn:          conn,
		userID:        claims.UserID,
		userEmail:     claims.Email,
		subscriptions: make(map[string]bool),
		send:          make(chan []byte, 256),
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
			c.handleSubscribe(req.Channel)

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
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			// Write primary message as an individual WebSocket TextMessage
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				slog.Warn("ws write failure", "user_id", c.userID, "error", err)
				return
			}

			// Drain queued messages, writing EACH as an individual WebSocket TextMessage frame
			n := len(c.send)
			for i := 0; i < n; i++ {
				msg, ok := <-c.send
				if !ok {
					_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
					return
				}
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

func (c *Client) handleSubscribe(channel string) {
	slog.Info("ws subscription requested", "user_id", c.userID, "channel", channel)

	if channel == "" {
		slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "channel is required")
		c.sendError("INVALID_CHANNEL", "channel is required")
		return
	}

	parts := strings.SplitN(channel, ":", 2)
	if len(parts) != 2 {
		slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "invalid channel format")
		c.sendError("INVALID_CHANNEL", "invalid channel format. Expected project:<uuid> or deployment:<uuid>")
		return
	}

	channelType, resourceIDStr := parts[0], parts[1]
	resourceID, err := uuid.Parse(resourceIDStr)
	if err != nil {
		slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "invalid resource UUID")
		c.sendError("INVALID_CHANNEL", "invalid resource UUID")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Perform server-side authorization check
	var projectID uuid.UUID
	if channelType == "project" {
		projectID = resourceID
	} else if channelType == "deployment" {
		if c.hub.deploymentService == nil {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "deployment service unavailable")
			c.sendError("SERVICE_UNAVAILABLE", "deployment service unavailable")
			return
		}
		deployment, err := c.hub.deploymentService.GetDeployment(ctx, resourceID)
		if err != nil {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "deployment not found or access denied")
			c.sendError("UNAUTHORIZED", "deployment not found or access denied")
			return
		}
		projectID = deployment.ProjectID
	} else {
		slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "unknown channel type")
		c.sendError("INVALID_CHANNEL", "unknown channel type")
		return
	}

	// Check if user owns the project
	if c.hub.projectService == nil {
		slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "project service unavailable")
		c.sendError("SERVICE_UNAVAILABLE", "project service unavailable")
		return
	}
	_, err = c.hub.projectService.GetProject(ctx, projectID, c.userID)
	if err != nil {
		slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "project access denied")
		c.sendError("UNAUTHORIZED", "access denied to this resource")
		return
	}

	// Authorization granted! Add to channel subscribers
	c.hub.mu.Lock()
	if c.hub.channels[channel] == nil {
		c.hub.channels[channel] = make(map[*Client]bool)
	}
	c.hub.channels[channel][c] = true
	c.hub.mu.Unlock()

	c.mu.Lock()
	c.subscriptions[channel] = true
	c.mu.Unlock()

	slog.Info("ws subscription authorized", "user_id", c.userID, "channel", channel, "project_id", projectID)
	c.sendEvent(&EventMessage{
		Type:    "subscribed",
		Channel: channel,
	})
}

func (c *Client) handleUnsubscribe(channel string) {
	c.hub.mu.Lock()
	if clients, exists := c.hub.channels[channel]; exists {
		delete(clients, c)
		if len(clients) == 0 {
			delete(c.hub.channels, channel)
		}
	}
	c.hub.mu.Unlock()

	c.mu.Lock()
	delete(c.subscriptions, channel)
	c.mu.Unlock()

	slog.Info("ws client unsubscribed", "user_id", c.userID, "channel", channel)
}

func (c *Client) sendEvent(event *EventMessage) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	select {
	case c.send <- payload:
	default:
		slog.Warn("ws client send buffer full during direct event", "user_id", c.userID)
	}
}

func (c *Client) sendError(code, message string) {
	c.sendEvent(&EventMessage{
		Type:    "error",
		Code:    code,
		Message: message,
	})
}
