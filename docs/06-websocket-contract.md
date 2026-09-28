# ForgeLAB — WebSocket Contract & Realtime Architecture

**Status:** Current Implementation Specification  
**Endpoint:** `GET /api/ws?token=<access_token>` (or authenticated via `forgelab_access_token` HttpOnly cookie)  
**Protocol:** WebSocket (RFC 6455)  

---

## Related Documents

- [INSTRUCTION.md](../INSTRUCTION.md) — Operational memory & architectural constraints
- [docs/00-current-state.md](00-current-state.md) — Current state snapshot & matrix
- [docs/09-api-contract.md](09-api-contract.md) — Authoritative REST & WebSocket API specification
- [docs/10-frontend-architecture.md](10-frontend-architecture.md) — Frontend WebSocket client hook (`useDeploymentWS.ts`)
- [docs/14-known-limitations.md](14-known-limitations.md) — Known platform boundaries & backlog

---

## 1. Scope Distinction: MVP Reality vs. Future Replay

ForgeLAB separates three distinct realtime layers:

```text
1. CURRENT IMPLEMENTED REALTIME ARCHITECTURE
   Deployment-scoped WebSocket subscriptions ("deployment:<uuid>")
   + REST-fetched historical logs ("GET /api/projects/:id/deployments/:did/logs")
   + live WebSocket log streaming with persistent database IDs
   + single-event framing (1 JSON event = 1 WebSocket text frame)
   + unified Redis pub/sub distribution (exactly-once local delivery)
   + frontend exponential-backoff reconnect and safe log merging

2. EXISTING PROJECT-LEVEL CHANNEL CONTRACT
   Channel "project:<uuid>" authorization exists in WebSocket Hub.
   Ready for future project-level lifecycle events.

3. FUTURE / PROPOSED EVENT-REPLAY ARCHITECTURE
   A dedicated Deployment Event Service, database event ledger, and replay-on-connect
   handshake. (PLANNED / NOT IMPLEMENTED IN CURRENT MVP).
```

---

## 2. Current Implemented Architecture

The realtime pipeline provides live build and runtime log observation for active deployments:

```text
Docker Build / Container Logs
               │
               ▼
      Docker Engine Runner
 (internal/docker/engine.go)
               │
               ├──► 1. Secret Redaction & PostgreSQL Persistence (deployment_logs)
               │       RETURNING id, timestamp
               │
               └──► 2. wsHub.PublishEvent("deployment:<uuid>", &EventMessage{
                           Data: { id: persistedId, deployment_id: uuid, ... }
                       })
                           │
                           ▼
                  Redis Pub/Sub Bus ("forgelab:pubsub:deployment:<uuid>")
                           │
                           ▼
                  WebSocket Hub (listenRedisPubSub -> broadcastLocally)
                           │
                           ▼ (1 event = 1 WS text message frame)
                Browser Client (useDeploymentWS)
                           │
                           ├── Deduplicate by persistent ID
                           └── Safely merge with REST historical logs
```

### Connection Handshake
- **URL Resolution:**
  - Configurable via `NEXT_PUBLIC_WS_URL`.
  - Next.js dev server on port 3000: `${wsProto}//${window.location.hostname}:8080/api/ws` (configurable via `NEXT_PUBLIC_BACKEND_PORT`).
  - Unified reverse-proxy / production: `${wsProto}//${window.location.host}/api/ws`.
- **Authentication:**
  1. Primary for browsers: `forgelab_access_token` HttpOnly cookie automatically forwarded by the browser during the WebSocket HTTP Upgrade handshake.
  2. Fallback: `token` query parameter (`?token=<jwt_access_token>`).
  3. Fallback: `Authorization: Bearer <token>` header.
- If no valid token or session exists, the server responds with `HTTP 401 Unauthorized` and aborts the WebSocket upgrade.

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
// 1. Subscription Confirmed (stream is now live)
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

// 3. Live Log Line (Build, Startup, Health, or Runtime) — Contains persistent database ID
{
  "type": "log",
  "channel": "deployment:c3d4e5f6-a7b8-4c1d-9e0f-1a2b3c4d5e6f",
  "data": {
    "id": 12345,                                      // Persisted PostgreSQL deployment_logs.id
    "deployment_id": "c3d4e5f6-a7b8-4c1d-9e0f-1a2b3c4d5e6f",
    "timestamp": "2026-09-28T08:00:05.123456Z",
    "phase": "build",                                 // "source" | "build" | "startup" | "health" | "runtime"
    "stream": "stdout",                               // "stdout" | "stderr" | "system"
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
    "timestamp": "2026-09-28T08:00:15Z"
  }
}

// 5. Heartbeat Pong
{
  "type": "pong"
}
```

---

## 4. Message Framing & Delivery Model

### Single-Event WebSocket Framing
- Every event queued for delivery is written as a distinct, complete `websocket.TextMessage` frame.
- Multiple queued events are never concatenated with newline delimiters into a single WebSocket text frame.
- The browser frontend parses each WebSocket `message` event as exactly one valid JSON object (`JSON.parse(event.data)`).

### Unified Redis Event Distribution (Resolved Architecture)
- Redis acts as the centralized event distribution bus for control plane instances.
- When `PublishEvent(channel, event)` is called:
  - If Redis is enabled, the event is serialized and published exclusively to Redis Pub/Sub (`forgelab:pubsub:<channel>`).
  - The Redis pub/sub listener (`listenRedisPubSub()`) receives the event from Redis and broadcasts it locally via `broadcastLocally(channel, payload)`.
  - This guarantees that local subscribers receive each event **exactly once** without duplicate delivery.
  - Multi-instance control planes work seamlessly: every control plane node receives the Redis pub/sub event and broadcasts to its own local clients.
  - If Redis is unconfigured (e.g. standalone test mode), direct local delivery is used as a fallback.
  - If Redis publishing fails, the system logs the error and gracefully falls back to local delivery.

---

## 5. Frontend Subscription & Lifecycle State Machine

The frontend hook (`useDeploymentWS`) exposes an explicit connection state:
- `connecting`: WebSocket connection in progress.
- `connected`: Transport connection established (waiting to send subscription).
- `subscribing`: Subscription frame sent to server.
- `subscribed`: Server acknowledged subscription with `{ type: 'subscribed' }` — the stream is fully live.
- `reconnecting`: Unexpected disconnect occurred; automatic reconnect scheduled with exponential backoff.
- `offline`: Socket closed intentionally or no channel active.

### Exponential Backoff & Reconnect
- On unexpected close, reconnects automatically with exponential backoff: 1s, 2s, 4s, 8s, 16s, max 30s.
- Backoff delay resets to 1000ms upon successful `subscribed` acknowledgment.
- Reconnect attempts and timers are cleanly cancelled on component unmount or channel change.
- Sockets in `CONNECTING` or `OPEN` states are closed during teardown, preventing orphaned connections.

### REST + WebSocket Safe Log Merging
1. When switching deployments, logs are cleared for the new deployment view.
2. Live WebSocket logs start streaming immediately.
3. In parallel, historical logs are fetched via `GET /api/projects/:id/deployments/:did/logs`.
4. When the historical REST request resolves, `addHistoricalLogs(historyLogs)` merges historical logs with live logs by persistent database ID and sorts chronologically.
5. In-flight WebSocket logs that arrived while the REST request was in flight are never wiped out.

---

## 6. Strict Realtime Isolation Rules

1. **Server-Side Authorization:** When a client sends `{ "type": "subscribe", "channel": "deployment:<uuid>" }`, the Hub:
   - Resolves `deployment.id == <uuid>` to find its `project_id`.
   - Queries `projects` table to check if `project.owner_id == client.user_id`.
   - If ownership check fails, the Hub sends an `UNAUTHORIZED` error frame and drops the subscription.
2. **Channel Separation:** Messages published to `deployment:<uuid-A>` are routed **only** to clients registered in `channels["deployment:<uuid-A>"]`. They are never broadcast to other projects or deployments.
