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

### 5. Platform-Dependent Ephemeral Storage Quotas (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/trust_boundary.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/trust_boundary.go), [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go), [`backend/internal/models/models.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/models/models.go)
- **Behavior:** CPU millicores (`NanoCPUs`), Memory MB (`Memory`), and PIDs limit (`PidsLimit`) are strictly enforced via Docker SDK `container.Resources`. However, container rootfs ephemeral storage quotas (`ephemeral_storage_mb`) require underlying filesystem storage driver support (e.g. Linux Docker Engine with `overlay2` or `zfs` backing filesystem with project quotas enabled).
- **Resolution:** Rather than silently accepting unsupported ephemeral storage quotas, `ValidateAndBuildSecureHostConfig` truthfully enforces this trust boundary: if a service requests `ephemeral_storage_mb > 0`, it returns `ErrEphemeralStorageNotSupported`, failing closed before container creation rather than falsely claiming enforcement.

### 6. Host Mount Security & Docker Socket Rejection (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/volume_resolver.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/volume_resolver.go), [`backend/internal/docker/preflight.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/preflight.go), [`backend/internal/security/volume_security.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/security/volume_security.go)
- **Resolution:** Strict administrator-controlled allowlist enforced on all absolute host bind mounts via `FORGELAB_ALLOWED_SOURCE_ROOTS`, `FORGELAB_HOST_SOURCE_ROOT`, daemon paths, and project source roots. Host Docker socket mounts (`/var/run/docker.sock` and `\\.\pipe\docker_engine`) are unconditionally rejected in both preflight validation and runtime deployment. Project configurations can never self-authorize Docker socket access. In tenant deployment pipelines, forbidden host Docker socket mounts are automatically sanitized (stripped with an explicit security build log notice) before preflight validation, enabling Compose projects containing local Docker socket mounts to deploy safely as sandboxed workloads.

### 7. Atomic Redis Queue Recovery & Lease Ownership (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/queue/redis_queue.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/queue/redis_queue.go)
- **Resolution:** Replaced ambiguous lease-acquisition error handling with atomic Lua scripts (`reconcileLeaseErrorScript` and `removeDuplicateIfLeaseHeldScript`). When network timeouts or ambiguous responses occur, workers check if their attempted lease token acquired the lock; if so, execution proceeds; if held by another worker, duplicate copies are purged without altering running state; if orphaned, safely returned to pending. Active jobs cannot disappear, execute multiply, or reset incorrectly.

### 8. Strict Encryption Key Validation & Dev Key Rejection (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/crypto/encryption.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/crypto/encryption.go), [`backend/internal/config/config.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/config/config.go)
- **Resolution:** Replaced silent padding and truncation with strict 32-byte key size validation (`ParseKey`). In production mode, `ValidateProductionKey` rejects known development keys, template placeholders, and low-entropy keys (<= 4 unique bytes) at startup. Existing 32-byte raw and base64 encrypted data access is preserved.

### 9. Conservative Build Concurrency Defaults (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/config/config.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/config/config.go), [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go)
- **Resolution:** Replaced aggressive concurrency defaults with conservative CPU-aware limits: 1 slot on machines with <= 2 CPUs, 2 slots on <= 4 CPUs, and `cpus / 2` capped at 4 for larger systems. Retains administrator overrides via `FORGELAB_MAX_CONCURRENT_BUILDS` and exposes runtime build semaphore diagnostics via `GetBuildDiagnostics()`.

### 10. GitHub Authorization & PAT Verification (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/services/github_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/github_service.go)
- **Resolution:** Distinguishes OAuth tokens from fine-grained Personal Access Tokens. Fine-grained PATs verify actual repository access via `GET /user/repos?per_page=1`. OAuth tokens require explicit `repo` scope and are no longer assumed to have access when scopes are empty or unspecified.

### 11. Incremental Deployment Engine Decomposition (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine_health.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_health.go), [`backend/internal/docker/engine_lifecycle.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_lifecycle.go), [`backend/internal/docker/engine_build.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_build.go), [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go)
- **Resolution:** Incrementally decomposed the monolithic deployment engine by extracting health checks and exit diagnostics (`engine_health.go`), container lifecycle and log collectors (`engine_lifecycle.go`), and build slot concurrency, image building, and DAG tier calculation (`engine_build.go`) without modifying deployment pipelines or breaking rollback guarantees.

### 12. Shared-Build Coordination & Fail-Safe Promise Completion (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/shared_build_coordinator.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/shared_build_coordinator.go), [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go)
- **Resolution:** Extracted shared-build artifact coordination from the engine into a dedicated `SharedBuildCoordinator` and `SharedBuildPromise`. Protects promise resolution with `sync.Once` and installs a defer-guarded fail-safe on leader promises to guarantee that follower services are never left waiting indefinitely upon build errors, semaphore acquisition timeouts, tar streaming failures, or deployment cancellation. Computes deterministic cache keys incorporating project ID, revision, context, Dockerfile, build strategy, target stage, canonical sorted build args, runtime type, internal port, start command, and package manager. Clean up safely removes completed records per project while leaving in-flight builds untouched.

### 13. CPU-Aware Concurrency & Memory Spike Elimination (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/semaphore.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/semaphore.go), [`backend/internal/docker/builder.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/builder.go), [`docker-compose.yml`](file:///c:/Users/estif/Desktop/ForgeLAB/docker-compose.yml)
- **Resolution:** Removed hardcoded concurrency defaults (`FORGELAB_MAX_CONCURRENT_BUILDS: 2`) from Compose definitions so `DefaultMaxConcurrentBuilds()` dynamically assigns CPU-aware limits (1 on <=2 CPUs, 2 on <=4 CPUs, up to 4 on larger systems). Replaced RAM-buffered Dockerfile peeking with a seekable, auto-cleaning disk temporary file, eliminating 100MB–500MB memory spikes during context preparation on Windows and Docker Desktop.

### 14. Production Allowed Source Roots & Fail-Closed Path Mappings (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/config/config.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/config/config.go), [`backend/internal/security/path_validator.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/security/path_validator.go)
- **Resolution:** In production mode (`isProd`), server startup strictly requires `FORGELAB_ALLOWED_SOURCE_ROOTS` to be explicitly configured. Host-to-container and container-to-daemon path mappings now fail closed with `ErrPathNotAllowed` on unmapped or escaping paths. Container source boundary traversal is strictly blocked while preserving legitimate relative imports.

### 15. Compose Validation & Mandatory Secrets Protection (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/discovery/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/discovery/engine.go), [`backend/internal/discovery/compose.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/discovery/compose.go), [`backend/internal/dockerignore/dockerignore.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/dockerignore/dockerignore.go), [`backend/internal/tararchive/tararchive.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/tararchive/tararchive.go)
- **Resolution:** Malformed Compose files and files with empty services now fail closed with descriptive errors rather than silently falling back to heuristics. Warns users about unsupported Compose features (e.g. `secrets`, `configs`, `profiles`, `deploy`). Enforced mandatory secrets protection (`.env`, `.env.*`, `*.pem`, `*.key`, `id_rsa*`, etc.) that cannot be un-ignored or exposed even if custom `.dockerignore` files define `!.env`. Bounded manifest discovery reads to 64 files and 2MB total.

### 16. Continuous Runtime Health Monitoring & Stuck Deployment Reconciliation (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine_lifecycle.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_lifecycle.go), [`backend/internal/services/deployment_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/deployment_service.go), [`backend/cmd/server/main.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/cmd/server/main.go)
- **Resolution:** Periodic reconciler continuously monitors running containers via `CheckContinuousRuntimeHealth`, detecting unexpected crashes, crash loops, and kernel OOM kills (`insp.State.OOMKilled`) with actionable failure logs. Fixed orphan reconciler query to monitor both parent deployment IDs and service deployment IDs without restrictive null constraints. Extended `ReconcileQueuedDeployments` to query and re-enqueue orphaned records from the `deployments` table stuck in `queued` status.

### 17. Reverse-Proxy Preparation & Project Resource Quotas (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/models/models.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/models/models.go), [`backend/internal/proxy/reverse_proxy.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/proxy/reverse_proxy.go), [`backend/internal/docker/engine_lifecycle.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_lifecycle.go)
- **Resolution:** Added custom domain validation and automated Caddyfile generation supporting custom domains and HTTPS routing. Added `ValidateProjectResourceQuota` enforcing project-level CPU and memory caps. Implemented `PruneOldProjectImages` to prune stale Docker images while preserving recent images for instant rollbacks.

### 18. Hardened GitHub Source Import Quotas & Atomic Cache (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/services/github_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/github_service.go), [`backend/internal/services/source_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/source_service.go)
- **Resolution:** Hardened GitHub import against zip-bombs and decompression attacks with cumulative limits: 100MB compressed, 100MB uncompressed, 10,000 files, 50MB single file, and 3-minute extraction timeout. Rejects path traversal and dangerous archive entry types. Implemented atomic cache directory creation via unique `.tmp.<uuid>` directories validated by `.complete` marker before atomic renaming, with strict `0700` directory and `0600` file permissions. Failed or aborted extractions are cleaned up reliably via deferrals.

### 19. Local Agent Privacy & Loopback Hardening (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/cmd/agent/main.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/cmd/agent/main.go), [`backend/internal/agent/server.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/agent/server.go), [`start-agent.ps1`](file:///c:/Users/estif/Desktop/ForgeLAB/start-agent.ps1)
- **Resolution:** Local agent defaults strictly to loopback `127.0.0.1` binding to prevent unauthorized LAN exposure. The `/api/agent/status` endpoint scrubs internal `agent_id` and active session counts for unauthenticated requests, preventing credential discovery and enumeration while returning online status and system architecture.

### 20. Direct Disk Build Context Streaming & Zero-Memory Buffering (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine_build.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_build.go)
- **Resolution:** Removed unnecessary `io.ReadAll` buffering of build contexts up to 512 MB. Validated archives stream directly from disk to the Docker daemon. To preserve required HTTP semantics and prevent chunked transfer encoding issues over Windows named pipes (`//./pipe/docker_engine`), a custom `buildStreamRoundTripper` sets `req.ContentLength` from the context archive file size and removes `Transfer-Encoding: chunked`. Log streaming lines in `parseDockerStream` remain bounded to 64KB.

### 21. Container Network Namespace Isolation (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/trust_boundary.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/trust_boundary.go), [`backend/internal/discovery/compose.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/discovery/compose.go)
- **Resolution:** `ValidateAndBuildSecureHostConfig` actively rejects `container:` network namespace sharing (`ErrContainerNetworkSharingRejected`), preventing inter-container cross-boundary tampering or network interception. Compose parsing emits an explicit security warning and maps services to isolated bridge networks.

### 22. Local Agent Filesystem Path Leak Resolution (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/agent/server.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/agent/server.go)
- **Resolution:** Eliminated exposure of internal host filesystem paths (`source_path: session.CanonicalPath`) from local agent session responses (`renderSessionResponse`). Responses now expose only safe public session metadata, preserving path privacy.

### 23. SharedBuildCoordinator Concurrency & Retry Safety (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/shared_build_coordinator.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/shared_build_coordinator.go), [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go)
- **Resolution:** Thread-safe singleton access for `SharedBuildCoordinator` via engine mutex. `GetOrInit` evicts previously failed build promises so subsequent deployment retries attempt a fresh build rather than immediately re-failing on stale cached errors. `SharedBuildPromise` access uses synchronized `Complete()` and `Result()` methods to prevent concurrent map/field race conditions.

### 24. BuildKit Session Fallback & Concurrency Separation (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine_build.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_build.go), [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go)
- **Resolution:** If BuildKit fails due to missing or unsupported client gRPC sessions (`no active session`), `buildImage` automatically falls back to the standard daemon builder (`types.BuilderV1`) by reopening the staged archive from disk. Context preparation (agent transfers, tar archive creation) runs concurrently without acquiring the Docker build slot; build slots are acquired only when Docker image building begins.

### 25. Immutable GitHub Revision Pinning & Error-Propagating Fingerprints (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/services/deployment_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/deployment_service.go), [`backend/internal/docker/engine_build.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_build.go), [`backend/internal/discovery/fingerprint.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/discovery/fingerprint.go)
- **Resolution:** GitHub deployments strictly resolve to immutable 40-character commit SHAs; deployment creation and release execution fail closed if the SHA cannot be acquired, prohibiting fallback to mutable branch names. Replaced metadata-only directory fingerprints with reliable content-based fingerprints backed by metadata caching, and propagated filesystem errors instead of silently skipping files.

### 26. Configuration Hardening & Secret Protection (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`docker-compose.yml`](file:///c:/Users/estif/Desktop/ForgeLAB/docker-compose.yml), [`backend/internal/services/source_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/source_service.go)
- **Resolution:** Docker Compose explicitly passes `APP_ENV: ${APP_ENV:-development}` to the backend service, activating production security validations in production mode. Removed the hard-coded default encryption key fallback in `SourceService`, failing closed if encryption is unavailable. External port exposures in Docker Compose default to loopback `127.0.0.1` (`${BACKEND_PORT_BINDING:-127.0.0.1:8080}:8080`, `${FRONTEND_PORT_BINDING:-127.0.0.1:3000}:3000`).

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
