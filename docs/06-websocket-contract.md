# ForgeLAB — WebSocket Contract & Realtime Architecture

**Status:** Current Implementation & Future Architecture Specification  
**Endpoint:** `GET /api/ws?token=<access_token>`  
**Protocol:** WebSocket (RFC 6455)  

---

## Related Documents

- [INSTRUCTION.md](../INSTRUCTION.md) — Operational memory & architectural constraints
- [docs/00-current-state.md](00-current-state.md) — Current state snapshot & matrix
- [docs/09-api-contract.md](09-api-contract.md) — Authoritative REST & WebSocket API specification
- [docs/10-frontend-architecture.md](10-frontend-architecture.md) — Frontend WebSocket client hook (`useWebSocket.ts`)
- [docs/14-known-limitations.md](14-known-limitations.md) — Known duplicate delivery investigation item

---

## 1. Scope Distinction: MVP Reality vs. Future Replay

To prevent architectural confusion, ForgeLAB explicitly separates three distinct realtime layers:

```text
1. CURRENT IMPLEMENTED MVP
   Deployment-scoped WebSocket subscriptions ("deployment:<uuid>")
   + REST-fetched historical logs ("GET /api/projects/:id/deployments/:did/logs")
   + live WebSocket log streaming

2. EXISTING PROJECT-LEVEL CHANNEL CONTRACT
   Channel "project:<uuid>" authorization exists in WebSocket Hub.
   However, meaningful current event producers in the backend are limited/unestablished.

3. FUTURE / PROPOSED EVENT-REPLAY ARCHITECTURE
   A dedicated Deployment Event Service, database event ledger, and replay-on-connect
   handshake. (PLANNED / NOT IMPLEMENTED IN CURRENT MVP).
```

---

## 2. Current Implemented MVP Architecture

The MVP provides live build and runtime log observation for active deployments:

```text
Docker Build / Container Logs
               │
               ▼
      Docker Engine Runner
 (internal/docker/engine.go)
               │
               ├──► 1. Secret Redaction & PostgreSQL Persistence (deployment_logs)
               │
               └──► 2. wsHub.PublishEvent("deployment:<uuid>", &EventMessage{...})
                           │
                           ▼
                  WebSocket Hub & Redis Pub/Sub
                           │
                           ▼
                Browser / Client (useWebSocket)
```

### Connection Handshake
- **URL:** `ws://localhost:8080/api/ws?token=<jwt_access_token>`
- **Authentication:**
  1. Primary: `token` query parameter.
  2. Fallback: `Authorization: Bearer <token>` header.
  3. Fallback: `forgelab_access_token` cookie.
- If no valid token is provided or the token has expired, the server responds with `HTTP 401 Unauthorized` and aborts the WebSocket upgrade.

---

## 3. Channel Naming & Protocol

All channels use durable **UUIDv4** strings. Channels are never keyed by project name, container name, or branch.

| Channel Pattern | MVP Implementation Status | Purpose |
| :--- | :--- | :--- |
| `deployment:<deployment-uuid>` | **ACTIVELY PRODUCED & CONSUMED** | Realtime build logs, runtime stdout/stderr, and status changes. |
| `project:<project-uuid>` | **CONTRACT SUPPORTED / NO ACTIVE PRODUCERS** | Project-wide lifecycle events (e.g. deployment queued). |

### Client-to-Server Message Formats

```typescript
// Subscribe to a deployment channel
{
  "type": "subscribe",
  "channel": "deployment:c3d4e5f6-a7b8-4c1d-9e0f-1a2b3c4d5e6f"
}

// Unsubscribe from a deployment channel
{
  "type": "unsubscribe",
  "channel": "deployment:c3d4e5f6-a7b8-4c1d-9e0f-1a2b3c4d5e6f"
}

// Heartbeat keepalive
{
  "type": "ping"
}
```

### Server-to-Client Message Formats

```typescript
// 1. Subscription Confirmed
{
  "type": "subscribed",
  "channel": "deployment:c3d4e5f6-a7b8-4c1d-9e0f-1a2b3c4d5e6f"
}

// 2. Subscription Rejected / Authorization Error
{
  "type": "error",
  "code": "UNAUTHORIZED",
  "message": "access denied to this resource"
}

// 3. Live Log Line (Build, Startup, Health, or Runtime)
{
  "type": "log",
  "channel": "deployment:c3d4e5f6-a7b8-4c1d-9e0f-1a2b3c4d5e6f",
  "data": {
    "timestamp": "2026-09-26T12:00:05Z",
    "phase": "build",        // "source" | "build" | "startup" | "health" | "runtime"
    "stream": "stdout",      // "stdout" | "stderr" | "system"
    "message": "Step 2/5 : RUN npm run build"
  }
}

// 4. Deployment Status Change
{
  "type": "status_change",
  "channel": "deployment:c3d4e5f6-a7b8-4c1d-9e0f-1a2b3c4d5e6f",
  "data": {
    "deployment_id": "c3d4e5f6-a7b8-4c1d-9e0f-1a2b3c4d5e6f",
    "project_id": "a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d",
    "previous_status": "building",
    "new_status": "starting",
    "timestamp": "2026-09-26T12:00:15Z"
  }
}

// 5. Heartbeat Pong
{
  "type": "pong"
}
```

---

## 4. Strict Realtime Isolation Rules

1. **Server-Side Authorization:** When a client sends `{ "type": "subscribe", "channel": "deployment:<uuid>" }`, the Hub:
   - Resolves `deployment.id == <uuid>` to find its `project_id`.
   - Queries `projects` table to check if `project.owner_id == client.user_id`.
   - If ownership check fails, the Hub sends an `UNAUTHORIZED` error frame and drops the subscription.
2. **Channel Separation:** Messages published to `deployment:<uuid-A>` are routed **only** to clients registered in `channels["deployment:<uuid-A>"]`. They are never broadcast to other projects or deployments.

---

## 5. Known Implementation Issue / Investigation Item

### Double Delivery in `PublishEvent()`
In `backend/internal/websocket/hub.go:L123-L184`, the `PublishEvent` method currently executes:

```go
// 1. Send directly to local connected subscribers
h.mu.RLock()
subscribers, exists := h.channels[channel]
if exists {
    for client := range subscribers {
        client.send <- payload
    }
}
h.mu.RUnlock()

// 2. Publish to Redis if configured
if h.redisClient != nil {
    redisChan := "forgelab:pubsub:" + channel
    h.redisClient.Publish(h.ctx, redisChan, payload)
}
```

Simultaneously, `listenRedisPubSub()` listens on `forgelab:pubsub:*`:
```go
case msg, ok := <-ch:
    channel := strings.TrimPrefix(msg.Channel, "forgelab:pubsub:")
    h.mu.RLock()
    subscribers, exists := h.channels[channel]
    if exists {
        for client := range subscribers {
            client.send <- []byte(msg.Payload)
        }
    }
    h.mu.RUnlock()
```

### Problem Description
Because the same backend instance both delivers directly to local subscribers **and** republishes to Redis (which its own listener receives and forwards to the exact same local subscribers), local subscribers can receive every event **twice**.

### Investigation Guidance for Future Agents:
- **Do not silently accept this as intended behavior.**
- In a single-instance setup, either Redis Pub/Sub should be the sole distributor, or Redis messages should include an instance originator ID (`node_id`) so the sending node skips re-broadcasting messages it originated.

---

## 6. Future Realtime Architecture (Not Implemented in MVP)

The earlier design discussions proposed an event-sourcing and replay model:

```text
Worker
  │
  ▼
Deployment Event Service
  │
  ├──► Database (Event Store Table)
  │
  └──► Redis Pub/Sub
          │
          ▼
    WebSocket Gateway
          │
          ▼
       Browser
```

### Proposed Event Schema (Future)
```json
{
  "eventId": "evt_01J8...",
  "eventType": "DEPLOYMENT_PHASE_CHANGED",
  "projectId": "...",
  "deploymentId": "...",
  "timestamp": "2026-09-26T12:00:00Z",
  "payload": {
    "phase": "BUILDING_IMAGE",
    "stage": 2,
    "totalStages": 5
  }
}
```

### Proposed Progress Stages (Future)
```text
1. FETCHING_SOURCE
2. BUILDING_IMAGE
3. STARTING_CONTAINER
4. HEALTH_CHECK
5. DEPLOYING
6. COMPLETED
```

> **Note:** Neither the event ledger database table nor the structured stage progression schema exists in the current MVP codebase. The current MVP uses direct phase log persistence (`deployment_logs`) and status transitions (`deployments.status`).
