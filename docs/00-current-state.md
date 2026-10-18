# ForgeLAB — Current Implementation State

**Implementation Baseline Commit:**  
`468940881338054d0771cac4be633da698a89d0f`  

**Commit Subject:**  
`feat: implement ForgeLAB MVP with secure deployment infrastructure`  

**Implementation Baseline Audit Note:**  
The MVP implementation described by this documentation was audited against commit `46894088`. The documentation intentionally records the implementation commit against which the implementation state was audited. It does not attempt to track repository HEAD because documentation commits themselves change HEAD.

**Documentation Overhaul Commit:**  
`fb974c216f3f3150b6e90c7da7952e4490d31f57`  

---

## Related Documents

- [INSTRUCTION.md](../INSTRUCTION.md) — Operational memory & mandatory agent workflow
- [docs/09-api-contract.md](09-api-contract.md) — Authoritative REST & WebSocket API specification
- [docs/10-frontend-architecture.md](10-frontend-architecture.md) — Next.js frontend state & UI architecture
- [docs/11-development-environment.md](11-development-environment.md) — Local runtime, ports, Docker socket, & config
- [docs/12-manual-verification.md](12-manual-verification.md) — Authoritative physical/manual test procedures
- [docs/13-decisions.md](13-decisions.md) — Architecture Decision Records (ADRs)
- [docs/14-known-limitations.md](14-known-limitations.md) — Identified bugs, missing features, & hardening backlog
- [docs/15-github-integration.md](15-github-integration.md) — Future GitHub OAuth & repository selection design

---

## 1. Current Snapshot

### Project Identity
ForgeLAB is a single control-plane, self-hosted application deployment platform. Built with a **Go control plane**, **PostgreSQL 16**, **Redis 7**, **Docker Engine SDK**, and a **Next.js 16.3.6 Active LTS (TypeScript)** modern App Router web UI, it manages the full application lifecycle: host source validation, snapshot copying, Docker image builds, container creation on dynamic host ports, HTTP health-check gating, live WebSocket log streaming, AES-256-GCM secret management, rollback safety, and unified authentication with password, Google OAuth, and GitHub OAuth sign-in.

### Current Architectural State
- **Control Plane Pattern:** Single Go control-plane process (`backend/cmd/server/main.go`). It serves HTTP REST endpoints (via `chi`), WebSocket connections (via `gorilla/websocket`), OAuth 2.0 flows (Google and GitHub), and hosts an embedded asynchronous background deployment worker consuming jobs from a Redis list (`BRPOP`).
- **Data Persistence:** PostgreSQL 16 stores durable records for `users`, `auth_identities` (OAuth provider links), `projects`, `deployments`, `environment_variables` (secrets), `deployment_logs`, and `refresh_tokens`.
- **Database Migrations:** Executed automatically via a dedicated Docker Compose `migrate` container using `forgelab-migrate up` before the backend starts.
- **Session & Identity Management:** Backend-owned authentication. Sessions are delivered via secure HttpOnly cookies (`forgelab_access_token` and `forgelab_refresh_token`). No JWT persistence in client-side `localStorage`/`sessionStorage`. Safe account-linking model associates OAuth identities with existing accounts if the email is verified, or provisions new users w- **Frontend Architecture & API Proxy:** Built on Next.js 16.3.6 App Router, React 19, and Tailwind CSS. Employs a dedicated same-origin reverse-proxy route handler (`app/api/[[...path]]/route.ts`) that transparently preserves incoming `Cookie`, `Authorization`, query parameters, request bodies, backend `Set-Cookie` (via `getSetCookie()`), `Location` redirects, and status codes. Features a controlled 401 token-refresh-and-retry mechanism in `apiClient` without infinite loops. Includes a dedicated 3-stage project creation and review flow (`Source → Configuration → Deployment Plan Review → Deploy`).
- **Unified Deployment Architecture Pipeline:**
  ForgeLAB enforces a unified 7-stage deployment lifecycle:
  `Source → Immutable Snapshot → Discovery → Deployment Plan → User Review → Release → ServiceDeployments`
- **Docker Networking & Volume Persistence:** The browser communicates with the frontend on `http://localhost:3000`. The Next.js server proxies `/api/*` requests internally to `http://backend:8080`. Docker Compose explicitly binds host ports to loopback by default (`127.0.0.1:8080` and `127.0.0.1:3000`) and passes `APP_ENV: ${APP_ENV:-development}` to `backend` and `migrate` services. Non-development environments (`staging`, `production`, etc.) enforce strict production-grade validation: rejecting missing or weak encryption keys, default database passwords, insecure cookies, short JWT secrets, debug log levels, empty trusted proxies, and unsafe source roots (`/`, drive roots, `/etc`, `/var`, system directories), while development defaults are strictly confined to local development (`isDev`). Source workspaces are persisted via the dedicated Docker Compose volume `forgelab_sources:/app/data/sources` with `FORGELAB_SOURCES_DIR=/app/data/sources`, strictly segregated from ephemeral build workspaces. For deployed services, each project receives an isolated Docker bridge network (`forgelab-net-<projectID>`) with internal DNS addressing, alongside persistent named Docker volumes (`forgelab-vol-<projectID>-<volumeName>`). Absolute host bind mounts are strictly enforced against an administrator-controlled allowlist (`FORGELAB_ALLOWED_SOURCE_ROOTS`, `FORGELAB_HOST_SOURCE_ROOT`). Host Docker socket mounts (`/var/run/docker.sock`, `\\.\pipe\docker_engine`) are strictly rejected in both preflight validation and runtime execution, ensuring project configurations cannot self-authorize Docker socket access. Network isolation strictly rejects `host` networking and `container:` network namespace sharing, and unsupported `ephemeral_storage_mb` quotas are rejected explicitly rather than silently claimed.
- **Source Ingestion & Workspace Isolation:**
  - **Direct Local-Directory Deployment (`source_type: "local_directory"` — Primary Computer Import):** The user specifies an existing local project directory accessible to ForgeLAB. The frontend validates and analyzes the directory via `POST /api/sources/local/validate` without transferring any source files over HTTP. The backend's `PathValidator` checks directory existence, directory type, canonical symlink resolution, and restricts access strictly within configured `FORGELAB_ALLOWED_SOURCE_ROOTS`. In Docker environments, host source roots are mounted via `${FORGELAB_HOST_SOURCE_ROOT}:/host-projects:ro`, and host paths are safely translated to container paths. Detection inspects configuration manifests directly on disk. When deployed, the deployment engine uses the validated directory directly as the Docker build source (direct build mode), streaming the build context via an `io.Pipe()` with `.dockerignore` early directory pruning without buffering archives in memory.
  - **Local Agent Connected Source (`source_type: "local_agent"` — Native Host Agent):** Users connect via the local ForgeLAB agent running on the host (`cmd/agent`). The agent defaults strictly to loopback `127.0.0.1` binding. Unauthenticated status checks (`/api/agent/status`) scrub internal `agent_id` and active session counts, avoiding credential leakage and enumeration attacks. Session serialization also strictly scrubs host filesystem paths (`source_path`), eliminating local path disclosure. The agent generates a secure ephemeral session token, binds the folder, and registers via `POST /api/sources/agent/register`. The backend verifies the session, computes a deterministic content-based SHA256 source fingerprint (propagating all filesystem traversal errors), encrypts the session token using AES-256-GCM, and stores it in `sources.encrypted_session_token`. Raw tokens are scrubbed from metadata, logs, WebSocket events, and REST responses. During asynchronous deployment, the engine securely retrieves and decrypts the token via `SourceService.GetDecryptedAgentToken` with strict ownership checks, transmitting it via `Authorization: Bearer <token>` and `X-Agent-Session-Token` headers. If credentials or encryption are unavailable, the deployment fails closed before attempting build.
  - **Local Archive Upload (`source_type: "local_upload"` — Fallback Computer Import):** Available when direct filesystem access is unavailable. Users upload a `.zip`, `.tar.gz`, or `.tgz` archive via `POST /api/sources/upload` using Go's sequential `r.MultipartReader()`. The server enforces a 100MB limit, extracts files into a dedicated isolated source workspace (`data/sources/<source-id>`) with restricted `0700` directory and `0600` file permissions, and performs asynchronous runtime detection while the frontend polls `GET /api/sources/{id}`.
  - **Source Ownership & Deletion:** Persistent ownership is recorded in the `source_workspaces` table for uploaded sources. Source lookups, deletions (`DELETE /api/sources/{id}`), and project creations strictly verify that the authenticated user owns the source workspace.
  - **GitHub Repository Import (`source_type: "github"`):** Users authorize GitHub repository access via dedicated OAuth code exchange (`repo` scope) or fine-grained PAT. The backend actively verifies repository access (`GET /user/repos?per_page=1`) and distinguishes OAuth token scopes from fine-grained PATs. When analyzed or deployed, ForgeLAB resolves the exact 40-character commit SHA via `ResolveCommitSHA` and strictly pins deployments to that immutable SHA; deployment fails closed if the reviewed commit SHA cannot be acquired. ForgeLAB downloads an isolated source tarball pinned to that exact revision into a temporary analysis workspace, and ensures temporary analysis workspaces are reliably cleaned up via `defer`. Hardened extraction limits are enforced: 100MB compressed, 100MB uncompressed, 10,000 files, 50MB single file, and a 3-minute extraction timeout. Caching uses atomic directory staging (`.tmp.<uuid>` to final destination verified by `.complete` marker) with `0700` directory and `0600` file permissions. Partial or failed extractions are cleaned up reliably.
  - **Host Filesystem Path (Legacy `source_type: "local"`):** Handled dynamically by `ProjectService`, resolving to `local_directory` if `repository_path` is present, or `local_upload` if `source_reference` is present.
- **Authoritative Discovery Engine (`internal/discovery`):**
  - Consolidates repository topology, service detection, Compose file parsing, environment provenance tracking, and dependency DAG computation into a single authoritative engine.
  - Supports `compose.yaml`, `compose.yml`, `docker-compose.yaml`, and `docker-compose.yml`.
  - Evidence-based detection for static HTML/CSS/JS (nginx runtime), PHP/Laravel, Python/Django/Flask/FastAPI, Node.js/Next.js/Vite, Go, Java (Maven/Gradle), and Rust.
  - Computes execution DAG tiers (Tier 1: databases/queues → Tier 2: backends/workers → Tier 3: frontends) and rejects cyclic dependencies.
  - Computes deterministic source fingerprints based on file content rather than timestamps/sizes alone, caching hashes while walking and strictly propagating filesystem errors instead of silently skipping files.
  - Detects `.env` files and environment declarations, tracking provenance and resolving conflicts by prioritizing existing ForgeLAB secrets over imported file values (`overridden_by_forgelab`).
- **Deployment Plan & User Review:**
  - `POST /api/discovery/plan` generates an immutable `DeploymentPlan` blueprint capturing source revision, deployment strategy (`compose`, `dockerfile`, `auto`, `custom`), execution tiers, planned services, volume mounts, public/internal endpoints, and environment variables.
  - The project creation modal allows users to review the complete plan (DAG tiers, service roles, endpoints, and environment provenance) before finalizing deployment.
- **Dynamic Port Mapping & Health Strategies:**
  - Publicly exposed application containers map internal ports to dynamic host ports (`10000–60000`).
  - Internal infrastructure and worker services (e.g. databases, Redis) operate inside the isolated project network without binding host ports.
  - Readiness checks support `auto`, `http`, `tcp`, and `none` alongside Compose healthcheck specifications.
- **Service-First Deployment Architecture & Compose Execution Foundation:**
  - `ServiceDeployment` is the primary unit of deployment execution (`backend/internal/docker/engine.go -> ExecuteServiceDeployment`).
  - `Deployment` represents the release orchestration layer, executing services in sequential DAG tiers while preserving semaphore-controlled concurrency within each tier. Dependent services fail gracefully if their prerequisites fail to start or pass health checks.
  - Pre-built infrastructure images (`classification == "infrastructure"`) are pulled directly via `dockerClient.ImagePull` without source builds.
  - Every service retains independent logs, status, resources, container, deployment history, and rollback capabilities.
  - The deployment engine is modularized across focused components (`engine_health.go`, `engine_lifecycle.go`, `engine_build.go`, `shared_build_coordinator.go`, `engine.go`) maintaining strict rollback safety and deployment state guarantees. Build context tarballs are validated and streamed directly from disk to Docker Engine without buffering whole archives into memory (eliminating up to 512MB RAM buffering per build), and build log lines are bounded to 64KB. Context preparation concurrency is completely separated from Docker build slots so network transfers and tar creation do not block builder execution.
  - Build requests stream with explicit `Content-Length` and disabled chunked transfer encoding, and if BuildKit sessions fail (`no active sessions`), builds automatically reopen archives and fall back to the standard builder.
  - Build concurrency is conservatively throttled dynamically by CPU cores (1 slot on <=2 CPUs, 2 slots on <=4 CPUs, up to 4 on multi-core hosts) with full build queue diagnostics exposed via `/health/queue`. Shared builds are coordinated via a thread-safe `SharedBuildCoordinator` with fail-safe promise resolution, automatic eviction of failed build promises on retry, and comprehensive configuration cache hashing.
  - The background queue reconciler recovers orphaned queued release deployments, and workers perform cancellation checks on every heartbeat tick to promptly abort cancelled deployments.
- **Port Allocation:** Dynamic host port allocation in the range **`10000–60000`** managed by `internal/network/PortManager`.
- **Realtime Pipeline & Resilient Container Logging:**
  - Deployment events and container logs are pushed to Redis Pub/Sub channels (`forgelab:pubsub:deployment:<uuid>`), bridged to an in-memory WebSocket Hub, and streamed to authenticated client subscriptions.
  - **Runtime Collector Lifecycle:** The runtime log collector (`StartRuntimeLogCollector`) runs in an independent goroutine with a root context that continues after deployment execution returns (`ExecuteServiceDeployment` / `executeLegacySingleContainerDeployment`). Collectors stop explicitly when their container is stopped, replaced, removed, or ForgeLAB shuts down.
  - **Bounded Retries & Stream Continuity:** If a container's log stream drops while the container remains running, the collector performs up to 5 bounded retries with backoff before terminating.
  - **Daemon Container Collector Recovery:** On backend restart, `ReconcileDaemonContainers` inspects active containers across all running multi-service and legacy deployments and restores log collectors for running containers lacking an active collector.
  - **Log Querying & Historical Replay:** Historical logs are fetched with bounded cursor pagination (`service_id`, `phase`, `cursor`, `after_id`, `before_id`, `limit`). Chronological ordering is preserved via subqueries (`ORDER BY id DESC LIMIT N` wrapped in `ORDER BY id ASC`), ensuring recent runtime logs are never truncated by initial build lines.
  - **WebSocket Historical Replay:** On client subscription (`last_sequence > 0`), the WebSocket Hub queries and streams missing historical logs without gaps or duplicate delivery.
  - **UI Filter States & Memory Bounds:** The log UI provides **All Logs**, **Deployment** (`source`, `build`, `startup`, `health`), and **Production** (`runtime`) filter tabs with real-time log count badges, alongside "All Services" aggregation (with distinct service badges) and per-service scope selection. Frontend memory growth is bounded to 2,000 log entries (`MAX_STORED_LOGS`).
- **Rollback & Safety Invariant:** When a deployment fails at any stage (build, container startup, or readiness check), the previous running deployment container remains untouched, and `projects.current_deployment_id` remains unchanged. Rollback creates a new deployment referencing the prior known-good Docker image.
- **Container Restart & Runtime Health Monitoring:**
  - **Docker Runtime Restart Policy:** Containers are created with `RestartPolicy: "unless-stopped"`. The Docker daemon itself automatically restarts crashed containers.
  - **ForgeLAB Continuous Runtime Health Monitoring:** The background deployment reconciler continuously monitors running containers (`CheckContinuousRuntimeHealth`), detecting stopped/exited containers or kernel OOM kills (`insp.State.OOMKilled`), marking service records as `FAILED` with actionable failure diagnostics. Automated rollback and multi-node orchestration remain deferred.

### Current Implementation vs. Verification Stage
- **Implementation Stage:** The full-stack platform (Go backend, Next.js 16 frontend, PostgreSQL, Redis, Migrations, Worker, Google/GitHub OAuth, Unified Source Ingestion, Authoritative Discovery Engine, Deployment Plan Review, and DAG-ordered ServiceDeployments) is **IMPLEMENTED** in the codebase.
- **Verification Stage:** **NOT RECORDED** (or verified in live Docker stack). Per project policy, automated unit tests and static builds are non-authoritative artifacts. No capability may be classified as `PHYSICALLY VERIFIED` until the project owner executes the manual procedures defined in [docs/12-manual-verification.md](12-manual-verification.md) in the live development environment.

---

## 2. Current Implementation Matrix

The status classifications strictly follow these definitions:
- `DESIGNED`: Architecture exists, but no implementation code exists.
- `IMPLEMENTED`: Repository contains code intended to provide the feature.
- `PHYSICALLY VERIFIED`: Project owner manually tested the feature in the live environment and recorded the result.
- `FAILED`: Physical verification was performed and exposed a failure.
- `BLOCKED`: Verification cannot proceed due to external prerequisites.
- `DEFERRED`: Intentionally postponed.
- `REJECTED`: Explicitly ruled out.

| Capability | Status | Physical Verification | Notes |
| :--- | :--- | :--- | :--- |
| **Foundation & Scaffolding** | IMPLEMENTED | NOT RECORDED | Go module, chi router, Docker Compose, pgx driver |
| **User Registration & Login** | IMPLEMENTED | NOT RECORDED | bcrypt password hashing, JWT access + refresh tokens in HttpOnly cookies |
| **Google OAuth Authentication** | IMPLEMENTED | NOT RECORDED | Backend-owned OAuth2 authorization code flow, CSRF state in Redis |
| **GitHub OAuth Authentication** | IMPLEMENTED | NOT RECORDED | Backend-owned web OAuth flow, `read:user user:email` scopes, CSRF state in Redis |
| **Multi-Provider Identity Model** | IMPLEMENTED | NOT RECORDED | `auth_identities` table, nullable password, deterministic account linking |
| **Atomic Refresh Token Rotation** | IMPLEMENTED | NOT RECORDED | Single-use rotation via PostgreSQL atomic updates |
| **Project CRUD & Ownership** | IMPLEMENTED | NOT RECORDED | Server-side user ownership checks on all endpoints |
| **Direct Local-Directory Deployment** | IMPLEMENTED | NOT RECORDED | `POST /api/sources/local/validate`, zero browser upload, direct filesystem build source |
| **Streaming Docker Build Context** | IMPLEMENTED | NOT RECORDED | `io.Pipe()` + `tar.Writer`, zero in-memory buffer materialization, virtual Dockerfile injection |
| **.dockerignore Early Directory Pruning** | IMPLEMENTED | NOT RECORDED | Prunes `node_modules`, `.git`, `.next`, cache dirs before filesystem descent |
| **Local Source Archive Upload** | IMPLEMENTED | NOT RECORDED | Optional fallback for .zip/.tar.gz archives via `POST /api/sources/upload`, 100MB limit |
| **Source Workspace Ownership & Deletion**| IMPLEMENTED | NOT RECORDED | Persistent ownership in `source_workspaces` table, `DELETE /api/sources/{id}`, multi-tenant user isolation |
| **GitHub Repository Authorization** | IMPLEMENTED | NOT RECORDED | Dedicated OAuth `repo` scope flow or fine-grained PAT; active API access verification (`/user/repos`); encrypted at rest via AES-256-GCM |
| **GitHub Repository & Branch Picker**| IMPLEMENTED | NOT RECORDED | Scoped listing of accessible repos and branches via `/api/integrations/github` |
| **Heuristic Runtime Detection** | IMPLEMENTED | NOT RECORDED | Fast inspection of package.json, go.mod, python, etc. without copying |
| **Automatic Build Strategy** | IMPLEMENTED | NOT RECORDED | Multi-stage Dockerfile generation without requiring user Dockerfile |
| **Dynamic Internal Port Mapping** | IMPLEMENTED | NOT RECORDED | Container internal port (3000, 8000, etc.) mapped to dynamic host port |
| **Readiness Strategy (HTTP/TCP/None)**| IMPLEMENTED | NOT RECORDED | Flexible health check gating supporting HTTP, TCP, and None |
| **Host Path Security & Container Translation** | IMPLEMENTED | NOT RECORDED | Rejects system dirs, enforces administrator-controlled allowlist for absolute host bind mounts, strictly rejects Docker socket mounts in preflight & runtime |
| **Redis Deployment Queue** | IMPLEMENTED | NOT RECORDED | `LPUSH` / `BRPOP` queue with atomic Lua-scripted lease acquisition error reconciliation and duplicate prevention |
| **Docker Build Pipeline** | IMPLEMENTED | NOT RECORDED | Docker SDK ImageBuild via streaming tar, log parsing, conservative default concurrency (1-2 on low-resource machines, max 4) with queue semaphore diagnostics |
| **Dynamic Host Port Allocation** | IMPLEMENTED | NOT RECORDED | Scans available ports in range `10000–60000` |
| **Deployment State Machine** | IMPLEMENTED | NOT RECORDED | Enforced: `QUEUED`→`CLONING`→`BUILDING`→`STARTING`→`HEALTH_CHECKING`→`RUNNING`/`FAILED` |
| **Health-Check Deployment Gate** | IMPLEMENTED | NOT RECORDED | Polling gate before promotion supporting HTTP, TCP, and None |
| **Deployment Safety Invariant** | IMPLEMENTED | NOT RECORDED | Failed health checks keep previous running deployment alive |
| **Lifecycle Controls (Stop/Start/Restart)** | IMPLEMENTED | NOT RECORDED | Container Stop/Start/Restart via Docker SDK |
| **Rollback Execution** | IMPLEMENTED | NOT RECORDED | Creates new deployment using prior known-good image tag or digest; strictly fails closed if image is missing from Docker daemon (never silently rebuilds from current source) |
| **Docker Container Restart Policy** | IMPLEMENTED | NOT RECORDED | Docker daemon `unless-stopped` restarts crashed containers |
| **Continuous Runtime Health Monitoring** | IMPLEMENTED | NOT RECORDED | Periodic background inspection (`CheckContinuousRuntimeHealth`) detects stopped/crashed containers and kernel OOM kills, updating service records to FAILED with diagnostics |
| **ForgeLAB Continuous Self-Healing** | NOT IMPLEMENTED | — | Background auto-rollback and automated self-healing remediation deferred |
| **Secret Encryption at Rest** | IMPLEMENTED | NOT RECORDED | AES-256-GCM with unique 12-byte nonces; strict 32-byte key validation rejecting weak/default/low-entropy keys in production; zero hard-coded fallback keys (fails closed when unavailable) |
| **Encrypted Local Agent Credentials** | IMPLEMENTED | NOT RECORDED | Verified agent session token encrypted via AES-256-GCM in `sources.encrypted_session_token`, decrypted during deployment; zero exposure over REST, WS, or logs |
| **Secret Log Redaction** | IMPLEMENTED | NOT RECORDED | In-memory pattern replacement (`[REDACTED]`) before write |
| **WebSocket Subscription Auth** | IMPLEMENTED | NOT RECORDED | Verifies user ownership of deployment project before subscribe |
| **Deployment & Service Log Streaming** | IMPLEMENTED | NOT RECORDED | Streams live build & runtime logs to `service-deployment:<uuid>`, `deployment:<uuid>`, and `project:<uuid>`; persistent collectors continue post-deployment |
| **Project-Level WebSocket Channels** | IMPLEMENTED | NOT RECORDED | Actively produces `service_deployment_log`, `service_status_change`, and `project_status` on `project:<uuid>` |
| **Frontend UI (Next.js 16 App Router)** | IMPLEMENTED | NOT RECORDED | Next.js 16.3.6 LTS, React 19, Tailwind CSS, landing page, dashboard, proxy.ts |
| **Authoritative Discovery Engine** | IMPLEMENTED | NOT RECORDED | Consolidated topology, multi-service, Compose, static, Laravel, Django/Flask detection |
| **Docker Compose Manifest Ingestion** | IMPLEMENTED | NOT RECORDED | Supports `compose.yaml`, `compose.yml`, `docker-compose.yaml`, `docker-compose.yml` |
| **Deployment Plan & User Review** | IMPLEMENTED | NOT RECORDED | Immutable blueprint with revision, DAG tiers, services, endpoints, volumes, and env provenance |
| **Per-Project Docker Network** | IMPLEMENTED | NOT RECORDED | Isolated bridge network (`forgelab-net-<id>`) with internal service DNS |
| **Persistent Named Volumes** | IMPLEMENTED | NOT RECORDED | Per-project persistent volumes (`forgelab-vol-<projID>-<vol>`) |
| **DAG Dependency Execution** | IMPLEMENTED | NOT RECORDED | Strict multi-tier ordering, cycle detection, cascading failure gating |
| **Pre-Built Infrastructure Images** | IMPLEMENTED | NOT RECORDED | Pre-built database/redis images pulled and run without source builds |
| **Kubernetes / Multi-Node Orchestration** | DEFERRED | — | Explicitly excluded; single-node Docker architecture |
| **GitHub Webhook Auto-Deploy** | DEFERRED | — | Planned future feature |
| **Zero-Downtime Rolling Updates** | NOT IMPLEMENTED | — | Blue/green or proxy traffic shifting deferred |
| **Caddy Reverse Proxy & TLS** | NOT IMPLEMENTED | — | Reverse proxy routing not in MVP |
| **OpenTelemetry Observability** | NOT IMPLEMENTED | — | Metrics and distributed tracing deferred |
| **Container Resource Quotas** | IMPLEMENTED | NOT RECORDED | CPU (`NanoCPUs`), Memory (`Memory`), and PIDs (`PidsLimit`) enforced in Docker HostConfig via `ValidateAndBuildSecureHostConfig` |
| **Docker Trust Boundary Enforcement** | IMPLEMENTED | NOT RECORDED | Reject `/var/run/docker.sock` and sensitive mounts in preflight & runtime, `CapDrop: [ALL]`, `no-new-privileges: true`, reject host network |
| **Redis Job Lease Ownership & Recovery** | IMPLEMENTED | NOT RECORDED | Unique lease tokens (`workerID:uuid`), Lua atomic renewal/recovery and lease error reconciliation, stranded lease rescue on failure, background abandoned-job recovery, and `/health/queue` diagnostics |
| **Deep Dependency Readiness Check** | IMPLEMENTED | NOT RECORDED | `/ready` and `/health/ready` probe PostgreSQL, Redis, and Docker Engine concurrently with 0 secret exposure; `/health` remains lightweight liveness |
| **API & WebSocket Rate Limiting** | IMPLEMENTED | NOT RECORDED | Redis/in-memory fixed-window counter throttling with trusted reverse proxy validation on `/auth/*`, `/sources/*`, deploy/rollback, and WebSocket connect/subscribe returning HTTP 429 |
| **Deployment Cancellation & Superseding** | IMPLEMENTED | NOT RECORDED | User cancellation of queued/in-progress deployments; newer service deployments atomically supersede obsolete queued deployments across queue and execution entry |
| **Automated Multi-Stage CI Pipeline** | IMPLEMENTED | NOT RECORDED | GitHub Actions (`.github/workflows/ci.yml`) covering backend vet/fmt/test, frontend lint/test/build, compose smoke, and pre-built migration execution |
| **Container Reconciliation & Resource Isolation** | IMPLEMENTED | NOT RECORDED | Reconciler excludes terminal deployments from container protection, preserves intentionally stopped services and persistent volumes |
| **Source Scanning Concurrency & Validation** | IMPLEMENTED | NOT RECORDED | Bounded filesystem scanning concurrency semaphore, timeout and context cancellation propagation, and session deduplication |
| **Role-Based Access Control (RBAC)**| NOT IMPLEMENTED | — | Single owner model active; teams/roles deferred |
| **WebSocket Event Replay Service** | IMPLEMENTED | NOT RECORDED | Replay from PostgreSQL on WebSocket subscription when `last_sequence > 0` across project and deployment channels |
| **Log Filtering & Bounded Cursor Pagination** | IMPLEMENTED | NOT RECORDED | All Logs / Deployment / Production filters, All Services aggregation, subquery-based recent log retrieval, bounded 2000-entry frontend memory |
| **Multi-Node Workers** | NOT IMPLEMENTED | — | In-process embedded worker active; multi-node deferred |

---

## 3. Current Development Phase

```text
Documentation alignment and physical verification preparation for
the implemented local-deployment MVP.
```

### Critical Rule for Future Agents
> **Do not begin major new platform features merely because the architecture documents mention them. Existing implemented functionality must be physically verified before expanding the platform.**

All code contributions must respect the established implementation reality:
1. The code is authoritative for current implementation state.
2. Automated tests are historical repository artifacts, not the authoritative validation method.
3. Every feature must progress through the mandatory validation sequence:
   $$\text{IMPLEMENT} \longrightarrow \text{PHYSICAL MANUAL VERIFICATION} \longrightarrow \text{RECORD RESULT} \longrightarrow \text{PROCEED}$$
