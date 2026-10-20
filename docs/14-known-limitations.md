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
5. **Deployment-Time Health Gating & Runtime Monitoring (Docker Restart Policy vs ForgeLAB Self-Healing):**
   - Health checks occur during deployment promotion (`HEALTH_CHECKING`) supporting `auto`, `http`, `tcp`, and `none`.
   - **Docker runtime restart behavior (`IMPLEMENTED`):** Deployed application containers are configured with `RestartPolicy: { Name: "unless-stopped" }` in [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go). The Docker daemon automatically restarts stopped or crashed containers at the container-engine layer.
   - **ForgeLAB continuous runtime health monitoring (`IMPLEMENTED`):** The background deployment reconciler periodically inspects running containers via `CheckContinuousRuntimeHealth` in [`backend/internal/docker/engine_lifecycle.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine_lifecycle.go), detecting unexpected container exits and kernel OOM kills (`insp.State.OOMKilled`) and transitioning service records to `FAILED` with diagnostics.
   - **ForgeLAB continuous self-healing / auto-rollback (`NOT IMPLEMENTED`):** ForgeLAB does not implement automatic rollback, automated crash-loop recovery, or automated re-promotion upon runtime failure. If an application enters a broken state or fails long after reaching `RUNNING`, ForgeLAB flags the service as failed and logs diagnostics, but takes no automated remedial rollback action.
6. **No Zero-Downtime Rolling Traffic Shift:**
   - Promotion stops the old container and starts traffic on the new port. Because there is no proxy layer to gracefully drain connections or shift HTTP traffic, a brief connection gap can occur.
7. **REST Pre-Fetch & WebSocket Sequence Replay (`RESOLVED IN ARCHITECTURE`):**
   - Historical logs are fetched via REST endpoints (`GET /api/projects/:id/logs` or `GET /api/projects/:id/deployments/:did/logs`) with subquery-based recent log ordering (`ORDER BY id DESC LIMIT N` wrapped in `ORDER BY id ASC`).
   - On WebSocket reconnection, clients transmit `last_sequence` with their subscription frame, triggering the backend WebSocket Hub to stream missing historical logs from PostgreSQL before live events, preventing gaps or duplicates.

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

### 18. GitHub Repository Importing & Authorization Architecture (`RESOLVED IN ARCHITECTURE`)
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

### 19. Authoritative Dependency Graph Validation & Silent Fallback Elimination (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/discovery/graph.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/discovery/graph.go), [`backend/internal/discovery/compose.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/discovery/compose.go), [`backend/internal/discovery/engine.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/discovery/engine.go), [`backend/internal/docker/preflight.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/docker/preflight.go), [`backend/internal/docker/engine_build.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/docker/engine_build.go), [`backend/internal/docker/engine.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/docker/engine.go)
- **Resolution:**
  - Replaced disparate cycle checking and heuristic graph parsing with a single authoritative dependency-graph implementation (`ValidateAndBuildDAG`).
  - Strict validation rejects cyclic service dependencies with explicit directed cycle paths (e.g. `svc-a -> svc-b -> svc-c -> svc-a`), self-dependencies, unknown service references, and unsupported dependency conditions before building images or creating containers.
  - Plan generation (`GeneratePlan`) and release execution (`buildServiceDeploymentTiers`) fail closed immediately on graph errors without falling back to flawed tiers containing cyclic services.
  - Generates deterministic tiers using Kahn's algorithm with alphabetical sorting within each tier, and deterministically honors `service_started`, `service_healthy`, and `service_completed_successfully` dependency conditions.

### 20. Compose Network Isolation, Alias Preservation & Fatal Attachment Handling (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/docker/engine.go), [`backend/internal/docker/engine_lifecycle.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/docker/engine_lifecycle.go)
- **Resolution:**
  - Preserves declared Compose network configuration, including user-defined network membership, driver, `internal`, and supported `external` settings.
  - When services declare explicit networks, they are isolated to those declared networks and are not attached to the common project bridge network (`forgelab-net-<projectID>`).
  - Preserves default service-name DNS aliases and custom aliases across all networks to which the service belongs.
  - Treats network creation failures or secondary network attachment failures as fatal deployment failures (fails closed, cleans up the container, restores the previous running container, and records failure diagnostics), never demoting network failures to non-fatal warnings.

### 21. Target Database Credential Resolution, URL Encoding & Secret Redaction (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/docker/engine.go), [`backend/internal/logging/redactor.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/logging/redactor.go)
- **Resolution:**
  - Standardized connection string resolver derives target database credentials directly from the target database service's configuration and decrypted secrets (`secretService.GetDecryptedEnvMap`), not from the consuming service's environment.
  - Encodes connection credentials using standard library `url.UserPassword` to safely percent-encode special characters.
  - Disambiguates multiple infrastructure services: generic `DATABASE_URL` and `REDIS_URL` are only populated when exactly one corresponding infrastructure service exists. Multiple databases/caches provide explicit `<SERVICE>_URL`, `<SERVICE>_HOST`, `<SERVICE>_PORT` variables to prevent ambiguous automatic selection.
  - Dynamically registers generated connection URLs and database passwords with the log redactor, guaranteeing zero plaintext secret exposure in plans, logs, API responses, or diagnostics.
  - Resolves client-facing frontend variables (`NEXT_PUBLIC_API_URL`, `VITE_API_URL`, `API_URL`) to public URLs, custom domains, or host port bindings, never private Docker DNS names (`backend:8080`).

### 22. Strict Health Check Status Gating & Readiness Diagnostics (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine_health.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/docker/engine_health.go)
- **Resolution:**
  - Explicit HTTP health checks require HTTP 2xx or 3xx responses (status 200–399) and reject HTTP 404 and 5xx responses.
  - HTTP health checks never silently fall back to TCP socket inspection upon HTTP failure.
  - Keeps HTTP, TCP, Docker-native, Auto, and None health check strategies behaviorally distinct.
  - Deployment status and diagnostics clearly distinguish process startup, listening ports, application readiness, and dependency connectivity.

### 23. Release Promotion Container Preservation & Rollback Truthfulness (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/docker/engine.go)
- **Resolution:**
  - In release deployments, previous healthy service containers are preserved throughout execution across all tiers and are only superseded/cleaned up after the full release meets promotion criteria.
  - When partial failures occur (some services succeed while others fail), ForgeLAB accurately logs that individual services were updated and explicitly disclaims full release rollback, preventing misleading rollback claims.
### 24. Unified Volume Mount Resolution Across Remote and Local Source Types (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/volume_resolver.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/docker/volume_resolver.go), [`backend/internal/services/source_service.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/services/source_service.go), [`backend/internal/services/project_service.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/services/project_service.go)
- **Defects Fixed:**
  - Resolved preflight validation failure (`bind mount source directory '.' (resolved to '') does not exist or is not accessible from backend environment`) when deploying imported GitHub repositories and archive uploads.
  - Unified `ResolveProjectSourcePaths` and `ResolveAndValidateVolumeMount` across all source origins (`local_directory`, `local_agent`, `local_upload`, `github`, and `github_public`).
  - Remote GitHub imports and archive uploads locate their immutable snapshot directories (`/app/data/sources/imports/...` and `/app/data/sources/<id>`) via source metadata, database records, and repository paths.
  - Implemented dynamic daemon host path translation (`translateBackendPathToDaemon` and `DetectAllBackendContainerMounts`) to translate container storage volume paths to host paths visible to the Docker daemon (`/var/lib/docker/volumes/...`).
  - Permitted boundary-safe automatic directory creation for relative project subpaths (e.g. `./data`) during preflight while strictly maintaining path-escape rejections, restricted directory checks, and Docker socket mount bans.

### 25. WebSocket Query String Token Removal & Single-Use Ticket Protocol (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/websocket/hub.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/websocket/hub.go), [`backend/cmd/server/main.go`](file:///home/estif/Projects/ForgeLAB/backend/cmd/server/main.go)
- **Resolution:** Removed token authentication via URL query parameters (`?token=...`), which leaked JWT credentials into server/proxy access logs and browser histories. Connection requests containing `?token=` are strictly rejected with `401 Unauthorized`. Implemented short-lived (60s TTL), single-use tickets minted via `POST /api/ws/ticket` and consumed via `GET /api/ws?ticket=<uuid>`. Replayed or expired tickets are rejected with `401 Unauthorized`. Also supported authentication via secure HttpOnly `forgelab_access_token` cookies and `Authorization: Bearer <token>` headers.

### 26. Enforced WebSocket Origin Policies (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/websocket/hub.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/websocket/hub.go)
- **Resolution:** Replaced unconditional `CheckOrigin: func(...) { return true }` with strict origin verification. All WebSocket upgrade requests validate the incoming `Origin` header against configured frontend URLs (`CORS_ALLOWED_ORIGINS` and `FRONTEND_URL`) and same-origin host matching, eliminating cross-site WebSocket hijacking (CSWSH) vulnerabilities.

### 27. Restricted Internal Diagnostics Access (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/handlers/health_handler.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/handlers/health_handler.go), [`backend/cmd/server/main.go`](file:///home/estif/Projects/ForgeLAB/backend/cmd/server/main.go)
- **Resolution:** Public `/ready` health probe no longer leaks internal operational details (`queue`, `build_queue`, `log_collectors`) to unauthenticated external clients; external callers receive a sanitized readiness status checking core dependencies. Detailed queue metrics and container log collector diagnostics via `/ready`, `/health/queue`, and `/health/logs` are restricted strictly to trusted loopback/internal callers or authenticated administrators, returning `401 Unauthorized` for external unauthenticated requests.

### 28. Docker Host Port Conflict Retries & In-Transition Reconciliation (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/docker/engine.go), [`backend/internal/docker/engine_port.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/docker/engine_port.go), [`backend/internal/docker/engine_lifecycle.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/docker/engine_lifecycle.go)
- **Resolution:** Implemented automatic conflict detection and retry logic (up to 3 retries) during container creation and startup. When Docker Engine reports that an allocated port is already bound on the host (`isPortConflictError`), ForgeLAB registers the conflicted port as used in `PortManager`, allocates a fresh port, updates port bindings, and reattempts creation/start. In addition, `ReconcileDaemonContainers` collects host ports from containers across `running`, `restarting`, `created`, and `paused` states, preventing collisions during service transitions.

### 29. Build Reproducibility & Source Revision Fingerprint Verification (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/docker/engine.go), [`backend/internal/docker/engine_build.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/docker/engine_build.go)
- **Resolution:** Pinned immutable source revisions across both release and independent service deployments. For local directory deployments, when a source revision fingerprint is pinned (`fp_...`), `resolveSourceDirectory` computes a fresh content-based fingerprint (`ComputeDirectoryContentFingerprintWithContext`) and fails closed with an informative error (`source content changed since deployment was created`) if disk files drift from the reviewed deployment revision before build execution.

### 30. Remote Public Host URL Resolution (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/config/config.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/config/config.go), [`backend/internal/docker/engine.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/docker/engine.go), [`backend/cmd/server/main.go`](file:///home/estif/Projects/ForgeLAB/backend/cmd/server/main.go)
- **Resolution:** Configured `PublicHost` via `FORGELAB_PUBLIC_HOST` or `PUBLIC_HOST` (falling back to host from `FRONTEND_URL`), preventing remote deployments from erroneously generating `localhost` browser URLs.

### 31. Local Agent Endpoint Identity Verification & Token Revocability (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/agent/server.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/agent/server.go), [`backend/internal/agent/client.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/agent/client.go), [`backend/internal/agent/session.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/agent/session.go), [`backend/internal/handlers/source_handler.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/handlers/source_handler.go)
- **Resolution:** Added preflight unauthenticated identity verification probes (`POST /api/agent/verify-identity` and `GET /api/agent/status` with `X-Expected-Agent-ID`). The control plane verifies endpoint identity BEFORE transmitting bearer credentials, preventing session tokens from ever being sent to unverified fallback endpoints. Tokens default to a short 15-minute TTL (`FORGELAB_AGENT_SESSION_TTL`), support explicit revocation via `POST /api/sources/agent/session/revoke`, and are automatically revoked upon source workspace deletion.

### 32. Isolated Anonymous Browser Sessions & Safe Local Fallback (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/middleware/middleware.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/middleware/middleware.go), [`backend/internal/services/user_service.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/services/user_service.go), [`backend/internal/websocket/hub.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/websocket/hub.go)
- **Resolution:** Anonymous browser users are assigned an isolated guest UUID and cookie (`forgelab_guest_session`) / header (`X-Guest-Session`) with provisioned database records (`guest-<uuid>@forgelab.local`), preventing anonymous users from accessing one another's projects, logs, or secrets. Local non-browser loopback requests (`curl`, scripts, test harnesses) resolve safely to `models.GuestUserID`, preserving registration-free local CLI workflows.

### 33. Deployment Reliability & Non-Destructive Failure Invariant (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/handlers/project_handler.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/handlers/project_handler.go), [`backend/internal/docker/engine_lifecycle.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/docker/engine_lifecycle.go), [`backend/internal/handlers/health_handler.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/handlers/health_handler.go)
- **Resolution:** Enqueue failures never claim successful queueing (properly failing deployment record with HTTP 500 error). If an active release is running, enqueue or deployment failures derive project status via `CalculateProjectStatus` rather than destroying the healthy release. Startup daemon container reconciliation authoritativeness is tracked and exposed via `/ready` (returning 503 if reconciliation is unavailable or failed).

### 34. Bounded Discovery & Leak-Free Build Context Tar Cleanup (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/discovery/engine.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/discovery/engine.go), [`backend/internal/tararchive/tararchive.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/tararchive/tararchive.go), [`backend/internal/docker/engine.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/docker/engine.go)
- **Resolution:** `DiscoverWithContext` enforces context cancellation, 50,000 file and 500 MB bounds, and verifies that directory traversal and symlinks do not escape the canonical project root. Archive creation logs progress every 1,000 files, and build context `.tar` files are cleaned up reliably via `defer os.Remove` in all build code paths.

### 35. Live-Preview URL Suppression for Non-Running Containers (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/services/service_service.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/services/service_service.go), [`backend/internal/services/deployment_service.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/services/deployment_service.go)
- **Resolution:** Stopped, failed, or unreachable services strictly set `preview_url = nil` across ServiceService, DeploymentService, and ProjectService, preventing misleading preview links from displaying in the UI for non-running containers.

### 36. Authoritative Service Connection Diagnostics & DAG Topology View (`RESOLVED IN ARCHITECTURE`)
- **Location:** [`backend/internal/docker/engine_topology.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/docker/engine_topology.go), [`backend/internal/handlers/project_handler.go`](file:///home/estif/Projects/ForgeLAB/backend/internal/handlers/project_handler.go), [`frontend/src/features/projects/topology-section.tsx`](file:///home/estif/Projects/ForgeLAB/frontend/src/features/projects/topology-section.tsx), [`frontend/src/app/(app)/projects/[id]/page.tsx`](file:///home/estif/Projects/ForgeLAB/frontend/src/app/(app)/projects/[id]/page.tsx)
- **Resolution:** Implemented `GET /api/projects/{id}/topology` reusing existing DAG discovery and plan architecture. Exposes DAG execution tiers, service nodes (internal DNS, ports, host ports, networks, readiness), and actionable connection diagnostics (port collisions, missing dependencies, failed upstreams, database connection strings). Added interactive `Topology & Diagnostics` tab in the frontend project view.

---

## 3. Security Hardening Items (Deferred Backlog)

1. **`localStorage` Token Storage (`RESOLVED IN ARCHITECTURE`):**
   - Authentication is backend-owned using `HttpOnly`, `Secure`, `SameSite=Lax` cookies (`forgelab_access_token` and `forgelab_refresh_token`). Client-side token storage in `localStorage` has been completely eliminated.
2. **WebSocket Query String Token (`RESOLVED IN ARCHITECTURE`):**
   - Query string JWT authentication (`?token=...`) has been eliminated and is strictly rejected with `401 Unauthorized`. WebSocket connections authenticate via HttpOnly cookies, `Authorization` headers, or short-lived single-use tickets (`POST /api/ws/ticket`).
3. **Permissive WebSocket CORS / Origin Check (`RESOLVED IN ARCHITECTURE`):**
   - Strict origin validation is enforced across all WebSocket upgrades, matching against configured frontend URLs (`CORS_ALLOWED_ORIGINS` and `FRONTEND_URL`) or same-origin host headers.
4. **Backend Docker Socket Trust (Inherent Architectural Boundary):**
   - The Go backend mounts `/var/run/docker.sock` with root-equivalent Docker Engine control.
   - Architectural Hardening Required for Complete Elimination: Because the Docker daemon does not offer granular per-tenant Unix socket ACLs, root-equivalent control over the host cannot be eliminated purely in software on a shared single-node daemon. Complete architectural mitigation requires Docker-in-Docker (DinD), Rootless Docker, or isolated microVM hypervisors (Firecracker/gVisor). Active defense-in-depth mitigations (rejection of socket mounts into tenant containers, `CapDrop: ALL`, `no-new-privileges`, strict path validation) are currently enforced.
5. **Container Resource Limits (`RESOLVED IN ARCHITECTURE`):**
   - `ValidateAndBuildSecureHostConfig` enforces CPU (`NanoCPUs`), Memory (`Memory`), and PIDs (`PidsLimit`) quotas per service within `container.HostConfig.Resources`. Ephemeral storage quotas are explicitly rejected.

---

## 4. Prioritized Future Platform Capabilities (Explicitly Post-MVP Roadmap)

Do not implement these capabilities during current MVP verification; they are prioritized roadmap items for future passes:

1. **Caddy Reverse Proxy & Automatic HTTPS:**
   - Automatic subdomain routing (`<slug>.forgelab.local`) and Let's Encrypt / local ACME TLS certificate automation. Currently, dynamic host port allocation (`10000–60000`) and loopback bindings provide direct service access.
2. **GitHub Push-to-Deploy & Webhook Automation:**
   - Automated deployment triggers on `git push` to tracked branches with HMAC-SHA256 signature verification. Currently, manual immutable commit-pinned deployments (`source_type: "github"`) are active.
3. **Pull-Request Preview Environments:**
   - Ephemeral preview environments automatically created and torn down on GitHub pull requests. Currently, project and service deployments are managed per-project.
4. **Distributed Multi-Node Orchestration:**
   - Offloading Docker builds and container scheduling to external runner nodes or worker clusters. Currently, a single-node Docker daemon with an embedded background worker queue is active.
5. **Full Observability Infrastructure (APM & OpenTelemetry):**
   - Distributed request tracing, metrics scraping, Prometheus exporters, and external APM integrations. Currently, container log streaming, bounded log retention, and `/health/*` deep diagnostics are active.
6. **Continuous Self-Healing / Auto-Rollback:**
   - Automated crash-loop recovery and automatic rollback upon runtime failure (continuous health inspection is implemented).
7. **Role-Based Access Control (RBAC):**
   - Organization-level user permissions and team workspaces (single-owner per-project model active).

