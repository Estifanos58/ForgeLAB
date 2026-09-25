# ForgeLab — WebSocket Contract & Realtime Isolation

**Status:** Current  
**Last Updated:** 2026-09-25  

---

## WebSocket Purpose

WebSockets in ForgeLab provide realtime event streaming for:

- **Deployment progress** — phase transitions during build/deploy
- **Build logs** — live output from Docker build
- **Runtime/container logs** — live stdout/stderr from running containers
- **Status changes** — deployment and project status updates

## Connection Lifecycle

```
Browser                                    ForgeLab Server
  │                                              │
  │  WS CONNECT /api/ws                          │
  │  Headers: Authorization: Bearer <jwt>         │
  │──────────────────────────────────────────────►│
  │                                              │
  │  ◄─── Connection accepted (or 401 rejected)  │
  │                                              │
  │  SUBSCRIBE { type: "subscribe",              │
  │    channel: "deployment:<deploy-uuid>" }     │
  │──────────────────────────────────────────────►│
  │                                              │
  │  ◄─── AUTH CHECK: does user own this         │
  │       deployment's project?                  │
  │                                              │
  │  ◄─── { type: "subscribed",                  │
  │         channel: "deployment:<deploy-uuid>" } │
  │       OR                                      │
  │  ◄─── { type: "error",                       │
  │         code: "UNAUTHORIZED" }                │
  │                                              │
  │  ◄─── { type: "log", ...event data }         │
  │  ◄─── { type: "status_change", ... }         │
  │  ◄─── { type: "log", ...event data }         │
  │                                              │
  │  UNSUBSCRIBE { type: "unsubscribe",          │
  │    channel: "deployment:<deploy-uuid>" }     │
  │──────────────────────────────────────────────►│
  │                                              │
  │  CLOSE                                        │
  │──────────────────────────────────────────────►│
```

## Channel Naming Convention

All channels use **durable UUIDs**, never names or URLs.

| Channel Pattern | Purpose |
|----------------|---------|
| `deployment:<deployment-uuid>` | Logs and status for a specific deployment |
| `project:<project-uuid>` | Project-level events (new deployment, status changes) |

## Message Types

### Client → Server Messages

```typescript
// Subscribe to a channel
{
  "type": "subscribe",
  "channel": "deployment:<uuid>"  // or "project:<uuid>"
}

// Unsubscribe from a channel
{
  "type": "unsubscribe",
  "channel": "deployment:<uuid>"
}

// Ping (keepalive)
{
  "type": "ping"
}
```

### Server → Client Messages

```typescript
// Subscription confirmed
{
  "type": "subscribed",
  "channel": "deployment:<uuid>"
}

// Subscription rejected
{
  "type": "error",
  "code": "UNAUTHORIZED",
  "message": "You do not have access to this resource"
}

// Deployment log line
{
  "type": "log",
  "channel": "deployment:<uuid>",
  "data": {
    "timestamp": "2026-09-25T20:00:00Z",
    "phase": "build",        // source | build | startup | health | runtime
    "stream": "stdout",      // stdout | stderr | system
    "message": "Step 1/5 : FROM node:18-alpine"
  }
}

// Deployment status change
{
  "type": "status_change",
  "channel": "deployment:<uuid>",
  "data": {
    "deployment_id": "<uuid>",
    "project_id": "<uuid>",
    "previous_status": "BUILDING",
    "new_status": "STARTING",
    "timestamp": "2026-09-25T20:00:00Z"
  }
}

// Project-level event
{
  "type": "project_event",
  "channel": "project:<uuid>",
  "data": {
    "event": "deployment_created",
    "deployment_id": "<uuid>",
    "deploy_number": 5,
    "timestamp": "2026-09-25T20:00:00Z"
  }
}

// Pong (keepalive response)
{
  "type": "pong"
}
```

## Isolation Rules (CRITICAL)

### Rule 1: UUID-Based Identity

All WebSocket channels are identified by durable UUIDs. Never by:
- Project name
- Repository URL
- Container name
- Branch name

### Rule 2: Subscription Authorization

When a client sends a `subscribe` message:

1. Extract user_id from the authenticated WebSocket connection
2. Look up the target resource (deployment → project → owner)
3. Verify `project.owner_id == user_id`
4. If unauthorized: send error message, do NOT subscribe
5. If authorized: add client to channel subscriber list, send confirmation

### Rule 3: Event Isolation

- Events published to `deployment:<uuid-A>` are NEVER delivered to subscribers of `deployment:<uuid-B>`
- Events published to `project:<uuid-A>` are NEVER delivered to subscribers of `project:<uuid-B>`
- Even if both projects use the same repository, they are separate entities

### Rule 4: Server-Side Enforcement

Isolation is enforced server-side. The client cannot bypass it by guessing UUIDs because:
- Subscription requires valid JWT
- Subscription requires ownership verification
- Events are routed by exact channel match on the server

## Implementation Architecture

```
Redis Pub/Sub                    WebSocket Hub                    Clients
                                                                   
channel: deploy:<uuid-1>  ──►  Hub receives message          ──►  Client A
                                │                                   (subscribed to deploy:<uuid-1>)
                                │  Check subscriber list for
                                │  channel "deploy:<uuid-1>"
                                │
                                │  NOT sent to Client B
                                │  (subscribed to deploy:<uuid-2>)

channel: deploy:<uuid-2>  ──►  Hub receives message          ──►  Client B
                                │                                   (subscribed to deploy:<uuid-2>)
                                │  Check subscriber list for
                                │  channel "deploy:<uuid-2>"
```

The Hub maintains a map:
```
channels: map[string]map[*Client]bool
// e.g., "deployment:<uuid>" → {client1: true, client2: true}
```

Each client maintains:
```
subscriptions: map[string]bool
// e.g., {"deployment:<uuid>": true, "project:<uuid>": true}
```
