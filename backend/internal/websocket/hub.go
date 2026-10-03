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
	"github.com/forgelab/backend/internal/ratelimit"
)

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
		ctx:               ctx,
		cancel:            cancel,
	}

	go h.run()
	if redisClient != nil {
		go h.listenRedisPubSub()
	}
	return h
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

// broadcastLocally sends an event payload to all local connected clients subscribed to the channel.
func (h *Hub) broadcastLocally(channel string, payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	subscribers, exists := h.channels[channel]
	if exists {
		for client := range subscribers {
			client.enqueue(payload)
		}
	}
}

// PublishEvent publishes a realtime event. Local WebSocket subscribers receive the event immediately
// without waiting for a Redis round trip. If Redis is configured, an envelope tagged with this node's
// nodeID is published to Redis for multi-instance distribution. Receiving nodes inspect nodeID and
// suppress duplicates originating from themselves.
func (h *Hub) PublishEvent(channel string, event *EventMessage) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event payload: %w", err)
	}

	// 1. Immediately deliver to local subscribers on this node
	h.broadcastLocally(channel, payload)
	slog.Info("ws event published locally", "channel", channel, "event_type", event.Type, "node_id", h.nodeID)

	// 2. If Redis is configured, forward to other backend nodes via Redis Pub/Sub
	if h.redisClient != nil {
		envelope := RedisEnvelope{
			NodeID:  h.nodeID,
			Channel: channel,
			Event:   event,
		}
		envPayload, err := json.Marshal(envelope)
		if err != nil {
			slog.Error("failed to marshal redis envelope", "channel", channel, "error", err)
			return fmt.Errorf("failed to marshal redis envelope: %w", err)
		}

		redisChan := "forgelab:pubsub:" + channel
		if err := h.redisClient.Publish(h.ctx, redisChan, envPayload).Err(); err != nil {
			slog.Error("redis publish error", "channel", redisChan, "error", err)
			// Return error so caller is informed, but local clients have already received the event
			return fmt.Errorf("failed to publish event to redis: %w", err)
		}
	}

	return nil
}

func (h *Hub) listenRedisPubSub() {
	backoff := 1 * time.Second
	for {
		if h.ctx.Err() != nil {
			return
		}

		pubsub := h.redisClient.PSubscribe(h.ctx, "forgelab:pubsub:*")

		// Verify subscription readiness
		_, err := pubsub.Receive(h.ctx)
		if err != nil {
			if h.ctx.Err() != nil {
				_ = pubsub.Close()
				return
			}
			slog.Error("failed to establish redis pubsub subscription", "error", err, "node_id", h.nodeID)
			_ = pubsub.Close()
			select {
			case <-h.ctx.Done():
				return
			case <-time.After(backoff):
				if backoff < 15*time.Second {
					backoff *= 2
				}
				continue
			}
		}

		backoff = 1 * time.Second
		slog.Info("ws redis pubsub listener connected successfully", "pattern", "forgelab:pubsub:*", "node_id", h.nodeID)
		ch := pubsub.Channel()

	readLoop:
		for {
			select {
			case <-h.ctx.Done():
				_ = pubsub.Close()
				return
			case msg, ok := <-ch:
				if !ok {
					slog.Warn("ws redis pubsub channel closed by server", "node_id", h.nodeID)
					break readLoop
				}

				// Unmarshal envelope
				var env RedisEnvelope
				if err := json.Unmarshal([]byte(msg.Payload), &env); err == nil && env.NodeID != "" {
					// Origin-node suppression: ignore copy published by this same node
					if env.NodeID == h.nodeID {
						slog.Debug("redis pubsub ignored event from self", "node_id", env.NodeID, "channel", env.Channel)
						continue
					}

					// Event originated from a remote node: broadcast to local subscribers
					eventPayload, err := json.Marshal(env.Event)
					if err == nil {
						h.broadcastLocally(env.Channel, eventPayload)
						slog.Debug("redis pubsub delivered event from remote node", "remote_node_id", env.NodeID, "channel", env.Channel)
					}
				} else {
					// Backward compatibility: raw EventMessage fallback
					channel := strings.TrimPrefix(msg.Channel, "forgelab:pubsub:")
					h.broadcastLocally(channel, []byte(msg.Payload))
				}
			}
		}

		_ = pubsub.Close()
		if h.ctx.Err() != nil {
			return
		}
		slog.Warn("ws redis pubsub disconnected, reconnecting in 1s...", "node_id", h.nodeID)
		select {
		case <-h.ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
}

// ServeWS handles GET /api/ws endpoint.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	// Authenticate JWT token: 1. Cookie, 2. Auth header, 3. Query param (compatibility fallback)
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
		tokenString = r.URL.Query().Get("token")
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
		userID:        claims.UserID,
		userEmail:     claims.Email,
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

func (c *Client) handleSubscribe(channel string, lastSequence ...int64) {
	var lastSeq int64
	if len(lastSequence) > 0 {
		lastSeq = lastSequence[0]
	}
	slog.Info("ws subscription requested", "user_id", c.userID, "channel", channel, "last_sequence", lastSeq)

	// Enforce subscription rate limit
	if c.hub.rateLimiter != nil && c.hub.subscriptionLimit > 0 {
		allowed, retryAfter, _ := c.hub.rateLimiter.Allow(context.Background(), "ws_sub", "user:"+c.userID.String(), c.hub.subscriptionLimit, 1*time.Minute)
		if !allowed {
			slog.Warn("ws subscription rate limited", "user_id", c.userID, "channel", channel, "retry_after", retryAfter)
			retrySec := int(retryAfter.Seconds())
			if retrySec <= 0 {
				retrySec = 1
			}
			c.sendError("RATE_LIMITED", fmt.Sprintf("subscription rate limit exceeded, please retry after %d seconds", retrySec))
			return
		}
	}

	if channel == "" {
		slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "channel is required")
		c.sendError("INVALID_CHANNEL", "channel is required")
		return
	}

	var (
		channelType string
		resourceID  uuid.UUID
	)

	if strings.HasPrefix(channel, "deployment:") && strings.Contains(channel, ":service:") {
		// Scoped service deployment channel: deployment:<uuid>:service:<uuid>
		subParts := strings.Split(channel, ":")
		if len(subParts) != 4 || subParts[0] != "deployment" || subParts[2] != "service" {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "invalid service deployment channel format")
			c.sendError("INVALID_CHANNEL", "invalid channel format. Expected deployment:<uuid>:service:<uuid>")
			return
		}
		deployUUID, err := uuid.Parse(subParts[1])
		if err != nil {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "invalid deployment UUID")
			c.sendError("INVALID_CHANNEL", "invalid deployment UUID")
			return
		}
		_, err = uuid.Parse(subParts[3])
		if err != nil {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "invalid service UUID")
			c.sendError("INVALID_CHANNEL", "invalid service UUID")
			return
		}
		channelType = "deployment"
		resourceID = deployUUID
	} else {
		parts := strings.SplitN(channel, ":", 2)
		if len(parts) != 2 {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "invalid channel format")
			c.sendError("INVALID_CHANNEL", "invalid channel format. Expected project:<uuid> or deployment:<uuid>")
			return
		}

		channelType = parts[0]
		parsedID, err := uuid.Parse(parts[1])
		if err != nil {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "invalid resource UUID")
			c.sendError("INVALID_CHANNEL", "invalid resource UUID")
			return
		}
		resourceID = parsedID
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
	} else if channelType == "service-deployment" {
		// Authorize service-deployment channels: resolve service deployment → service → project → owner
		if c.hub.serviceDeploymentService == nil || c.hub.serviceService == nil {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "service-deployment resolver unavailable")
			c.sendError("SERVICE_UNAVAILABLE", "service-deployment service unavailable")
			return
		}
		sd, err := c.hub.serviceDeploymentService.GetServiceDeployment(ctx, resourceID)
		if err != nil {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "service deployment not found")
			c.sendError("UNAUTHORIZED", "service deployment not found")
			return
		}
		svc, err := c.hub.serviceService.GetServiceByID(ctx, sd.ServiceID)
		if err != nil {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "parent service not found")
			c.sendError("UNAUTHORIZED", "parent service not found")
			return
		}
		projectID = svc.ProjectID
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
	_, err := c.hub.projectService.GetProject(ctx, projectID, c.userID)
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
		Type:     "subscribed",
		Channel:  channel,
		Sequence: lastSeq,
	})

	// Replay missed log events if lastSeq > 0 and service is available
	if channelType == "deployment" && lastSeq > 0 && c.hub.deploymentService != nil {
		var serviceUUIDPtr *uuid.UUID
		if strings.Contains(channel, ":service:") {
			subParts := strings.Split(channel, ":")
			if len(subParts) == 4 {
				if parsedSvcID, err := uuid.Parse(subParts[3]); err == nil {
					serviceUUIDPtr = &parsedSvcID
				}
			}
		}
		replayLogs, err := c.hub.deploymentService.GetDeploymentLogsAfter(ctx, resourceID, serviceUUIDPtr, lastSeq, 2000)
		if err == nil && len(replayLogs) > 0 {
			slog.Info("replaying missed logs for reconnected subscriber",
				"user_id", c.userID,
				"channel", channel,
				"last_sequence", lastSeq,
				"count", len(replayLogs),
			)
			for _, l := range replayLogs {
				logData := map[string]interface{}{
					"id":            l.ID,
					"sequence":      l.ID,
					"deployment_id": l.DeploymentID.String(),
					"timestamp":     l.Timestamp.Format(time.RFC3339Nano),
					"phase":         l.Phase,
					"stream":        l.Stream,
					"message":       l.Message,
				}
				if l.ServiceID != nil {
					logData["service_id"] = l.ServiceID.String()
				}
				c.sendEvent(&EventMessage{
					Type:     "log",
					Channel:  channel,
					Sequence: l.ID,
					Data:     logData,
				})
			}
		}
	}

	// Replay missed logs for service-deployment channels
	if channelType == "service-deployment" && lastSeq > 0 && c.hub.serviceDeploymentService != nil {
		replayLogs, err := c.hub.serviceDeploymentService.GetServiceDeploymentLogsAfter(ctx, resourceID, lastSeq, 2000)
		if err == nil && len(replayLogs) > 0 {
			slog.Info("replaying missed service-deployment logs",
				"user_id", c.userID,
				"channel", channel,
				"last_sequence", lastSeq,
				"count", len(replayLogs),
			)
			for _, l := range replayLogs {
				logData := map[string]interface{}{
					"id":                    l.ID,
					"sequence":              l.ID,
					"service_deployment_id": resourceID.String(),
					"timestamp":             l.Timestamp.Format(time.RFC3339Nano),
					"phase":                 l.Phase,
					"stream":                l.Stream,
					"message":               l.Message,
				}
				if l.ServiceID != nil {
					logData["service_id"] = l.ServiceID.String()
				}
				if l.DeploymentID != nil {
					logData["deployment_id"] = l.DeploymentID.String()
				}
				c.sendEvent(&EventMessage{
					Type:     "log",
					Channel:  channel,
					Sequence: l.ID,
					Data:     logData,
				})
			}
		}
	}
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
	c.enqueue(payload)
}

func (c *Client) sendError(code, message string) {
	c.sendEvent(&EventMessage{
		Type:    "error",
		Code:    code,
		Message: message,
	})
}
