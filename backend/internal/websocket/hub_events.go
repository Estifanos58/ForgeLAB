package websocket

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

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

// RegisterTestSubscriber registers a test channel to receive raw JSON messages for a channel.
// Returns an unregister function.
func (h *Hub) RegisterTestSubscriber(channel string, ch chan []byte) func() {
	h.mu.Lock()
	defer h.mu.Unlock()

	client := &Client{
		hub:           h,
		subscriptions: map[string]bool{channel: true},
		send:          ch,
	}
	h.clients[client] = true
	if h.channels[channel] == nil {
		h.channels[channel] = make(map[*Client]bool)
	}
	h.channels[channel][client] = true

	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.clients, client)
		if subs, ok := h.channels[channel]; ok {
			delete(subs, client)
			if len(subs) == 0 {
				delete(h.channels, channel)
			}
		}
	}
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
