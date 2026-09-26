# ForgeLAB — Known Limitations & Issue Backlog

**Status:** Current Reference Specification  
**Purpose:** Ensure future AI coding agents understand deliberate boundaries, known bugs, and deferred features, preventing premature refactors or false assumptions.  

---

## Related Documents

- [INSTRUCTION.md](../INSTRUCTION.md) — Operational memory & mandatory agent workflow
- [docs/00-current-state.md](00-current-state.md) — Current state snapshot & matrix
- [docs/05-security-trust-boundaries.md](05-security-trust-boundaries.md) — Security model & trust boundaries
- [docs/06-websocket-contract.md](06-websocket-contract.md) — WebSocket architecture & known duplicate delivery
- [docs/13-decisions.md](13-decisions.md) — Architecture Decision Records (ADRs)

---

## 1. Current Implementation Limitations (MVP Boundaries)

These are deliberate scoping decisions for the MVP vertical slice. They are **not** accidental bugs.

1. **Host Filesystem Source Only (`source_type: "local"`):**
   - ForgeLAB imports projects from absolute directory paths on the host filesystem.
   - It does not currently support browser-based ZIP archive uploads (`LOCAL_UPLOAD`) or remote git URL imports.
2. **Dynamic Direct Host Port Mapping:**
   - Deployed containers are exposed directly on dynamically allocated host ports (`10000–60000`).
   - There is no unified reverse proxy (e.g. Caddy/Nginx) providing virtual host routing, subdomains (e.g. `project-slug.localhost`), or SSL termination.
3. **Single Control-Plane Deployment Worker:**
   - The deployment worker runs as an in-process goroutine in the Go backend (`backend/cmd/server/main.go`).
   - Multi-node worker clusters or separate worker containers are deferred.
4. **Single-Owner Tenant Model:**
   - Projects and deployments belong strictly to the creating user (`owner_id`).
   - There are no teams, organizations, or Role-Based Access Control (RBAC) permissions.
5. **Deployment-Time Health Gating (No Continuous Self-Healing):**
   - Health checks occur **only** during deployment promotion (`HEALTH_CHECKING`).
   - If an application crashes 30 minutes after reaching `RUNNING`, ForgeLAB does not automatically restart it, detect the crash, or trigger an automated rollback.
6. **No Zero-Downtime Rolling Traffic Shift:**
   - Promotion stops the old container and starts traffic on the new port. Because there is no proxy layer to gracefully drain connections or shift HTTP traffic, a brief connection gap can occur.
7. **REST Pre-Fetch Required for Logs (No WebSocket Replay):**
   - Connecting to a WebSocket channel only streams events that occur *after* the connection is established.
   - To view prior build or startup logs, clients must explicitly query the REST API endpoint (`GET /api/projects/:id/deployments/:did/logs`).

---

## 2. Known Implementation Issues / Investigation Items

These are identified codebase behaviors that require investigation and resolution by future agents:

### 1. WebSocket Duplicate Event Delivery
- **Location:** `backend/internal/websocket/hub.go:L123-L184`
- **Behavior:** `PublishEvent(channel, event)` performs two actions:
  1. Sends the JSON payload directly to all local clients in `h.channels[channel]`.
  2. Publishes the same JSON payload to Redis (`forgelab:pubsub:<channel>`).
  3. The same backend instance's `listenRedisPubSub()` receives the message from Redis and delivers it a **second time** to the same local subscribers in `h.channels[channel]`.
- **Impact:** Connected clients receive duplicate log lines and duplicate `status_change` frames.
- **Guidance:** In single-node deployments, either distribute exclusively via Redis Pub/Sub, or attach an instance identifier (`sender_id`) to Redis messages so the local node ignores messages it generated.

### 2. Project-Level Channel Has No Active Producers
- **Location:** `backend/internal/websocket/hub.go:L347-L349`
- **Behavior:** The Hub implements subscription handling and authorization for `project:<uuid>` channels. However, grep analysis reveals that `PublishEvent` is **never called** for project channels in any backend service.
- **Impact:** Subscribing to `project:<uuid>` will succeed but will never receive any events.
- **Guidance:** Future work should either emit project lifecycle events (e.g. `deployment_created`, `project_status_updated`) to `project:<uuid>` or remove the unused channel type.

### 3. Missing Frontend Token Refresh Loop
- **Location:** `frontend/src/lib/api.ts`
- **Behavior:** The frontend stores the access token in `localStorage`. Access tokens expire after 15 minutes (`JWT_ACCESS_TOKEN_EXPIRY`). When the access token expires, API calls return `401 Unauthorized`, causing the frontend to immediately redirect the user to `/login`.
- **Impact:** Active users are logged out every 15 minutes, even though a valid 7-day refresh token exists in the database and cookies.
- **Guidance:** Implement an HTTP response interceptor in `api.ts` that catches 401 responses, calls `POST /api/auth/refresh`, updates `localStorage`, and retries the failed request before redirecting to `/login`.

---

## 3. Security Hardening Items (Deferred Backlog)

1. **`localStorage` Token Storage:**
   - Tokens stored in `localStorage` (`forgelab_token`) are vulnerable to cross-site scripting (XSS).
   - Hardening: Transition client-side authentication to use `HttpOnly`, `Secure`, `SameSite=Lax` cookies exclusively.
2. **WebSocket Query String Token:**
   - The WebSocket hook passes the JWT via URL query parameter (`?token=...`).
   - Hardening: Authenticate WebSocket upgrades using the `forgelab_access_token` HttpOnly cookie or an initial authentication JSON frame after socket open.
3. **Permissive WebSocket CORS / Origin Check:**
   - In `backend/internal/websocket/hub.go:L24-L26`, `upgrader.CheckOrigin` returns `true` unconditionally.
   - Hardening: Restrict allowed origins to configured domains (e.g. `http://localhost:3000`).
4. **Backend Docker Socket Trust:**
   - The Go backend mounts `/var/run/docker.sock` with root-equivalent Docker Engine control.
   - Hardening: Explore Docker rootless mode or a restricted daemon proxy to reduce blast radius.
5. **Container Resource Limits:**
   - Containers are created without memory or CPU caps (`container.HostConfig.Resources` is unconfigured).
   - Hardening: Provide configurable memory (`Memory`) and CPU (`NanoCPUs`) quotas per project.

---

## 4. Future Platform Capabilities (Explicitly Post-MVP)

Do not implement these capabilities during current MVP verification:

- **GitHub OAuth Integration & Repository Selection:** Documented in [docs/15-github-integration.md](15-github-integration.md).
- **Automated Webhooks:** Push-to-deploy triggers from GitHub.
- **Caddy Reverse Proxy:** Automatic subdomain routing (`<slug>.forgelab.local`) and Let's Encrypt TLS.
- **Continuous Runtime Health Monitoring:** Background crash-loop detection and auto-recovery.
- **Zero-Downtime Deployments:** Blue/green traffic switching.
- **OpenTelemetry:** Distributed tracing and container metrics export.
- **Role-Based Access Control (RBAC):** Organization-level user permissions.
- **ForgeLAB CLI:** Command-line tooling (`forge deploy`).
- **Distributed Multi-Node Workers:** Offloading Docker builds to external runner nodes.
