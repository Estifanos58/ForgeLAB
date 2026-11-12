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
2. **Dynamic Direct Host Port Mapping & Loopback Security:**
   - Deployed containers are bound to dynamically allocated host ports (`10000–60000`) on loopback `127.0.0.1` by default via `FORGELAB_APP_HOST_IP`.
   - Direct LAN/WAN exposure of container host ports is disabled by default to prevent attackers from bypassing authentication or access controls.
   - For public external access, ForgeLAB provides the Secure Public Project Sharing architecture using an authenticated outbound Yamux tunnel to a public gateway (`cmd/gateway`).
3. **Single Control-Plane Deployment Worker:**
   - The deployment worker runs as an in-process goroutine in the Go backend (`backend/cmd/server/main.go`).
   - Multi-node worker clusters or separate worker containers are deferred.
4. **Single-Owner Tenant Model:**
   - Projects and deployments belong strictly to the creating user (`owner_id`).
   - There are no teams, organizations, or Role-Based Access Control (RBAC) permissions.
5. **Deployment-Time Health Gating, Self-Healing & Auto-Rollback:**
   - Health checks occur during deployment promotion (`HEALTH_CHECKING`) supporting `auto`, `http`, `tcp`, and `none`.
   - **Docker runtime restart behavior (`IMPLEMENTED`):** Deployed application containers are configured with `RestartPolicy: { Name: "unless-stopped" }` in [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go). The Docker daemon automatically restarts stopped or crashed containers at the container-engine layer.
   - **ForgeLAB continuous runtime health monitoring (`IMPLEMENTED`):** The background deployment reconciler periodically inspects running containers via `CheckContinuousRuntimeHealth` in [`backend/internal/docker/engine_runtime_health.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_runtime_health.go), detecting unexpected container exits, kernel OOM kills (`insp.State.OOMKilled`), restart loops, and Docker health-check degradation (`healthy` / `unhealthy`).
   - **ForgeLAB configurable self-healing & auto-rollback (`IMPLEMENTED`):** Bounded container restart self-healing (`FORGELAB_SELF_HEAL_ENABLED`, `FORGELAB_SELF_HEAL_MAX_RESTARTS`), sliding-window crash-loop detection (`FORGELAB_CRASH_LOOP_THRESHOLD`, `FORGELAB_CRASH_LOOP_WINDOW`), and automatic rollback to the prior known-good image (`FORGELAB_AUTO_ROLLBACK_ENABLED`, `FORGELAB_AUTO_ROLLBACK_THRESHOLD`, `FORGELAB_AUTO_ROLLBACK_COOLDOWN`) run continuously when enabled, recording audit trail entries and never silently overwriting healthy releases. Multi-node external orchestration remains out of scope.
6. **Zero-Downtime Rolling Traffic Shift (Opt-In Blue-Green Implemented; Standard Mode Direct Cutover):**
   - By default (`deployment_mode: "standard"`), service promotions bind dynamically allocated host ports (`10000–60000`) and replace the prior container once health checks pass; a brief connection gap can occur for direct host-port callers.
   - Services configured with `deployment_mode: "blue_green"` (`PATCH /api/projects/{id}/services/{serviceId}/deployment-mode`) start and health-verify the replacement container before atomically cutting over routes in `TrafficRouter` (`/proxy/services/{serviceId}/*`) and syncing Caddy routes (`SetOnRouteChange`) prior to retiring the old container.
7. **REST Pre-Fetch & WebSocket Sequence Replay (`RESOLVED IN ARCHITECTURE`):**
   - Historical logs are fetched via REST endpoints (`GET /api/projects/:id/logs` or `GET /api/projects/:id/deployments/:did/logs`) with subquery-based recent log ordering (`ORDER BY id DESC LIMIT N` wrapped in `ORDER BY id ASC`).
   - On WebSocket reconnection, clients transmit `last_sequence` with their subscription frame, triggering the backend WebSocket Hub to stream missing historical logs from PostgreSQL before live events, preventing gaps or duplicates.
8. **Public Project Sharing & Outbound Tunnel Architecture:**
   - **Default Domainless Sharing (Cloudflare Quick Tunnels):** Enables users to share applications via `https://<random>.trycloudflare.com` without owning a domain, configuring DNS, opening firewall ports, or registering for Cloudflare.
     - *Quick Tunnel Boundaries:* Ephemeral URLs (a new random URL is generated every time a share is created); shared rate limits and concurrent-connection ceilings on Cloudflare's free edge tier; Server-Sent Events (SSE) buffering (not recommended for SSE; WebSockets and standard HTTP request/response are fully supported).
     - *Local Machine Prerequisite:* Requires `cloudflared` installed on the host. If missing, ForgeLAB gracefully reports `unavailable` with actionable package manager install commands.
   - **Self-Hosted Public Gateway (Optional / Advanced):** For organizations with their own domain and public server, the standalone gateway (`cmd/gateway`) provides authenticated multiplexed previews (`*.preview.yourdomain.com`). Handshake authentication uses constant-time secret comparison and bounded 5s registration acknowledgments.
   - **Hardcoded Localhost in Client Bundles:** Applications with hardcoded localhost URLs in compiled frontend JavaScript (e.g. client-side `fetch("http://localhost:8080/api")`) cannot be reconfigured dynamically at runtime. Client-side code should use relative paths (e.g. `/api/*`) or standard build-time environment variables (`NEXT_PUBLIC_API_URL`).

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

### 4. Docker Compose Networking, Next.js Proxy & CSP Alignment (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`frontend/src/app/api/[[...path]]/route.ts`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/src/app/api/%5B%5B...path%5D%5D/route.ts), [`frontend/next.config.ts`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/next.config.ts), [`docker-compose.yml`](file:///c:/Users/estif/Desktop/ForgeLAB/docker-compose.yml)
- **Resolution:** Frontend API requests proxy transparently via the App Router catch-all handler (`route.ts`) to `${BACKEND_INTERNAL_URL || 'http://backend:8080'}` while stripping untrusted client forwarding headers, paired with a hardened Content-Security-Policy (`'unsafe-eval'` omitted outside development) and security headers in `next.config.ts`.

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

### 27. Runtime Log Collector Lifecycle & Reconnection Replay (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine_lifecycle.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_lifecycle.go), [`backend/internal/services/deployment_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/deployment_service.go), [`backend/internal/websocket/hub.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/websocket/hub.go), [`frontend/src/features/projects/logs-section.tsx`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/src/features/projects/logs-section.tsx)
- **Resolution:** Runtime log collectors now run with an independent root context decoupled from deployment execution, continuing after deployment returns. Collectors stop explicitly when their container is stopped, replaced, removed, or during control-plane shutdown. Collectors execute up to 5 bounded retries with backoff if streams drop while containers are running. On backend restart, `ReconcileDaemonContainers` restores collectors for all running multi-service and legacy containers. Log retrieval uses subquery ordering (`ORDER BY id DESC LIMIT N` wrapped in `ORDER BY id ASC`), ensuring latest production logs are returned in chronological order rather than truncated by initial build lines. On WebSocket reconnect with `last_sequence`, missing logs are replayed from PostgreSQL. Frontend memory growth is bounded to 2,000 entries (`MAX_STORED_LOGS`).

### 28. Docker Stream Hardening, Collector Concurrency, Diagnostics & Retention (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/logging/docker_stream.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/logging/docker_stream.go), [`backend/internal/docker/log_collector_registry.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/log_collector_registry.go), [`backend/internal/handlers/health_handler.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/handlers/health_handler.go), [`backend/internal/services/deployment_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/deployment_service.go)
- **Resolution:**
  - *Stream Desynchronization:* Hardened `DemuxDockerStream` against oversized frames (>8MB) by consuming excess payload bytes via `io.CopyN(io.Discard, ...)` keeping the reader header-aligned.
  - *Memory Bounding:* Replaced raw payload allocation with 32KB streaming chunk buffers. Flushes newline-free output in bounded 64KB chunks (`DefaultMaxLineLength`) and safely truncates messages exceeding 64KB before persistence.
  - *Race-Safe Collector Registry:* Introduced `LogCollectorRegistry` with UUID instance IDs. During container redeployments, unregistering predecessor collectors cannot remove or cancel newer replacement collectors mapped to the same container key.
  - *Operational Observability:* Exposed operational health diagnostics via `GET /health/logs` and `GET /ready` (`active_collectors`, `lines_collected`, `last_activity`, `collection_errors`, `persistence_errors`) with sanitized secret masking.
  - *Configurable Log Retention:* Implemented `DeploymentService.PruneLogs` with age-based (`FORGELAB_LOG_RETENTION_DAYS`, default 30) and per-deployment count-based (`FORGELAB_LOG_MAX_PER_DEPLOYMENT`, default 10,000) cleanup, scheduled via a background ticker (`FORGELAB_LOG_CLEANUP_INTERVAL`, default 1h).

### 29. GitHub Repository Importing & Authorization Architecture (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/services/repository_import_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/repository_import_service.go), [`backend/internal/services/github_app.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/github_app.go), [`backend/internal/services/github_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/github_service.go), [`frontend/src/components/dashboard/create-project-modal.tsx`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/src/components/dashboard/create-project-modal.tsx)
- **Defects Fixed:**
  - Removed all fake repository metadata (`id: Date.now()`) and synthetic default-branch fallbacks (`main`). Network and authorization failures strictly return actionable errors instead of masking missing permissions.
  - Fixed frontend session refresh: `/api/integrations/github` is no longer excluded from automatic 401 token refresh retries.
  - Distinct state handling: UI and backend distinguish connected, reauthorization-required (`needs_reauth`), SAML/SSO required (`sso_required`), and repository access denied (`not_found` / `404`).
  - Public repository imports: Anonymous visitors (Guest Mode) and registered users can import public GitHub repositories without connecting GitHub accounts (`source_type: "github_public"`).
- **GitHub App Authorization:**
  - Preferred access mechanism using GitHub App with read-only **Contents** and **Metadata** permissions.
  - Backend signs RS256 JWTs using configured private key, mints short-lived installation access tokens, and validates installation ownership server-side.
  - Backward compatibility: Retains encrypted OAuth and Personal Access Token (PAT) storage via AES-256-GCM.
- **Durable Import Pipeline & Immutable Snapshot Reuse:**
  - Background asynchronous import pipeline tracked via `repository_imports` table (`pending` → `verifying` → `resolving` → `downloading` → `extracting` → `discovering` → `ready`).
  - Resolves exact 40-character commit SHA and downloads one tarball archive into isolated workspace (`data/sources/imports/<id>`).
  - The discovery engine scans the snapshot, produces DAG tiers and service definitions, and generates an authoritative deployment plan pinned to the resolved commit SHA.
  - Project creation transfers the immutable snapshot directory reference to `sources.metadata["snapshot_path"]`.
  - Docker deployment engine reuses the snapshot directory directly without making any secondary network or GitHub requests.
  - Enforces archive security bounds: 100MB compressed, 100MB uncompressed, 10,000 files, 50MB single file, traversal and symlink guards.
  - Periodic background ticker prunes expired or abandoned import snapshots and database records after 24 hours.

### 30. Authoritative Dependency Graph Validation & Silent Fallback Elimination (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/discovery/graph.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/discovery/graph.go), [`backend/internal/discovery/compose.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/discovery/compose.go), [`backend/internal/discovery/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/discovery/engine.go), [`backend/internal/docker/preflight.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/preflight.go), [`backend/internal/docker/engine_build.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_build.go), [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go)
- **Resolution:**
  - Replaced disparate cycle checking and heuristic graph parsing with a single authoritative dependency-graph implementation (`ValidateAndBuildDAG`).
  - Strict validation rejects cyclic service dependencies with explicit directed cycle paths (e.g. `svc-a -> svc-b -> svc-c -> svc-a`), self-dependencies, unknown service references, and unsupported dependency conditions before building images or creating containers.
  - Plan generation (`GeneratePlan`) and release execution (`buildServiceDeploymentTiers`) fail closed immediately on graph errors without falling back to flawed tiers containing cyclic services.
  - Generates deterministic tiers using Kahn's algorithm with alphabetical sorting within each tier, and deterministically honors `service_started`, `service_healthy`, and `service_completed_successfully` dependency conditions.

### 31. Compose Network Isolation, Alias Preservation & Fatal Attachment Handling (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go), [`backend/internal/docker/engine_lifecycle.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_lifecycle.go)
- **Resolution:**
  - Preserves declared Compose network configuration, including user-defined network membership, driver, `internal`, and supported `external` settings.
  - When services declare explicit networks, they are isolated to those declared networks and are not attached to the common project bridge network (`forgelab-net-<projectID>`).
  - Preserves default service-name DNS aliases and custom aliases across all networks to which the service belongs.
  - Treats network creation failures or secondary network attachment failures as fatal deployment failures (fails closed, cleans up the container, restores the previous running container, and records failure diagnostics), never demoting network failures to non-fatal warnings.

### 32. Target Database Credential Resolution, URL Encoding & Secret Redaction (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go), [`backend/internal/logging/redactor.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/logging/redactor.go)
- **Resolution:**
  - Standardized connection string resolver derives target database credentials directly from the target database service's configuration and decrypted secrets (`secretService.GetDecryptedEnvMap`), not from the consuming service's environment.
  - Encodes connection credentials using standard library `url.UserPassword` to safely percent-encode special characters.
  - Disambiguates multiple infrastructure services: generic `DATABASE_URL` and `REDIS_URL` are only populated when exactly one corresponding infrastructure service exists. Multiple databases/caches provide explicit `<SERVICE>_URL`, `<SERVICE>_HOST`, `<SERVICE>_PORT` variables to prevent ambiguous automatic selection.
  - Dynamically registers generated connection URLs and database passwords with the log redactor, guaranteeing zero plaintext secret exposure in plans, logs, API responses, or diagnostics.
  - Resolves client-facing frontend variables (`NEXT_PUBLIC_API_URL`, `VITE_API_URL`, `API_URL`) to public URLs, custom domains, or host port bindings, never private Docker DNS names (`backend:8080`).

### 33. Strict Health Check Status Gating & Readiness Diagnostics (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine_health.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_health.go)
- **Resolution:**
  - Explicit HTTP health checks require HTTP 2xx or 3xx responses (status 200–399) and reject HTTP 404 and 5xx responses.
  - HTTP health checks never silently fall back to TCP socket inspection upon HTTP failure.
  - Keeps HTTP, TCP, Docker-native, Auto, and None health check strategies behaviorally distinct.
  - Deployment status and diagnostics clearly distinguish process startup, listening ports, application readiness, and dependency connectivity.

### 34. Release Promotion Container Preservation & Rollback Truthfulness (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go)
- **Resolution:**
  - In release deployments, previous healthy service containers are preserved throughout execution across all tiers and are only superseded/cleaned up after the full release meets promotion criteria.
  - When partial failures occur (some services succeed while others fail), ForgeLAB accurately logs that individual services were updated and explicitly disclaims full release rollback, preventing misleading rollback claims.

### 35. Unified Volume Mount Resolution Across Remote and Local Source Types (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/volume_resolver.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/volume_resolver.go), [`backend/internal/services/source_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/source_service.go), [`backend/internal/services/project_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/project_service.go)
- **Defects Fixed:**
  - Resolved preflight validation failure (`bind mount source directory '.' (resolved to '') does not exist or is not accessible from backend environment`) when deploying imported GitHub repositories and archive uploads.
  - Unified `ResolveProjectSourcePaths` and `ResolveAndValidateVolumeMount` across all source origins (`local_directory`, `local_agent`, `local_upload`, `github`, and `github_public`).
  - Remote GitHub imports and archive uploads locate their immutable snapshot directories (`/app/data/sources/imports/...` and `/app/data/sources/<id>`) via source metadata, database records, and repository paths.
  - Implemented dynamic daemon host path translation (`translateBackendPathToDaemon` and `DetectAllBackendContainerMounts`) to translate container storage volume paths to host paths visible to the Docker daemon (`/var/lib/docker/volumes/...`).
  - Permitted boundary-safe automatic directory creation for relative project subpaths (e.g. `./data`) during preflight while strictly maintaining path-escape rejections, restricted directory checks, and Docker socket mount bans.

### 36. WebSocket Query String Token Removal & Single-Use Ticket Protocol (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/websocket/hub.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/websocket/hub.go), [`backend/cmd/server/main.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/cmd/server/main.go)
- **Resolution:** Removed token authentication via URL query parameters (`?token=...`), which leaked JWT credentials into server/proxy access logs and browser histories. Connection requests containing `?token=` are strictly rejected with `401 Unauthorized`. Implemented short-lived (60s TTL), single-use tickets minted via `POST /api/ws/ticket` and consumed via `GET /api/ws?ticket=<uuid>`. Replayed or expired tickets are rejected with `401 Unauthorized`. Also supported authentication via secure HttpOnly `forgelab_access_token` cookies and `Authorization: Bearer <token>` headers.

### 37. Enforced WebSocket Origin Policies (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/websocket/hub.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/websocket/hub.go)
- **Resolution:** Replaced unconditional `CheckOrigin: func(...) { return true }` with strict origin verification. All WebSocket upgrade requests validate the incoming `Origin` header against configured frontend URLs (`CORS_ALLOWED_ORIGINS` and `FRONTEND_URL`) and same-origin host matching, eliminating cross-site WebSocket hijacking (CSWSH) vulnerabilities.

### 38. Restricted Internal Diagnostics Access (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/handlers/health_handler.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/handlers/health_handler.go), [`backend/cmd/server/main.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/cmd/server/main.go)
- **Resolution:** Public `/ready` health probe no longer leaks internal operational details (`queue`, `build_queue`, `log_collectors`) to unauthenticated external clients; external callers receive a sanitized readiness status checking core dependencies. Detailed queue metrics and container log collector diagnostics via `/ready`, `/health/queue`, and `/health/logs` are restricted strictly to trusted loopback/internal callers or authenticated administrators, returning `401 Unauthorized` for external unauthenticated requests.

### 39. Docker Host Port Conflict Retries & In-Transition Reconciliation (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go), [`backend/internal/docker/engine_port.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_port.go), [`backend/internal/docker/engine_lifecycle.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_lifecycle.go)
- **Resolution:** Implemented automatic conflict detection and retry logic (up to 3 retries) during container creation and startup. When Docker Engine reports that an allocated port is already bound on the host (`isPortConflictError`), ForgeLAB registers the conflicted port as used in `PortManager`, allocates a fresh port, updates port bindings, and reattempts creation/start. In addition, `ReconcileDaemonContainers` collects host ports from containers across `running`, `restarting`, `created`, and `paused` states, preventing collisions during service transitions.

### 40. Build Reproducibility & Source Revision Fingerprint Verification (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go), [`backend/internal/docker/engine_build.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_build.go)
- **Resolution:** Pinned immutable source revisions across both release and independent service deployments. For local directory deployments, when a source revision fingerprint is pinned (`fp_...`), `resolveSourceDirectory` computes a fresh content-based fingerprint (`ComputeDirectoryContentFingerprintWithContext`) and fails closed with an informative error (`source content changed since deployment was created`) if disk files drift from the reviewed deployment revision before build execution.

### 41. Remote Public Host URL Resolution (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/config/config.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/config/config.go), [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go), [`backend/cmd/server/main.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/cmd/server/main.go)
- **Resolution:** Configured `PublicHost` via `FORGELAB_PUBLIC_HOST` or `PUBLIC_HOST` (falling back to host from `FRONTEND_URL`), preventing remote deployments from erroneously generating `localhost` browser URLs.

### 42. Local Agent Endpoint Identity Verification & Token Revocability (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/agent/server.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/agent/server.go), [`backend/internal/agent/client.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/agent/client.go), [`backend/internal/agent/session.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/agent/session.go), [`backend/internal/handlers/source_handler.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/handlers/source_handler.go)
- **Resolution:** Added preflight unauthenticated identity verification probes (`POST /api/agent/verify-identity` and `GET /api/agent/status` with `X-Expected-Agent-ID`). The control plane verifies endpoint identity BEFORE transmitting bearer credentials, preventing session tokens from ever being sent to unverified fallback endpoints. Tokens default to a short 15-minute TTL (`FORGELAB_AGENT_SESSION_TTL`), support explicit revocation via `POST /api/sources/agent/session/revoke`, and are automatically revoked upon source workspace deletion.

### 43. Isolated Anonymous Browser Sessions & Safe Local Fallback (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/middleware/middleware.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/middleware/middleware.go), [`backend/internal/services/user_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/user_service.go), [`backend/internal/websocket/hub.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/websocket/hub.go)
- **Resolution:** Anonymous browser users are assigned an isolated guest UUID and cookie (`forgelab_guest_session`) / header (`X-Guest-Session`) with provisioned database records (`guest-<uuid>@forgelab.local`), preventing anonymous users from accessing one another's projects, logs, or secrets. Local non-browser loopback requests (`curl`, scripts, test harnesses) resolve safely to `models.GuestUserID`, preserving registration-free local CLI workflows.

### 44. Deployment Reliability & Non-Destructive Failure Invariant (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/handlers/project_handler.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/handlers/project_handler.go), [`backend/internal/docker/engine_lifecycle.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_lifecycle.go), [`backend/internal/handlers/health_handler.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/handlers/health_handler.go)
- **Resolution:** Enqueue failures never claim successful queueing (properly failing deployment record with HTTP 500 error). If an active release is running, enqueue or deployment failures derive project status via `CalculateProjectStatus` rather than destroying the healthy release. Startup daemon container reconciliation authoritativeness is tracked and exposed via `/ready` (returning 503 if reconciliation is unavailable or failed).

### 45. Bounded Discovery & Leak-Free Build Context Tar Cleanup (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/discovery/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/discovery/engine.go), [`backend/internal/tararchive/tararchive.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/tararchive/tararchive.go), [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go)
- **Resolution:** `DiscoverWithContext` enforces context cancellation, 50,000 file and 500 MB bounds, and verifies that directory traversal and symlinks do not escape the canonical project root. Archive creation logs progress every 1,000 files, and build context `.tar` files are cleaned up reliably via `defer os.Remove` in all build code paths.

### 46. Live-Preview URL Suppression for Non-Running Containers (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/services/service_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/service_service.go), [`backend/internal/services/deployment_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/deployment_service.go)
- **Resolution:** Stopped, failed, or unreachable services strictly set `preview_url = nil` across ServiceService, DeploymentService, and ProjectService, preventing misleading preview links from displaying in the UI for non-running containers.

### 47. Authoritative Service Connection Diagnostics & DAG Topology View (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine_topology.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_topology.go), [`backend/internal/handlers/project_handler.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/handlers/project_handler.go), [`frontend/src/features/projects/topology-section.tsx`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/src/features/projects/topology-section.tsx), [`frontend/src/app/(app)/projects/[id]/page.tsx`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/src/app/(app)/projects/%5Bid%5D/page.tsx)
- **Resolution:** Implemented `GET /api/projects/{id}/topology` reusing existing DAG discovery and plan architecture. Exposes DAG execution tiers, service nodes (internal DNS, ports, host ports, networks, readiness), and actionable connection diagnostics (port collisions, missing dependencies, failed upstreams, database connection strings). Added interactive `Topology & Diagnostics` tab in the frontend project view.

### 48. Node.js/Next.js Lifecycle Build Staging & Prisma Generation (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/discovery/detector.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/discovery/detector.go), [`backend/internal/detector/detector.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/detector/detector.go), [`backend/internal/docker/preflight.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/preflight.go)
- **Resolution:** When Next.js or Node.js services declare lifecycle scripts requiring Prisma (e.g. `postinstall: prisma generate`), the automatic Dockerfile generator selectively stages `prisma/` and config files (`prisma.config.ts`, or custom paths from `package.json`) before dependency installation (`RUN npm ci`). Ephemeral placeholder `DATABASE_URL` is scoped to build commands without baking credentials into images. Unrelated projects emit zero extra COPY steps. In addition, preflight checks validate manifest integrity and schema presence, surfacing actionable diagnostics before Docker build starts. Container start commands use JSON-serialized `CMD ["sh", "-c", ...]` escaping quotes and shell metacharacters.

### 49. Safe Multi-Service Exposure Controls & Volume Sanitization (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/services/project_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/project_service.go), [`backend/internal/security/volume_security.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/security/volume_security.go), [`frontend/src/components/dashboard/create-project-modal.tsx`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/src/components/dashboard/create-project-modal.tsx)
- **Resolution:** Added explicit `public_exposed` selection per service in the project creation modal and API (`*bool`). Legacy and older API requests that omit the flag preserve backward-compatible heuristic defaults while ensuring database/infrastructure services never default to public exposure. Volume sanitization strictly validates volume targets and parent escapes (`..`), propagating errors rather than silently continuing, while safely removing forbidden Docker socket mounts.

### 50. Domainless Public Sharing via Cloudflare Quick Tunnels & Gateway Hardening (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/tunnel/`](file:///home/estif/Projects/ForgeLAB/backend/internal/tunnel/), [`backend/internal/gateway/`](file:///home/estif/Projects/ForgeLAB/backend/internal/gateway/), [`backend/internal/services/share_service.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/services/share_service.go), [`frontend/src/features/projects/share-modal.tsx`](file:///home/estif/Projects/ForgeLAB/frontend/src/features/projects/share-modal.tsx)
- **Resolution:**
  1. *Provider Abstraction:* Created `ShareProvider` interface implemented by `CloudflareProvider` (default domainless) and `GatewayProvider` (advanced self-hosted).
  2. *Cloudflare Quick Tunnel Integration:* Supervised child process management with safe argv slices (`exec.CommandContext`), zero shell interpolation, output line scanning with strict regex (`^https://[a-zA-Z0-9-]+\.trycloudflare\.com$`), pre-existing config conflict detection, bounded memory buffers, and graceful SIGTERM/SIGKILL termination.
  3. *Share-Scoped Local Router:* Ephemeral listener bound to `127.0.0.1:0`. Longest-prefix matching route dispatcher (`/api/*` prioritised over `/`), strict path traversal prevention (`..` and `%2e%2e`), management plane port shielding (:8080, :4142, :5432, :6379, Docker socket), and bidirectional WebSocket hijacking proxy.
  4. *Stale Share Reconciliation:* Server startup scans for and revokes orphaned ephemeral shares via `ReconcileStaleShares`, ensuring dead processes are never falsely displayed as online.
  5. *Gateway Security & Handshake Hardening:* Constant-time secret comparison with `crypto/subtle.ConstantTimeCompare`, startup non-empty secret enforcement, per-session route isolation preventing cross-tenant route hijacking, and bounded 5s synchronous registration acknowledgments.
  6. *Truthful Frontend Experience:* Truthful states (`not_shared`, `checking_prerequisites`, `starting_tunnel`, `connecting`, `online`, `disconnected`, `unavailable`, `expired`, `revoked`, `failed`), missing prerequisite copyable install guide, and Quick Tunnel caveats notice.

### 41. Atomic Refresh Token Rotation via PostgreSQL Transactions (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/services/user_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/user_service.go), [`backend/internal/services/token_rotation_regression_test.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/token_rotation_regression_test.go)
- **Resolution:** Refresh-token rotation executes inside a single PostgreSQL transaction (`tx.Begin(ctx)`):
  1. Atomically claims and revokes the active token via conditional update (`UPDATE refresh_tokens SET revoked = true WHERE token_hash = $1 AND revoked = false AND expires_at > $2`).
  2. Generates new access and refresh token pair.
  3. Inserts the hashed replacement refresh token into `refresh_tokens` within the same transaction.
  4. Commits both operations together (`tx.Commit`).
  If any database error occurs or if concurrent requests attempt to reuse the token, the transaction rolls back cleanly; users are never left with a revoked token and no replacement session. Replayed tokens, expired tokens, and invalid tokens fail closed with explicit errors (`ErrTokenRevoked`, `ErrTokenExpired`, `ErrTokenNotFound`).

### 42. Trusted-Proxy Client IP Extraction and Rate-Limit Spoofing Mitigation (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/middleware/middleware.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/middleware/middleware.go), [`backend/internal/ratelimit/limiter.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/ratelimit/limiter.go), [`backend/cmd/server/main.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/cmd/server/main.go), [`frontend/test/trusted-proxy.test.mjs`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/test/trusted-proxy.test.mjs)
- **Resolution:** Removed unconditional trust of `X-Forwarded-For` and `X-Real-IP` from `chi/middleware.RealIP` and rate limiting. Replaced with `middleware.TrustedProxyRealIP(cfg.RateLimit.TrustedProxies...)`.
  - Rate limiting and auditing invoke `ExtractClientIP(r, trustedProxies)`.
  - When the direct remote peer is NOT in `TRUSTED_PROXIES`, client IP extraction strictly uses the direct TCP socket address (`r.RemoteAddr`), completely ignoring attacker-supplied forwarding headers.
  - When the direct peer IS a configured trusted proxy (e.g. Next.js proxy, Cloudflare, nginx), it traverses `X-Forwarded-For` right-to-left, peeling off trusted intermediary hops until reaching the first untrusted client IP.
  - Fully supports IPv4/IPv6 addresses and CIDR subnets (e.g. `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `127.0.0.1/32`).

### 43. Security Headers Middleware & Production CSP Registration (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/cmd/server/main.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/cmd/server/main.go), [`backend/internal/middleware/middleware.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/middleware/middleware.go), [`frontend/next.config.ts`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/next.config.ts), [`frontend/test/security-headers.test.mjs`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/test/security-headers.test.mjs)
- **Resolution:**
  - Registered `SecurityHeadersWithTrustedProxies` on the backend Chi router, injecting `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `X-XSS-Protection: 1; mode=block`, `Referrer-Policy: strict-origin-when-cross-origin`, and `Permissions-Policy`.
  - Configured tightened Next.js frontend headers in `next.config.ts`: Content-Security-Policy (`'unsafe-eval'` omitted outside development builds, explicit `connect-src` allowlist covering `'self'`, `ws:`/`wss:`, Local Agent `127.0.0.1:4142`, and configured backend origins rather than wildcard `http:`/`https:`, `frame-ancestors 'none'`, `form-action 'self'`, `object-src 'none'`, `base-uri 'self'`), `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, and `Referrer-Policy: strict-origin-when-cross-origin`.
  - HSTS (`Strict-Transport-Security`) is applied on HTTPS (`COOKIE_SECURE=true` / `ENABLE_HSTS=true` on frontend; direct TLS or trusted `X-Forwarded-Proto: https` on backend), preventing breakage during local HTTP development.

### 44. Deployment Recovery Reliability & Degraded State Reporting (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go), [`backend/internal/docker/engine_recovery.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_recovery.go), [`backend/internal/docker/engine_recovery_test.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_recovery_test.go)
- **Resolution:**
  - When replacement container creation, secondary network attachment, startup, or health-check gating fails, `restorePreviousIfNeeded` invokes `RestorePreviousHealthyContainer`.
  - Starts the previous container and performs Docker daemon inspection (`ContainerInspect`) to verify `State.Running == true`.
  - Re-attaches runtime log collectors if the container is running.
  - If restarting the previous container fails or if the container is not in `running` state, `hasPreviousHealthy` flips to `false`. ForgeLAB never reports successful recovery; the coarse service status is degraded to `models.ServiceStatusFailed`, and failure diagnostics are logged and persisted.

### 45. Provider-Specific Sharing Configuration & Production Guest Mode Hardening (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/config/config.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/config/config.go), [`backend/internal/config/config_test.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/config/config_test.go), [`docker-compose.yml`](file:///c:/Users/estif/Desktop/ForgeLAB/docker-compose.yml), [`.env.example`](file:///c:/Users/estif/Desktop/ForgeLAB/.env.example)
- **Resolution:**
  - `validateSharingConfig` distinguishes providers: Cloudflare Quick Tunnels (`SHARE_PROVIDER=cloudflare`) require no gateway credentials or tunnel URL, enabling domainless sharing out of the box. Gateway provider (`SHARE_PROVIDER=gateway`) validates `GATEWAY_TUNNEL_URL` and `GATEWAY_AUTH_TOKEN`/`GATEWAY_AUTH_SECRET`. Unsupported providers are rejected at startup with clear descriptive errors.
  - Production default for Guest Mode (`FORGELAB_ALLOW_GUEST_MODE`) is disabled (`false`) across `.env.example`, Docker Compose (`${FORGELAB_ALLOW_GUEST_MODE:-false}`), and Go configuration. Guest Mode remains available and isolated for local or self-hosted deployments via explicit opt-in configuration without breaking anonymous sessions.

### 46. Docker Build Intermediate Container Cleanup & Host Trust Boundaries (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine_build.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_build.go), [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go)
- **Resolution:**
  - Configured `Remove: true` and `ForceRemove: true` on all `types.ImageBuildOptions` invocations in `buildImage`, guaranteeing that intermediate build containers and dangling build layers are automatically cleaned up on both successful and failed/cancelled builds.
  - Documented Docker socket trust boundaries: on a single-node daemon, complete host-level escape prevention cannot be achieved purely in userspace software due to Docker daemon architecture. ForgeLAB implements active defense-in-depth: Docker socket mount stripping, `CapDrop: ALL`, `no-new-privileges: true`, resource quotas (CPU/Memory/PIDs), bounded tar contexts, and path validation.

### 47. Modularized Project Creation Wizard & Decomposed Deployment Service (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`frontend/src/components/dashboard/create-project/`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/src/components/dashboard/create-project/), [`frontend/src/components/dashboard/create-project-modal.tsx`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/src/components/dashboard/create-project-modal.tsx), [`backend/internal/services/deployment_service*.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/), [`backend/internal/docker/engine_legacy.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_legacy.go)
- **Resolution:**
  - Refactored frontend `CreateProjectModal` (reduced from 3,710 to 1,952 lines) by extracting modular components: `PlanReviewStep`, `ConfigStep`, `ArchiveUploadSource`, `LocalAgentSource`, and `GitHubImportSource`, preserving all interfaces, callbacks, and validation steps.
  - Decomposed backend `deployment_service.go` (reduced from 3,321 to ~2,050 lines) by extracting log persistence and query methods (`deployment_service_logs.go`), deployment and service status transitions (`deployment_service_status.go`), and deployment queue reconciliation and cancellation (`deployment_service_reconcile.go`).
  - Extracted legacy single-container deployment execution into `backend/internal/docker/engine_legacy.go` (750 lines).

### 48. Control-Plane Isolation & Safe Host-Port Preview Routing (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/tunnel/upstream_resolver.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/tunnel/upstream_resolver.go), [`backend/internal/tunnel/local_router.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/tunnel/local_router.go), [`backend/internal/tunnel/share_router.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/tunnel/share_router.go)
- **Resolution:**
  - Completely eliminated `r.dockerCli.NetworkConnect(...)`, which previously attached the privileged backend container to tenant Docker bridge networks. The control plane remains strictly isolated on its internal network (`forgelab-net`).
  - Upstream resolution routes preview traffic through published Docker container host ports (`HostPort`), checking both loopback `127.0.0.1:<port>` and `host.docker.internal:<port>`.
  - Enhanced management plane shielding (`isForbiddenUpstream`): strictly blocks internal management ports (8080 backend, 4142 agent, 3000 frontend, 5432 postgres, 6379 redis) and internal hostnames, preventing SSRF or management service exposure.

### 49. Proxy Trust, Forwarding Header Stripping & Dynamic Preview CORS (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/middleware/middleware.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/middleware/middleware.go), [`backend/cmd/server/main.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/cmd/server/main.go), [`backend/internal/services/share_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/share_service.go)
- **Resolution:**
  - Untrusted client requests have all spoofed forwarding headers (`X-Forwarded-For`, `X-Forwarded-Host`, `X-Forwarded-Proto`, `X-Forwarded-Port`, `X-Real-IP`) stripped at the request boundary by `TrustedProxyRealIP`.
  - HSTS headers are applied strictly under verified HTTPS conditions (direct TLS connection or verified trusted proxy peer).
  - CORS origin validation no longer accepts broad wildcard `*.trycloudflare.com` origins (which could allow arbitrary Cloudflare tunnel operators to bypass CORS). Instead, `ShareService` registers active preview hosts into a thread-safe dynamic registry (`RegisterPreviewHost` / `UnregisterPreviewHost`), validating preview hosts strictly against active shares.

### 50. Gateway Verified TLS Enforcement in Production (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/config/config.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/config/config.go), [`backend/internal/tunnel/tunnel_manager.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/tunnel/tunnel_manager.go)
- **Resolution:**
  - `validateSharingConfig` in production (`!isDev`) strictly rejects `GATEWAY_TLS_SKIP_VERIFY=true`.
  - Removed automatic fallback from TLS to unencrypted plaintext TCP. In production, outbound gateway connections require verified TLS via `tls.DialWithDialer`.

### 51. JWT Token Default Alignment & Concurrency Grace Window (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/config/config.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/config/config.go), [`.env.example`](file:///c:/Users/estif/Desktop/ForgeLAB/.env.example), [`docker-compose.yml`](file:///c:/Users/estif/Desktop/ForgeLAB/docker-compose.yml), [`backend/internal/services/user_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/user_service.go), [`frontend/src/lib/auth/session-manager.ts`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/src/lib/auth/session-manager.ts)
- **Resolution:**
  - Extended and aligned JWT token expiries: Access token expiry defaults to 48 hours (> 1 day, `JWT_ACCESS_TOKEN_EXPIRY=48h`), and refresh token expiry defaults to 14 days (> 1 week, `JWT_REFRESH_TOKEN_EXPIRY=14d`) supporting both duration strings and minutes/days, with extended 24-hour frontend session inactivity limits and bidirectional WebSocket session updates.
  - Added bounded retry spin (up to 5 attempts over ~300ms) in `UserService.RefreshTokens` when an in-flight concurrent transaction is committing and caching its rotation.
  - Added short grace window check in frontend `SessionManager.performRefreshCall` on 401 before declaring `SESSION_EXPIRED`, preventing tab desynchronization and spurious logouts during simultaneous tab requests.

### 52. Cloudflare Tunnel Lifecycle, Graceful Shutdown & Stale Share Reconcile (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/tunnel/cloudflare_provider.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/tunnel/cloudflare_provider.go), [`backend/internal/services/share_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/share_service.go)
- **Resolution:**
  - Configured `cmd.Cancel` with graceful interruption signal (SIGINT fallback to Kill) and `cmd.WaitDelay = 3 * time.Second`.
  - Eliminated startup race: after receiving a trycloudflare URL from stdout, the provider checks `managed.done` and `errCh` to verify the process didn't terminate immediately before marking state as `Online`.
  - Replaced fire-and-forget goroutines in `CloudflareProvider.Close()` with a bounded `sync.WaitGroup` waiting up to 5 seconds for all child processes and listeners to exit.
  - Startup reconciliation (`ReconcileStaleShares`) cleans up all active shares across PostgreSQL and unregisters preview hosts upon server reboot.

### 53. Container Capability Minimization, Non-Root Profiles & Quotas (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/trust_boundary.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/trust_boundary.go), [`backend/internal/discovery/compose.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/discovery/compose.go), [`backend/internal/models/models.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/models/models.go), [`backend/internal/docker/engine_service_deploy.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_service_deploy.go), [`backend/internal/services/source_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/source_service.go)
- **Resolution:**
  - Reduced standard container `safeCaps` from `["CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID"]` to `["CHOWN", "SETGID", "SETUID"]`, omitting filesystem permission bypasses (`DAC_OVERRIDE`, `FOWNER`) from standard default capabilities.
  - Added `User` field across `RawComposeService`, `DiscoveredService`, `models.Service`, and `models.ServiceDeployment`, wiring `containerConfig.User = service.User` to support non-root container profiles.
  - Introduced cumulative per-owner resource quota validation (`ValidateOwnerResourceQuota`) and practical per-owner storage quota check (`MaxOwnerStorageQuotaBytes = 1GB`) on source archive ingest.

### 54. Independent CI Security Scanners & Govulncheck Audit (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`.github/workflows/ci.yml`](file:///c:/Users/estif/Desktop/ForgeLAB/.github/workflows/ci.yml), [`docs/00-current-state.md`](file:///c:/Users/estif/Desktop/ForgeLAB/docs/00-current-state.md)
- **Resolution:**
  - Split unified `security-scan` job into three independent CI jobs: `security-govulncheck`, `security-npm-audit`, and `security-trivy`. If one scanner reports findings, the others still execute to completion.
  - Documented govulncheck findings: 2 findings in `github.com/docker/docker@v28.5.2+incompatible` (`GO-2026-4887`, `GO-2026-4883`, Fixed in: N/A) reside in the Docker daemon (`daemon.Daemon.ContainerCreate`) not run by ForgeLAB; standard library findings on local dev (`go1.27.0`) are resolved in production CI via Go 1.26.10 (`golang:1.26-alpine`).
  - Frontend `npm audit --audit-level=high` reports 0 vulnerabilities.

### 55. Windows First-Launch Local Agent Cold-Start Race & Folder Picker Reliability (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/agent/server.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/agent/server.go), [`backend/internal/agent/picker.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/agent/picker.go), [`backend/internal/agent/picker_windows.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/agent/picker_windows.go), [`backend/cmd/agent/main.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/cmd/agent/main.go), [`start-agent.ps1`](file:///c:/Users/estif/Desktop/ForgeLAB/start-agent.ps1), [`frontend/src/lib/api/base.ts`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/src/lib/api/base.ts), [`frontend/src/lib/api/agent-client.ts`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/src/lib/api/agent-client.ts), [`frontend/src/components/dashboard/create-project-modal.tsx`](file:///c:/Users/estif/Desktop/ForgeLAB/frontend/src/components/dashboard/create-project-modal.tsx)
- **Resolution:**
  - Decoupled `GET /api/agent/status` from synchronous 2-second Docker pings; Docker daemon health now runs asynchronously in a background monitor exposing cached `docker_status` (`checking`, `available`, `unavailable`), `docker_error`, and `docker_checked_at`.
  - Fixed `ListenAddr()` formatting using `net.JoinHostPort` + `strconv.Itoa` (removing `rune` port corruption), guarded session/server close synchronization, and ensured `forgelab-agent` binds `net.Listen` synchronously before printing `Ready on http://...`.
  - Fixed `agentFetch` to omit `Content-Type: application/json` on bodyless GET requests (avoiding unnecessary CORS preflight), increased default status timeout to 5s, cleanly separated timeouts (HTTP 408) from caller aborts (`AbortError`) and unreachable agent errors, and guarded `CreateProjectModal` against leaving `agentStage` stuck in `selecting`.
  - Enforced interactive logged-in user session checks (`SessionId != 0` and visible window station) in both PowerShell startup scripts and Win32 preflight (`InspectInteractiveSession`), hardened PowerShell `-STA` `-EncodedCommand` `FolderBrowserDialog` invocation with a disposed invisible active-screen owner `Form`, UTF-8 Base64 path transport (`FORGELAB_PATH_B64:`), and distinct cancellation (`ErrPickerCancelled`) vs timeout (`ErrPickerTimedOut`).

### 56. Windows Path Canonicalization, NTFS Junction Resolution & TAR Context Hardening (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/security/path_validator.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/security/path_validator.go), [`backend/internal/tararchive/tararchive.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/tararchive/tararchive.go), [`backend/internal/discovery/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/discovery/engine.go), [`backend/internal/agent/session.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/agent/session.go)
- **Resolution:** Added unified `CanonicalizeExistingPath` and `CanonicalizeWithAncestor` normalizing Windows object-manager prefixes (`\??\C:\...`, `\??\UNC\...`), resolving NTFS junctions/reparse points step-by-step, canonicalizing Windows 8.3 short paths (e.g. `RUNNER~1`), and rejecting UNC/network paths (`ErrRestrictedSystemPath`). Discovery and TAR context creation reject symlinks/reparse targets escaping the canonical root boundary (`ErrSymlinkEscape`).

### 57. Custom-Domain Lifecycle Truthfulness & Standard Compose Caddy Integration (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/services/domain_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/domain_service.go), [`backend/internal/proxy/caddy_syncer.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/proxy/caddy_syncer.go), [`docker-compose.yml`](file:///c:/Users/estif/Desktop/ForgeLAB/docker-compose.yml), [`infra/caddy/Caddyfile`](file:///c:/Users/estif/Desktop/ForgeLAB/infra/caddy/Caddyfile)
- **Resolution:** Integrated `caddy:2-alpine` into `docker-compose.yml` with persistent `forgelab_caddy_data` and `forgelab_caddy_config` volumes, baseline `infra/caddy/Caddyfile`, and Admin API route synchronization (`CADDY_ADMIN_URL`). When `CADDY_ADMIN_URL` is unconfigured or Caddy sync fails, DNS-verified custom domains truthfully report `tls_status: "proxy_unconfigured"` or `"error"` with `https_active: false` instead of falsely reporting active HTTPS.

### 58. Strict Docker Capability Security Allowlist (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/trust_boundary.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/trust_boundary.go)
- **Resolution:** Replaced legacy blacklist filtering with `allowedContainerCapabilities` (`CHOWN`, `SETGID`, `SETUID` with `CAP_` prefix normalization) after `CapDrop: ["ALL"]`; any capability outside the allowlist is rejected fail-closed with `ErrCapabilityNotAllowed`.

### 59. Restart-Recoverable GitHub Imports & Push-to-Deploy Webhooks (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/services/repository_import_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/repository_import_service.go), [`backend/internal/services/webhook_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/webhook_service.go), [`backend/internal/handlers/webhook_handler.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/handlers/webhook_handler.go), [`backend/cmd/server/main.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/cmd/server/main.go)
- **Resolution:** Durable `repository_imports` recovery (`RecoverInterruptedImports`) automatically resumes interrupted imports across backend restarts alongside `GET /api/integrations/github/imports`, cancel, and retry controls. Implemented push-to-deploy webhooks (`POST /api/webhooks/github`, `GET`/`PATCH /api/projects/{id}/webhook`) with AES-256-GCM encrypted secrets, 1 MiB body caps, HMAC-SHA256 verification, and `X-GitHub-Delivery` replay deduplication.

### 60. Configurable Self-Healing, Automatic Rollback, Blue-Green Cutover & Persistent Volume Backups (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine_runtime_health.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_runtime_health.go), [`backend/internal/docker/engine_recovery.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_recovery.go), [`backend/internal/proxy/traffic_router.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/proxy/traffic_router.go), [`backend/internal/docker/engine_volumes.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_volumes.go)
- **Resolution:** Added configurable runtime self-healing restarts, crash-loop detection, automatic rollback with verified previous healthy container inspection (`VerifyPreviousHealthyContainer` / `RestorePreviousHealthyContainer`), opt-in `blue_green` traffic shifting via `TrafficRouter` (`/proxy/services/{serviceId}/*`), and project volume disk usage visibility plus gzip tar backup/restore (`POST .../backup`, `POST .../restore`).

### 61. Cached & Historical Runtime Metrics, Redacted Audit Logs & Diagnostics (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine_metrics.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_metrics.go), [`backend/internal/services/metrics_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/metrics_service.go), [`backend/internal/services/audit_service.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/services/audit_service.go), [`backend/internal/docker/engine_diagnostics.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_diagnostics.go)
- **Resolution:** Added backend-sampled/cached live metrics (`FORGELAB_METRICS_CACHE_TTL`, single-flight deduplication), configurable historical metrics persistence and retention (`FORGELAB_METRICS_SAMPLE_INTERVAL`, `FORGELAB_METRICS_RETENTION_DAYS`), secret-redacted operational audit trail (`audit_logs`), and aggregated diagnostics (`GET /api/diagnostics`, `GET /api/projects/{id}/diagnostics`).


---

## 3. Security Hardening Items (Deferred Backlog)

1. **`localStorage` Token Storage (`RESOLVED IN ARCHITECTURE`):**
   - Authentication is backend-owned using `HttpOnly`, `Secure`, `SameSite=Lax` cookies (`forgelab_access_token` and `forgelab_refresh_token`). Client-side token storage in `localStorage` has been completely eliminated.
2. **WebSocket Query String Token (`RESOLVED IN ARCHITECTURE`):**
   - Single-use ticket authentication (`POST /api/ws/ticket` -> `?ticket=<uuid>`), HttpOnly cookies, and `Authorization: Bearer` headers are enforced; `?token=...` query string authentication is strictly rejected with `401 Unauthorized`.
3. **Permissive WebSocket CORS / Origin Check (`RESOLVED IN ARCHITECTURE`):**
   - Strict origin validation is enforced across all WebSocket upgrades, matching against configured frontend URLs (`CORS_ALLOWED_ORIGINS` and `FRONTEND_URL`) or same-origin host headers.
4. **Backend Docker Socket Trust (Inherent Architectural Boundary):**
   - The Go backend mounts `/var/run/docker.sock` with root-equivalent Docker Engine control.
   - Architectural Hardening Required for Complete Elimination: Because the Docker daemon does not offer granular per-tenant Unix socket ACLs, root-equivalent control over the host cannot be eliminated purely in software on a shared single-node daemon. Complete architectural mitigation requires Docker-in-Docker (DinD), Rootless Docker, or isolated microVM hypervisors (Firecracker/gVisor). Active defense-in-depth mitigations (rejection of socket mounts into tenant containers, `CapDrop: ALL`, strict capability allowlist, `no-new-privileges`, strict path validation) are currently enforced.
5. **Container Resource Limits (`RESOLVED IN ARCHITECTURE`):**
   - `ValidateAndBuildSecureHostConfig` enforces CPU (`NanoCPUs`), Memory (`Memory`), and PIDs (`PidsLimit`) quotas per service within `container.HostConfig.Resources`. Ephemeral storage quotas are explicitly rejected.

---

## 4. Prioritized Future Platform Capabilities (Explicitly Post-MVP Roadmap)

Do not implement these capabilities during current stabilization verification; they are prioritized roadmap items for future passes:

1. **Pull-Request Preview Environments:**
   - Ephemeral preview environments automatically created and torn down on GitHub pull requests. Currently, project, branch push-to-deploy, and service deployments are managed per-project.
2. **Distributed Multi-Node Orchestration:**
   - Offloading Docker builds and container scheduling to external runner nodes or worker clusters. Currently, a single-node Docker daemon with an embedded background worker queue is active.
3. **Full External Observability Exporters (APM & OpenTelemetry):**
   - Distributed request tracing (`OTLP`), Prometheus `/metrics` scraping endpoints, and external APM integrations. Currently, backend-cached/historical runtime metrics, container log streaming, redacted audit logs, and `/health/*` + `/api/diagnostics` are active.
4. **Role-Based Access Control (RBAC):**
   - Organization-level user permissions and team workspaces (single-owner per-project model active).
5. **Packaged / Signed Local Agent Installers:**
   - Code-signed OS installers / installers via Homebrew/winget for `forgelab-agent` rather than script-based host startup (`start-agent.ps1` / `start-agent.sh`).

