package websocket

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

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

	// Case 1: Subscribe Client A to Channel A
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

	// Case 2: Verify Client B received NOTHING for Channel A
	select {
	case msg := <-clientB.send:
		t.Fatalf("client B received unexpected message on channel B: %s", string(msg))
	default:
		// Success! Client B did not receive Client A's message
	}

	// Case 5: Unsubscribe Client A and verify no subsequent events received
	hub.mu.Lock()
	delete(hub.channels[channelA], clientA)
	hub.mu.Unlock()

	err = hub.PublishEvent(channelA, eventA)
	if err != nil {
		t.Fatalf("unexpected publish error: %v", err)
	}

	select {
	case msg := <-clientA.send:
		t.Fatalf("unsubscribed client A received event: %s", string(msg))
	default:
		// Success! Unsubscribed client received no event.
	}
}
