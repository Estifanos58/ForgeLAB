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

## 1. Current Implementation Limitations & Boundaries

These are deliberate scoping decisions for the current platform release. They are **not** accidental bugs.

1. **Universal Source Ingestion:**
   - ForgeLAB supports direct local-directory ingestion (`local_directory`), browser-based computer archive uploads (`local_upload` via `.zip`/`.tar.gz`), and direct GitHub repository imports (`github`) with encrypted OAuth tokens.
   - Direct local directory projects stream the Docker build context directly over an `io.Pipe()` with early `.dockerignore` pruning, eliminating browser uploads and disk duplication.
   - Archive uploads use Go's streaming `MultipartReader` into staged workspaces (`.uploads/<source_id>`) with asynchronous status polling via `GET /api/sources/{id}`.
   - Resumable/chunked multi-part upload protocols (such as tus.io) and multi-part pause/resume are explicitly deferred to post-MVP.
   - Arbitrary unauthenticated git clone URLs remain rejected for security reasons.
2. **Dynamic Direct Host Port Mapping:**
   - Deployed containers are exposed directly on dynamically allocated host ports (`10000–60000`).
   - There is no unified reverse proxy (e.g. Caddy/Nginx) providing virtual host routing, subdomains (e.g. `project-slug.localhost`), or SSL termination.
3. **Single Control-Plane Deployment Worker:**
   - The deployment worker runs as an in-process goroutine in the Go backend (`backend/cmd/server/main.go`).
   - Multi-node worker clusters or separate worker containers are deferred.
4. **Single-Owner Tenant Model:**
   - Projects and deployments belong strictly to the creating user (`owner_id`).
   - There are no teams, organizations, or Role-Based Access Control (RBAC) permissions.
5. **Deployment-Time Health Gating (Docker Restart Policy vs ForgeLAB Self-Healing):**
   - Health checks occur during deployment promotion (`HEALTH_CHECKING`) supporting `auto`, `http`, `tcp`, and `none`.
   - **Docker runtime restart behavior (`IMPLEMENTED`):** Deployed application containers are configured with `RestartPolicy: { Name: "unless-stopped" }` in [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go). The Docker daemon automatically restarts stopped or crashed containers at the container-engine layer.
   - **ForgeLAB continuous health monitoring / self-healing (`NOT IMPLEMENTED`):** ForgeLAB does not implement continuous runtime health polling, crash diagnosis, automatic rollback, health-based remediation, crash-loop detection, or automated re-promotion. If an application enters a broken state or fails long after reaching `RUNNING`, ForgeLAB control plane takes no automated remedial action.
6. **No Zero-Downtime Rolling Traffic Shift:**
   - Promotion stops the old container and starts traffic on the new port. Because there is no proxy layer to gracefully drain connections or shift HTTP traffic, a brief connection gap can occur.
7. **REST Pre-Fetch Required for Logs (No WebSocket Replay):**
   - Connecting to a WebSocket channel only streams events that occur *after* the connection is established.
   - To view prior build or startup logs, clients must explicitly query the REST API endpoint (`GET /api/projects/:id/deployments/:did/logs`).

---

## 2. Known Implementation Issues / Investigation Items

These are identified codebase behaviors that require investigation and resolution by future agents:

### 1. WebSocket Duplicate Event Delivery (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/websocket/hub.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/websocket/hub.go)
- **Resolution:** Resolved by making Redis the authoritative event distribution bus. When Redis is configured, `PublishEvent()` publishes exclusively to Redis (`forgelab:pubsub:<channel>`), and the Redis pub/sub listener (`listenRedisPubSub()`) delivers to local connected subscribers via `broadcastLocally()`. This ensures each subscriber receives events exactly once without duplicate delivery. If Redis is unconfigured or in tests, direct local delivery is used as a fallback.
- **Single-Event Framing & Persistent IDs:** Additionally resolved WebSocket batching issues by framing each queued event as an individual WebSocket TextMessage frame, and persisting database log IDs before emitting log events.

### 2. Project-Level Channel Producers (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go)
- **Resolution:** The deployment engine actively publishes `service_deployment_log`, `service_status_change`, and `project_status` events to `project:<uuid>` channels on the WebSocket Hub. Subscribing clients receive project-wide status transitions and log activity without polling.

### 3. Frontend Token Refresh & Session Persistence (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`frontend/src/lib/api/client.ts`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/src/lib/api/client.ts), [`frontend/src/proxy.ts`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/src/proxy.ts)
- **Resolution:** Authentication is backend-owned via HttpOnly cookies (`forgelab_access_token` and `forgelab_refresh_token`). The frontend client (`apiClient`) intercepts 401 responses, executes an atomic token refresh via `/api/auth/refresh`, and seamlessly retries the original request with circuit-breaker protection against infinite loops.

### 4. Docker Compose Networking & Rewrite Alignment (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`frontend/next.config.ts`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/next.config.ts), [`docker-compose.yml`](file:///c:/Users/estif/Desktop/ForgeLAB/docker-compose.yml)
- **Resolution:** `next.config.ts` dynamically configures rewrite destinations using `${process.env.BACKEND_INTERNAL_URL || 'http://backend:8080'}/api/:path*`, allowing transparent proxying between containers on the internal Docker Compose bridge network.

### 5. Platform-Dependent Ephemeral Storage Quotas
- **Location:** [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go), [`backend/internal/models/models.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/models/models.go)
- **Behavior:** CPU millicores (`NanoCPUs`), Memory MB (`Memory`), and PIDs limit (`PidsLimit`) are strictly enforced via Docker SDK `container.Resources`. However, container rootfs ephemeral storage quotas (`ephemeral_storage_mb`) require underlying filesystem storage driver support (e.g. Linux Docker Engine with `overlay2` or `zfs` backing filesystem with project quotas enabled).
- **Limitation:** On Windows and macOS Docker Desktop environments, container rootfs size quotas are not supported by the underlying storage driver. Ephemeral storage limits are persisted in `service_deployments.ephemeral_storage_mb` for auditing and future enforcement, but strict disk quotas are platform-dependent.

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
