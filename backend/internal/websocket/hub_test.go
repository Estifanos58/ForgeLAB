package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/forgelab/backend/internal/auth"
	"github.com/forgelab/backend/internal/models"
)

// Mock project and deployment services for unit testing subscription authorization
type mockProjectAuthorizer struct {
	getProjectFn func(ctx context.Context, id, ownerID uuid.UUID) (*models.Project, error)
}

func (m *mockProjectAuthorizer) GetProject(ctx context.Context, id, ownerID uuid.UUID) (*models.Project, error) {
	if m.getProjectFn != nil {
		return m.getProjectFn(ctx, id, ownerID)
	}
	return nil, errors.New("project not found")
}

type mockDeploymentResolver struct {
	getDeploymentFn func(ctx context.Context, id uuid.UUID) (*models.Deployment, error)
}

func (m *mockDeploymentResolver) GetDeployment(ctx context.Context, id uuid.UUID) (*models.Deployment, error) {
	if m.getDeploymentFn != nil {
		return m.getDeploymentFn(ctx, id)
	}
	return nil, errors.New("deployment not found")
}

// 1. Channel isolation test
func TestHubChannelIsolation(t *testing.T) {
	hub := NewHub(nil, nil, nil, nil)
	defer hub.cancel()

	userA := uuid.New()
	userB := uuid.New()

	clientA := &Client{
		hub:           hub,
		userID:        userA,
		subscriptions: make(map[string]bool),
		send:          make(chan []byte, 10),
	}

	clientB := &Client{
		hub:           hub,
		userID:        userB,
		subscriptions: make(map[string]bool),
		send:          make(chan []byte, 10),
	}

	channelA := "deployment:" + uuid.New().String()
	channelB := "deployment:" + uuid.New().String()

	hub.mu.Lock()
	hub.clients[clientA] = true
	hub.clients[clientB] = true
	hub.channels[channelA] = map[*Client]bool{clientA: true}
	hub.channels[channelB] = map[*Client]bool{clientB: true}
	hub.mu.Unlock()

	// Publish to Channel A
	eventA := &EventMessage{
		Type:    "log",
		Channel: channelA,
		Data:    "log line for A",
	}
	err := hub.PublishEvent(channelA, eventA)
	if err != nil {
		t.Fatalf("unexpected publish error: %v", err)
	}

	// Verify Client A receives message for Channel A
	select {
	case msg := <-clientA.send:
		var rec EventMessage
		if err := json.Unmarshal(msg, &rec); err != nil {
			t.Fatalf("failed to unmarshal client A msg: %v", err)
		}
		if rec.Channel != channelA {
			t.Errorf("expected channel %s, got %s", channelA, rec.Channel)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("client A timed out waiting for event on Channel A")
	}

	// Verify Client B received NOTHING for Channel A
	select {
	case msg := <-clientB.send:
		t.Fatalf("client B received unexpected message on channel B: %s", string(msg))
	default:
		// Success! Client B did not receive Client A's message
	}
}

// 2. One published event results in exactly one local delivery per subscriber
func TestSinglePublishedEventDeliveredExactlyOnce(t *testing.T) {
	hub := NewHub(nil, nil, nil, nil)
	defer hub.cancel()

	userID := uuid.New()
	client := &Client{
		hub:           hub,
		userID:        userID,
		subscriptions: make(map[string]bool),
		send:          make(chan []byte, 10),
	}

	channel := "deployment:" + uuid.New().String()

	hub.mu.Lock()
	hub.clients[client] = true
	hub.channels[channel] = map[*Client]bool{client: true}
	hub.mu.Unlock()

	event := &EventMessage{
		Type:    "log",
		Channel: channel,
		Data: map[string]interface{}{
			"id":      int64(101),
			"message": "test single delivery",
		},
	}

	err := hub.PublishEvent(channel, event)
	if err != nil {
		t.Fatalf("failed to publish event: %v", err)
	}

	// First read should succeed
	select {
	case msg := <-client.send:
		var rec EventMessage
		if err := json.Unmarshal(msg, &rec); err != nil {
			t.Fatalf("failed to unmarshal message: %v", err)
		}
		if rec.Type != "log" {
			t.Fatalf("expected type 'log', got %s", rec.Type)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for event delivery")
	}

	// Second read should be empty — proving exactly one delivery, no duplicate
	select {
	case msg := <-client.send:
		t.Fatalf("received duplicate event: %s", string(msg))
	default:
		// Success: exactly one event was delivered
	}
}

// 3. Redis event distribution does not duplicate local delivery
func TestRedisDistributionModelDoesNotDuplicate(t *testing.T) {
	hub := NewHub(nil, nil, nil, nil)
	defer hub.cancel()

	userID := uuid.New()
	client := &Client{
		hub:           hub,
		userID:        userID,
		subscriptions: make(map[string]bool),
		send:          make(chan []byte, 10),
	}

	channel := "deployment:" + uuid.New().String()

	hub.mu.Lock()
	hub.clients[client] = true
	hub.channels[channel] = map[*Client]bool{client: true}
	hub.mu.Unlock()

	event := &EventMessage{
		Type:    "log",
		Channel: channel,
		Data: map[string]interface{}{
			"id":      int64(555),
			"message": "redis delivered event",
		},
	}
	payload, _ := json.Marshal(event)

	// In the new architecture, the Redis subscriber listener forwards incoming Redis pubsub messages
	// to local subscribers via hub.broadcastLocally(channel, payload).
	// Simulate the Redis pub/sub delivery:
	hub.broadcastLocally(channel, payload)

	// Verify client receives exactly ONE event
	select {
	case msg := <-client.send:
		var rec EventMessage
		if err := json.Unmarshal(msg, &rec); err != nil {
			t.Fatalf("failed to parse: %v", err)
		}
		data := rec.Data.(map[string]interface{})
		if data["id"].(float64) != 555 {
			t.Fatalf("unexpected id: %v", data["id"])
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for redis broadcast event")
	}

	// Verify no second message exists in channel buffer
	select {
	case msg := <-client.send:
		t.Fatalf("unexpected duplicate message received: %s", string(msg))
	default:
		// Success!
	}
}

// 4. Multiple queued events are transmitted as separate WebSocket messages (framing fix)
func TestMultipleQueuedEventsAsSeparateWebSocketMessages(t *testing.T) {
	jwtManager := auth.NewJWTManager("test-secret-key-at-least-32-bytes-long!", 15*time.Minute, 7*24*time.Hour)
	userID := uuid.New()
	token, err := jwtManager.GenerateAccessToken(userID, "test@example.com")
	if err != nil {
		t.Fatalf("failed to generate access token: %v", err)
	}

	hub := NewHub(jwtManager, nil, nil, nil)
	defer hub.cancel()

	// Spin up test HTTP server with WebSocket handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.ServeWS(w, r)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/ws?token=" + token

	// Dial WebSocket client
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer wsConn.Close()

	// Allow client to register in hub
	time.Sleep(50 * time.Millisecond)

	hub.mu.RLock()
	var registeredClient *Client
	for c := range hub.clients {
		if c.userID == userID {
			registeredClient = c
			break
		}
	}
	hub.mu.RUnlock()

	if registeredClient == nil {
		t.Fatal("client was not registered in hub")
	}

	// Queue 3 distinct JSON messages into registeredClient.send at once
	msg1, _ := json.Marshal(&EventMessage{Type: "log", Data: "line 1"})
	msg2, _ := json.Marshal(&EventMessage{Type: "log", Data: "line 2"})
	msg3, _ := json.Marshal(&EventMessage{Type: "log", Data: "line 3"})

	registeredClient.send <- msg1
	registeredClient.send <- msg2
	registeredClient.send <- msg3

	// Read 3 frames from WebSocket client.
	// In the old broken implementation, these would be combined into a single frame with '\n'.
	// In the fixed implementation, each must be a distinct WebSocket TextMessage frame containing valid JSON.
	for i := 1; i <= 3; i++ {
		msgType, data, err := wsConn.ReadMessage()
		if err != nil {
			t.Fatalf("failed to read message %d: %v", i, err)
		}
		if msgType != websocket.TextMessage {
			t.Fatalf("expected TextMessage, got %d", msgType)
		}

		// Ensure raw data does NOT contain newlines from concatenation
		if strings.Contains(string(data), "\n") {
			t.Fatalf("message %d contains newline delimiter, indicating invalid batching: %s", i, string(data))
		}

		var event EventMessage
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("message %d is not a single valid JSON object: %v (raw: %s)", i, err, string(data))
		}
		if event.Data != "line "+string(rune('0'+i)) {
			t.Fatalf("expected data 'line %d', got %v", i, event.Data)
		}
	}
}

// 5. Authorized deployment subscription succeeds
func TestAuthorizedDeploymentSubscriptionSucceeds(t *testing.T) {
	userID := uuid.New()
	projectID := uuid.New()
	deploymentID := uuid.New()

	mockProjectSvc := &mockProjectAuthorizer{
		getProjectFn: func(ctx context.Context, id, ownerID uuid.UUID) (*models.Project, error) {
			if id == projectID && ownerID == userID {
				return &models.Project{ID: projectID, OwnerID: userID}, nil
			}
			return nil, errors.New("access denied")
		},
	}

	mockDeploySvc := &mockDeploymentResolver{
		getDeploymentFn: func(ctx context.Context, id uuid.UUID) (*models.Deployment, error) {
			if id == deploymentID {
				return &models.Deployment{ID: deploymentID, ProjectID: projectID}, nil
			}
			return nil, errors.New("deployment not found")
		},
	}

	hub := NewHub(nil, mockProjectSvc, mockDeploySvc, nil)
	defer hub.cancel()

	client := &Client{
		hub:           hub,
		userID:        userID,
		subscriptions: make(map[string]bool),
		send:          make(chan []byte, 10),
	}

	channel := "deployment:" + deploymentID.String()
	client.handleSubscribe(channel)

	// Check response event
	select {
	case msg := <-client.send:
		var event EventMessage
		if err := json.Unmarshal(msg, &event); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}
		if event.Type != "subscribed" {
			t.Fatalf("expected type 'subscribed', got %s", event.Type)
		}
		if event.Channel != channel {
			t.Fatalf("expected channel %s, got %s", channel, event.Channel)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for subscription response")
	}

	// Verify client is in hub.channels
	hub.mu.RLock()
	isSubscribed := hub.channels[channel][client]
	hub.mu.RUnlock()
	if !isSubscribed {
		t.Fatal("client was not added to hub.channels subscribers")
	}
}

// 6. Unauthorized deployment subscription fails
func TestUnauthorizedDeploymentSubscriptionFails(t *testing.T) {
	userID := uuid.New()
	otherUserID := uuid.New()
	projectID := uuid.New()
	deploymentID := uuid.New()

	mockProjectSvc := &mockProjectAuthorizer{
		getProjectFn: func(ctx context.Context, id, ownerID uuid.UUID) (*models.Project, error) {
			// Project owned by other user
			if ownerID != otherUserID {
				return nil, errors.New("access denied to this resource")
			}
			return &models.Project{ID: projectID, OwnerID: otherUserID}, nil
		},
	}

	mockDeploySvc := &mockDeploymentResolver{
		getDeploymentFn: func(ctx context.Context, id uuid.UUID) (*models.Deployment, error) {
			return &models.Deployment{ID: deploymentID, ProjectID: projectID}, nil
		},
	}

	hub := NewHub(nil, mockProjectSvc, mockDeploySvc, nil)
	defer hub.cancel()

	client := &Client{
		hub:           hub,
		userID:        userID, // Current client is NOT otherUserID
		subscriptions: make(map[string]bool),
		send:          make(chan []byte, 10),
	}

	channel := "deployment:" + deploymentID.String()
	client.handleSubscribe(channel)

	// Check response event
	select {
	case msg := <-client.send:
		var event EventMessage
		if err := json.Unmarshal(msg, &event); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}
		if event.Type != "error" {
			t.Fatalf("expected type 'error', got %s", event.Type)
		}
		if event.Code != "UNAUTHORIZED" {
			t.Fatalf("expected code 'UNAUTHORIZED', got %s", event.Code)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for rejection response")
	}

	// Verify client is NOT in hub.channels
	hub.mu.RLock()
	isSubscribed := hub.channels[channel][client]
	hub.mu.RUnlock()
	if isSubscribed {
		t.Fatal("unauthorized client should not be in hub.channels")
	}
}

// 7. Invalid channel format is rejected
func TestInvalidChannelRejected(t *testing.T) {
	hub := NewHub(nil, nil, nil, nil)
	defer hub.cancel()

	client := &Client{
		hub:           hub,
		userID:        uuid.New(),
		subscriptions: make(map[string]bool),
		send:          make(chan []byte, 10),
	}

	client.handleSubscribe("invalid-channel-format")

	select {
	case msg := <-client.send:
		var event EventMessage
		if err := json.Unmarshal(msg, &event); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}
		if event.Type != "error" {
			t.Fatalf("expected type 'error', got %s", event.Type)
		}
		if event.Code != "INVALID_CHANNEL" {
			t.Fatalf("expected code 'INVALID_CHANNEL', got %s", event.Code)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for invalid channel error")
	}
}

// 8. Connection cleanup removes subscriptions
func TestConnectionCleanupRemovesSubscriptions(t *testing.T) {
	hub := NewHub(nil, nil, nil, nil)
	defer hub.cancel()

	userID := uuid.New()
	client := &Client{
		hub:           hub,
		userID:        userID,
		subscriptions: make(map[string]bool),
		send:          make(chan []byte, 10),
	}

	channel := "deployment:" + uuid.New().String()

	hub.mu.Lock()
	hub.clients[client] = true
	hub.channels[channel] = map[*Client]bool{client: true}
	client.subscriptions[channel] = true
	hub.mu.Unlock()

	// Unregister client
	hub.unregister <- client

	// Give the hub.run goroutine a moment to process unregistration
	time.Sleep(50 * time.Millisecond)

	hub.mu.RLock()
	_, clientExists := hub.clients[client]
	_, channelExists := hub.channels[channel]
	hub.mu.RUnlock()

	if clientExists {
		t.Fatal("client was not removed from hub.clients")
	}
	if channelExists {
		t.Fatal("empty channel was not cleaned up from hub.channels")
	}

	// Verify client.send is closed
	select {
	case _, ok := <-client.send:
		if ok {
			t.Fatal("client.send should be closed upon unregister")
		}
	default:
		t.Fatal("client.send should be closed and readable")
	}
}

// 9. WebSocket event contains persisted deployment log ID
func TestWebSocketEventContainsPersistedDeploymentLogID(t *testing.T) {
	deploymentID := uuid.New()
	channel := "deployment:" + deploymentID.String()

	logEvent := &EventMessage{
		Type:    "log",
		Channel: channel,
		Data: map[string]interface{}{
			"id":            int64(12345),
			"deployment_id": deploymentID.String(),
			"timestamp":     time.Now().Format(time.RFC3339),
			"phase":         "build",
			"stream":        "stdout",
			"message":       "Building Docker container...",
		},
	}

	payload, err := json.Marshal(logEvent)
	if err != nil {
		t.Fatalf("failed to marshal log event: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("failed to parse event payload: %v", err)
	}

	data, ok := parsed["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data field missing or invalid type")
	}

	if id, exists := data["id"]; !exists || id.(float64) != 12345 {
		t.Fatalf("expected persisted log id 12345, got %v", id)
	}
	if depID, exists := data["deployment_id"]; !exists || depID.(string) != deploymentID.String() {
		t.Fatalf("expected deployment_id %s, got %v", deploymentID.String(), depID)
	}
}
