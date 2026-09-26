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
5. **Deployment-Time Health Gating (Docker Restart Policy vs ForgeLAB Self-Healing):**
   - Health checks occur **only** during deployment promotion (`HEALTH_CHECKING`).
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

### 4. Docker Compose Development Stack Issues

The following issues affect the fully containerized Compose environment (`docker compose up --build`), which requires reconciliation before it can be treated as a verified end-to-end development environment:

#### a. Host Repository Inaccessibility in Containerized Backend
- **Location:** [`docker-compose.yml`](file:///c:/Users/estif/Desktop/ForgeLAB/docker-compose.yml) (`backend` service volumes)
- **Behavior:** The backend service mounts only `/var/run/docker.sock` and `forgelab_builds:/app/data/builds`. It does **not** mount arbitrary host filesystem paths.
- **Impact:** When a user enters a local host repository path (such as `C:\dev\my-app` or `/home/user/my-app`), the containerized Go backend's `PathValidator` and directory copy routines cannot access the directory because it exists only on the host filesystem outside the container. Local repository creation fails unless the backend runs directly on the host (**Environment A**) or an explicit bind mount is configured.
- **Guidance:** See [docs/11-development-environment.md](11-development-environment.md) for environment separation. A future enhancement could introduce a configurable source volume mount or an archive upload mechanism.

#### b. Frontend/Backend Container Networking & Rewrite Mismatch
- **Location:** [`frontend/next.config.js:L9-L14`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/next.config.js#L9-L14), [`docker-compose.yml`](file:///c:/Users/estif/Desktop/ForgeLAB/docker-compose.yml) (`frontend` service)
- **Behavior:** The Next.js configuration defines rewrites targeting `http://localhost:8080/api/:path*`:
  ```javascript
  {
    source: '/api/:path*',
    destination: 'http://localhost:8080/api/:path*',
  }
  ```
- **Impact:** Inside the `forgelab-frontend` container, `localhost:8080` resolves to the frontend container's loopback interface, **not** the `forgelab-backend` container (which resides at `http://backend:8080` on the internal Compose network). Consequently, proxy rewrites fail when both services are run inside Compose containers.
- **Guidance:** Future implementation must reconcile frontend/backend networking (for example, by supporting an environment variable such as `BACKEND_INTERNAL_URL` for server-side Next.js rewrites or introducing a unified gateway). Do not claim the fully containerized stack is physically verified end-to-end until this networking reconciliation is implemented and tested.

#### c. Compose Environment Isolation from Host `.env`
- **Location:** [`docker-compose.yml`](file:///c:/Users/estif/Desktop/ForgeLAB/docker-compose.yml) (`backend` service environment block)
- **Behavior:** `docker-compose.yml` specifies explicit static environment variables and does not declare `env_file: .env`.
- **Impact:** Variables configured in the host `.env` file (such as `FORGELAB_ALLOWED_SOURCE_ROOTS`) are **not** automatically injected into the backend container during `docker compose up`. Host `.env` values only apply when running the backend directly on the host.

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
