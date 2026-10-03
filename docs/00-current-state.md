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
- **Session & Identity Management:** Backend-owned authentication. Sessions are delivered via secure HttpOnly cookies (`forgelab_access_token` and `forgelab_refresh_token`). No JWT persistence in client-side `localStorage`/`sessionStorage`. Safe account-linking model associates OAuth identities with existing accounts if the email is verified, or provisions new users without passwords.
- **Frontend Architecture & API Proxy:** Built on Next.js 16.3.6 App Router, React 19, and Tailwind CSS. Employs a dedicated same-origin reverse-proxy route handler (`app/api/[[...path]]/route.ts`) that transparently preserves incoming `Cookie`, `Authorization`, query parameters, request bodies, backend `Set-Cookie` (via `getSetCookie()`), `Location` redirects, and status codes. Features a controlled 401 token-refresh-and-retry mechanism in `apiClient` without infinite loops.
- **Docker Networking & Volume Persistence:** The browser communicates with the frontend on `http://localhost:3000`. The Next.js server proxies `/api/*` requests internally to `http://backend:8080`. Source workspaces are persisted via the dedicated Docker Compose volume `forgelab_sources:/app/data/sources` with `FORGELAB_SOURCES_DIR=/app/data/sources`, strictly segregated from ephemeral build workspaces.
- **Source Ingestion & Workspace Isolation:**
  - **Direct Local-Directory Deployment (`source_type: "local_directory"` — Primary Computer Import):** The user specifies an existing local project directory accessible to ForgeLAB. The frontend validates and analyzes the directory via `POST /api/sources/local/validate` without transferring any source files over HTTP. The backend's `PathValidator` checks directory existence, directory type, canonical symlink resolution, and restricts access strictly within configured `FORGELAB_ALLOWED_SOURCE_ROOTS`. In Docker environments, host source roots are mounted via `${FORGELAB_HOST_SOURCE_ROOT}:/host-projects:ro`, and host paths are safely translated to container paths. Detection inspects configuration manifests directly on disk. When deployed, the deployment engine uses the validated directory directly as the Docker build source (direct build mode), streaming the build context via an `io.Pipe()` with `.dockerignore` early directory pruning. Tens of thousands of files are never uploaded through the browser or copied into `data/builds/<deployment-id>`.
  - **Local Agent Connected Source (`source_type: "local_agent"` — Native Host Agent):** Users connect via the local ForgeLAB agent running on the host (`cmd/agent`). The agent generates a secure ephemeral session token, binds the folder, and registers via `POST /api/sources/agent/register`. The backend verifies the session, computes a deterministic SHA256 source fingerprint, encrypts the session token using AES-256-GCM, and stores it in `sources.encrypted_session_token`. Raw tokens are scrubbed from metadata, logs, WebSocket events, and REST responses. During asynchronous deployment, the engine securely retrieves and decrypts the token via `SourceService.GetDecryptedAgentToken` with strict ownership checks, transmitting it via `Authorization: Bearer <token>` and `X-Agent-Session-Token` headers. If credentials are missing or expired, the deployment fails closed before attempting build.
  - **Local Archive Upload (`source_type: "local_upload"` — Fallback Computer Import):** Available when direct filesystem access is unavailable. Users upload a `.zip`, `.tar.gz`, or `.tgz` archive via `POST /api/sources/upload` using Go's sequential `r.MultipartReader()`. The server enforces a 100MB limit, extracts files into a dedicated isolated source workspace (`data/sources/<source-id>`), and performs asynchronous runtime detection while the frontend polls `GET /api/sources/{id}`.
  - **Source Ownership & Deletion:** Persistent ownership is recorded in the `source_workspaces` database table for uploaded sources. Source lookups, deletions (`DELETE /api/sources/{id}`), and project creations strictly verify that the authenticated user owns the source workspace.
  - **GitHub Repository Import (`source_type: "github"`):** Users authorize GitHub repository access via dedicated OAuth code exchange (`repo` scope). The backend queries GitHub's API for repositories and branches, analyzes project metadata remotely via `GET /api/integrations/github/repositories/{owner}/{repo}/detect`, and retrieves repository tarballs into isolated source workspaces. Tokens are encrypted at rest with AES-256-GCM in `github_integrations`.
  - **Host Filesystem Path (Legacy `source_type: "local"`):** Handled dynamically by `ProjectService`, resolving to `local_directory` if `repository_path` is present, or `local_upload` if `source_reference` is present.
- **Project Detection & Build Strategy:**
  - Automated heuristic detection (`internal/detector`) analyzes metadata (`package.json`, `go.mod`, `requirements.txt`, `Cargo.toml`, `pom.xml`, `build.gradle`, `Dockerfile`) to identify language, framework, build command, start command, suggested port, and health check endpoint.
  - Generates deterministic, executable multi-stage Dockerfiles across Node.js, Next.js, Vite/static, FastAPI, Flask, Django, Go, Maven/Spring Boot, Gradle/Spring Boot, and Rust—avoiding shell glob patterns in exec-form `CMD`. Virtual Dockerfiles for automatic builds are streamed directly in memory into the TAR stream without writing to read-only source directories.
- **Dynamic Port Mapping & Health Strategies:**
  - Container internal ports (3000, 8000, 8080) are mapped to dynamic host ports (`10000–60000`).
  - Readiness checks support `auto`, `http`, `tcp`, and `none`. `auto` requires HTTP 2xx/3xx (explicitly rejecting HTTP 5xx server errors as healthy) or verified TCP socket connectivity before promotion.
- **Service-First Deployment Architecture:**
  - `ServiceDeployment` is the primary unit of deployment execution (`backend/internal/docker/engine.go -> ExecuteServiceDeployment`).
  - `Deployment` represents the release orchestration layer (e.g. "Deploy All"), holding references to child `service_deployments`.
  - The project-wide active deployment lock (`uq_active_deployment_per_project`) is removed. Instead, an active deployment lock is enforced per `service_id` via PostgreSQL partial unique index `uq_active_service_deployment`.
  - Frontend and backend services deploy completely independently and concurrently without mutual blocking.
  - Active jobs in Redis identify whether they represent an individual `service_deployment` (`JobTypeServiceDeployment`) or a full release `deployment` (`JobTypeDeployment`), processed across a concurrent 4-worker pool.
  - Each service maintains independent sequential versioning (`deploy_number` on `service_deployments`) and independent rollback capability (`POST /api/projects/{id}/services/{serviceId}/rollback`).
  - Service-only deployments update `services.current_service_deployment_id`, service status, container ID, and port without overwriting `projects.current_deployment_id`.
  - Project status is dynamically derived from individual service states (`running`, `partially_running`, `stopped`, `failed`, `inactive`) and never violates valid state transitions.
  - Health check safety invariant: failed new service deployments never destroy or replace existing healthy running service containers.
- **Port Allocation:** Dynamic host port allocation in the range **`10000–60000`** managed by `internal/network/PortManager`.
- **Realtime Pipeline:** Deployment events and container logs are pushed to Redis Pub/Sub channels (`forgelab:pubsub:deployment:<uuid>`), bridged to an in-memory WebSocket Hub, and streamed to authenticated client subscriptions.
- **Rollback & Safety Invariant:** When a deployment fails at any stage (build, container startup, or readiness check), the previous running deployment container remains untouched, and `projects.current_deployment_id` remains unchanged. Rollback creates a new deployment referencing the prior known-good Docker image.
- **Container Restart vs. Platform Self-Healing:**
  - **Docker Runtime Restart Policy:** Containers are created with `RestartPolicy: "unless-stopped"`. The Docker daemon itself automatically restarts crashed containers.
  - **ForgeLAB-Level Self-Healing:** ForgeLAB does **not** implement continuous background runtime health monitoring, crash-loop detection, or automated application rollback after promotion.

### Current Implementation vs. Verification Stage
- **Implementation Stage:** The full-stack platform (Go backend, Next.js 16 frontend, PostgreSQL, Redis, Migrations, Worker, Google/GitHub OAuth, Direct Local-Directory Deployment, Streaming Tar Context, Archive Ingestion, and Independent Service Deployments) is **IMPLEMENTED** in the codebase.
- **Verification Stage:** **NOT RECORDED** (or verified in live Docker stack). Per project policy, automated unit tests and mocks are non-authoritative artifacts. No capability may be classified as `PHYSICALLY VERIFIED` until the project owner executes the manual procedures defined in [docs/12-manual-verification.md](12-manual-verification.md) in the live development environment.

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
| **GitHub Repository Authorization** | IMPLEMENTED | NOT RECORDED | Dedicated OAuth `repo` scope flow; encrypted at rest via AES-256-GCM |
| **GitHub Repository & Branch Picker**| IMPLEMENTED | NOT RECORDED | Scoped listing of accessible repos and branches via `/api/integrations/github` |
| **Heuristic Runtime Detection** | IMPLEMENTED | NOT RECORDED | Fast inspection of package.json, go.mod, python, etc. without copying |
| **Automatic Build Strategy** | IMPLEMENTED | NOT RECORDED | Multi-stage Dockerfile generation without requiring user Dockerfile |
| **Dynamic Internal Port Mapping** | IMPLEMENTED | NOT RECORDED | Container internal port (3000, 8000, etc.) mapped to dynamic host port |
| **Readiness Strategy (HTTP/TCP/None)**| IMPLEMENTED | NOT RECORDED | Flexible health check gating supporting HTTP, TCP, and None |
| **Host Path Security & Container Translation** | IMPLEMENTED | NOT RECORDED | Rejects system dirs & paths outside allowed roots, translates host to container mounts |
| **Redis Deployment Queue** | IMPLEMENTED | NOT RECORDED | `LPUSH` / `BRPOP` queue with `SETNX` worker concurrency lock |
| **Docker Build Pipeline** | IMPLEMENTED | NOT RECORDED | Docker SDK ImageBuild via streaming tar, log parsing |
| **Dynamic Host Port Allocation** | IMPLEMENTED | NOT RECORDED | Scans available ports in range `10000–60000` |
| **Deployment State Machine** | IMPLEMENTED | NOT RECORDED | Enforced: `QUEUED`→`CLONING`→`BUILDING`→`STARTING`→`HEALTH_CHECKING`→`RUNNING`/`FAILED` |
| **Health-Check Deployment Gate** | IMPLEMENTED | NOT RECORDED | Polling gate before promotion supporting HTTP, TCP, and None |
| **Deployment Safety Invariant** | IMPLEMENTED | NOT RECORDED | Failed health checks keep previous running deployment alive |
| **Lifecycle Controls (Stop/Start/Restart)** | IMPLEMENTED | NOT RECORDED | Container Stop/Start/Restart via Docker SDK |
| **Rollback Execution** | IMPLEMENTED | NOT RECORDED | Creates new deployment using prior known-good image tag or digest; strictly fails closed if image is missing from Docker daemon (never silently rebuilds from current source) |
| **Docker Container Restart Policy** | IMPLEMENTED | NOT RECORDED | Docker daemon `unless-stopped` restarts crashed containers |
| **ForgeLAB Continuous Self-Healing** | NOT IMPLEMENTED | — | Background crash-loop detection / auto-rollback deferred |
| **Secret Encryption at Rest** | IMPLEMENTED | NOT RECORDED | AES-256-GCM with unique 12-byte nonces |
| **Encrypted Local Agent Credentials** | IMPLEMENTED | NOT RECORDED | Verified agent session token encrypted via AES-256-GCM in `sources.encrypted_session_token`, decrypted during deployment; zero exposure over REST, WS, or logs |
| **Secret Log Redaction** | IMPLEMENTED | NOT RECORDED | In-memory pattern replacement (`[REDACTED]`) before write |
| **WebSocket Subscription Auth** | IMPLEMENTED | NOT RECORDED | Verifies user ownership of deployment project before subscribe |
| **Deployment & Service Log Streaming** | IMPLEMENTED | NOT RECORDED | Streams live build & runtime logs to `service-deployment:<uuid>`, `deployment:<uuid>`, and `project:<uuid>` |
| **Project-Level WebSocket Channels** | IMPLEMENTED | NOT RECORDED | Actively produces `service_deployment_log`, `service_status_change`, and `project_status` on `project:<uuid>` |
| **Frontend UI (Next.js 16 App Router)** | IMPLEMENTED | NOT RECORDED | Next.js 16.3.6 LTS, React 19, Tailwind CSS, landing page, dashboard, proxy.ts |
| **GitHub Webhook Auto-Deploy** | DEFERRED | — | Planned future feature |
| **Zero-Downtime Rolling Updates** | NOT IMPLEMENTED | — | Blue/green or proxy traffic shifting deferred |
| **Caddy Reverse Proxy & TLS** | NOT IMPLEMENTED | — | Reverse proxy routing not in MVP |
| **OpenTelemetry Observability** | NOT IMPLEMENTED | — | Metrics and distributed tracing deferred |
| **Container Resource Quotas** | IMPLEMENTED | NOT RECORDED | CPU (`NanoCPUs`), Memory (`Memory`), and PIDs (`PidsLimit`) enforced in Docker HostConfig via `ValidateAndBuildSecureHostConfig` |
| **Docker Trust Boundary Enforcement** | IMPLEMENTED | NOT RECORDED | Reject `/var/run/docker.sock` and sensitive mounts, `CapDrop: [ALL]`, `no-new-privileges: true`, reject host network |
| **Redis Job Lease Ownership** | IMPLEMENTED | NOT RECORDED | Unique lease owner tokens (`workerID:uuid`), Lua atomic renewal/release/recovery, O(1) state indexing, zero list scans |
| **Deep Dependency Readiness Check** | IMPLEMENTED | NOT RECORDED | `/ready` and `/health/ready` probe PostgreSQL, Redis, and Docker Engine concurrently with 0 secret exposure; `/health` remains lightweight liveness |
| **API & WebSocket Rate Limiting** | IMPLEMENTED | NOT RECORDED | Redis/in-memory sliding window throttling on `/auth/*`, `/sources/*`, deploy/rollback, and WebSocket connect/subscribe returning HTTP 429 |
| **Deployment Cancellation & Superseding** | IMPLEMENTED | NOT RECORDED | User cancellation of queued/in-progress deployments; newer service deployments atomically supersede obsolete queued deployments |
| **Automated Multi-Stage CI Pipeline** | IMPLEMENTED | NOT RECORDED | GitHub Actions (`.github/workflows/ci.yml`) covering backend vet/fmt/test, frontend lint/test/build, compose smoke, and migrations |
| **Role-Based Access Control (RBAC)**| NOT IMPLEMENTED | — | Single owner model active; teams/roles deferred |
| **WebSocket Event Replay Service** | NOT IMPLEMENTED | — | Replay from DB; client currently fetches REST logs first |
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
