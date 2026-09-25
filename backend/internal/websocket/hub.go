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
	"github.com/forgelab/backend/internal/services"
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

type Hub struct {
	clients           map[*Client]bool
	channels          map[string]map[*Client]bool
	register          chan *Client
	unregister        chan *Client
	mu                sync.RWMutex
	jwtManager        *auth.JWTManager
	projectService    *services.ProjectService
	deploymentService *services.DeploymentService
	redisClient       *redis.Client
	ctx               context.Context
	cancel            context.CancelFunc
}

func NewHub(jwtManager *auth.JWTManager, projectService *services.ProjectService, deploymentService *services.DeploymentService, redisClient *redis.Client) *Hub {
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
			slog.Info("ws client connected", "user_id", client.userID)

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
				slog.Info("ws client disconnected", "user_id", client.userID)
			}
			h.mu.Unlock()
		}
	}
}

// PublishEvent publishes a realtime event to Redis Pub/Sub and direct local subscribers.
func (h *Hub) PublishEvent(channel string, event *EventMessage) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event payload: %w", err)
	}

	// 1. Send directly to local connected subscribers
	h.mu.RLock()
	subscribers, exists := h.channels[channel]
	if exists {
		for client := range subscribers {
			select {
			case client.send <- payload:
			default:
				// Buffer full
			}
		}
	}
	h.mu.RUnlock()

	// 2. Publish to Redis if configured
	if h.redisClient != nil {
		redisChan := "forgelab:pubsub:" + channel
		if err := h.redisClient.Publish(h.ctx, redisChan, payload).Err(); err != nil {
			slog.Error("redis publish error", "channel", redisChan, "error", err)
		}
	}

	return nil
}

func (h *Hub) listenRedisPubSub() {
	pubsub := h.redisClient.PSubscribe(h.ctx, "forgelab:pubsub:*")
	defer pubsub.Close()

	ch := pubsub.Channel()
	for {
		select {
		case <-h.ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			// Extract channel name from redis key: forgelab:pubsub:<channel>
			channel := strings.TrimPrefix(msg.Channel, "forgelab:pubsub:")
			
			h.mu.RLock()
			subscribers, exists := h.channels[channel]
			if exists {
				payload := []byte(msg.Payload)
				for client := range subscribers {
					select {
					case client.send <- payload:
					default:
					}
				}
			}
			h.mu.RUnlock()
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
		http.Error(w, "unauthorized: token required", http.StatusUnauthorized)
		return
	}

	claims, err := h.jwtManager.ValidateAccessToken(tokenString)
	if err != nil {
		http.Error(w, "unauthorized: invalid token", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("ws upgrade failed", "error", err)
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

	go client.writePump()
	go client.readPump()
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
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
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				slog.Error("ws read error", "error", err)
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
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Add queued messages to the current frame
			n := len(c.send)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *Client) handleSubscribe(channel string) {
	if channel == "" {
		c.sendError("INVALID_CHANNEL", "channel is required")
		return
	}

	parts := strings.SplitN(channel, ":", 2)
	if len(parts) != 2 {
		c.sendError("INVALID_CHANNEL", "invalid channel format. Expected project:<uuid> or deployment:<uuid>")
		return
	}

	channelType, resourceIDStr := parts[0], parts[1]
	resourceID, err := uuid.Parse(resourceIDStr)
	if err != nil {
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
		deployment, err := c.hub.deploymentService.GetDeployment(ctx, resourceID)
		if err != nil {
			c.sendError("UNAUTHORIZED", "deployment not found or access denied")
			return
		}
		projectID = deployment.ProjectID
	} else {
		c.sendError("INVALID_CHANNEL", "unknown channel type")
		return
	}

	// Check if user owns the project
	_, err = c.hub.projectService.GetProject(ctx, projectID, c.userID)
	if err != nil {
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

	slog.Info("ws client subscribed", "user_id", c.userID, "channel", channel)
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
	}
}

func (c *Client) sendError(code, message string) {
	c.sendEvent(&EventMessage{
		Type:    "error",
		Code:    code,
		Message: message,
	})
}
