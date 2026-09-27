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
  - **Local Computer Import (`source_type: "local"`):** Users select a directory or upload an archive directly from their browser. Files are uploaded via `POST /api/sources/upload`. The service normalizes browser-style wrapper directories (e.g. `MyProject/package.json` -> `package.json`), strictly enforces the 100MB uncompressed limit without silent truncation, validates against Zip Slip / path traversal, and skips build artifacts.
  - **Source Ownership & Deletion:** Persistent ownership is recorded in the `source_workspaces` database table. Source lookups, deletions (`DELETE /api/sources/{id}`), and project creations strictly verify that the authenticated user owns the source workspace.
  - **GitHub Repository Import (`source_type: "github"`):** Users authorize GitHub repository access via dedicated OAuth code exchange (`repo` scope). The backend queries GitHub's API for repositories and branches, analyzes project metadata remotely via `GET /api/integrations/github/repositories/{owner}/{repo}/detect`, and retrieves repository tarballs into isolated source workspaces. Tokens are encrypted at rest with AES-256-GCM in `github_integrations`.
  - **Host Filesystem Path (Legacy/Fallback):** Validated by `internal/security/PathValidator` if explicitly provided when backend runs on host.
- **Project Detection & Build Strategy:**
  - Automated heuristic detection (`internal/detector`) analyzes metadata (`package.json`, `go.mod`, `requirements.txt`, `Cargo.toml`, `pom.xml`, `build.gradle`, `Dockerfile`) to identify language, framework, build command, start command, suggested port, and health check endpoint.
  - Generates deterministic, executable multi-stage Dockerfiles across Node.js, Next.js, Vite/static, FastAPI, Flask, Django, Go, Maven/Spring Boot, Gradle/Spring Boot, and Rust—avoiding shell glob patterns in exec-form `CMD`.
- **Dynamic Port Mapping & Health Strategies:**
  - Container internal ports (3000, 8000, 8080) are mapped to dynamic host ports (`10000–60000`).
  - Readiness checks support `auto`, `http`, `tcp`, and `none`. `auto` requires HTTP 2xx/3xx (explicitly rejecting HTTP 5xx server errors as healthy) or verified TCP socket connectivity before promotion.
- **Port Allocation:** Dynamic host port allocation in the range **`10000–60000`** managed by `internal/network/PortManager`.
- **Realtime Pipeline:** Deployment events and container logs are pushed to Redis Pub/Sub channels (`forgelab:pubsub:deployment:<uuid>`), bridged to an in-memory WebSocket Hub, and streamed to authenticated client subscriptions.
- **Rollback & Safety Invariant:** When a deployment fails at any stage (snapshot, build, container startup, or readiness check), the previous running deployment container remains untouched, and `projects.current_deployment_id` remains unchanged. Rollback creates a new deployment referencing the prior known-good Docker image.
- **Container Restart vs. Platform Self-Healing:**
  - **Docker Runtime Restart Policy:** Containers are created with `RestartPolicy: "unless-stopped"`. The Docker daemon itself automatically restarts crashed containers.
  - **ForgeLAB-Level Self-Healing:** ForgeLAB does **not** implement continuous background runtime health monitoring, crash-loop detection, or automated application rollback after promotion.

### Current Implementation vs. Verification Stage
- **Implementation Stage:** The full-stack platform (Go backend, Next.js 16 frontend, PostgreSQL, Redis, Migrations, Worker, and Google/GitHub OAuth authentication) is **IMPLEMENTED** in the codebase.
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
| **Local Computer Source Upload** | IMPLEMENTED | NOT RECORDED | Ingests directory/archive to isolated workspace via `/api/sources/upload`, normalizes wrapper directories, enforces 100MB limit |
| **Source Workspace Ownership & Deletion**| IMPLEMENTED | NOT RECORDED | Persistent ownership in `source_workspaces` table, `DELETE /api/sources/{id}`, multi-tenant user isolation |
| **GitHub Repository Authorization** | IMPLEMENTED | NOT RECORDED | Dedicated OAuth `repo` scope flow; encrypted at rest via AES-256-GCM |
| **GitHub Repository & Branch Picker**| IMPLEMENTED | NOT RECORDED | Scoped listing of accessible repos and branches via `/api/integrations/github` |
| **Heuristic Runtime Detection** | IMPLEMENTED | NOT RECORDED | Heuristic inspection of package.json, go.mod, python, etc. |
| **Automatic Build Strategy** | IMPLEMENTED | NOT RECORDED | Multi-stage Dockerfile generation without requiring user Dockerfile |
| **Dynamic Internal Port Mapping** | IMPLEMENTED | NOT RECORDED | Container internal port (3000, 8000, etc.) mapped to dynamic host port |
| **Readiness Strategy (HTTP/TCP/None)**| IMPLEMENTED | NOT RECORDED | Flexible health check gating supporting HTTP, TCP, and None |
| **Local Host Source Validation** | IMPLEMENTED | NOT RECORDED | Legacy/host-only fallback for direct filesystem paths |
| **Local Source Snapshotting** | IMPLEMENTED | NOT RECORDED | Source copied to isolated working directory before build |
| **Redis Deployment Queue** | IMPLEMENTED | NOT RECORDED | `LPUSH` / `BRPOP` queue with `SETNX` worker concurrency lock |
| **Docker Build Pipeline** | IMPLEMENTED | NOT RECORDED | Docker SDK ImageBuild via tar stream, log parsing |
| **Dynamic Host Port Allocation** | IMPLEMENTED | NOT RECORDED | Scans available ports in range `10000–60000` |
| **Deployment State Machine** | IMPLEMENTED | NOT RECORDED | Enforced: `QUEUED`→`CLONING`→`BUILDING`→`STARTING`→`HEALTH_CHECKING`→`RUNNING`/`FAILED` |
| **Health-Check Deployment Gate** | IMPLEMENTED | NOT RECORDED | Polling gate before promotion supporting HTTP, TCP, and None |
| **Deployment Safety Invariant** | IMPLEMENTED | NOT RECORDED | Failed health checks keep previous running deployment alive |
| **Lifecycle Controls (Stop/Start/Restart)** | IMPLEMENTED | NOT RECORDED | Container Stop/Start/Restart via Docker SDK |
| **Rollback Execution** | IMPLEMENTED | NOT RECORDED | Creates new deployment using prior known-good image tag |
| **Docker Container Restart Policy** | IMPLEMENTED | NOT RECORDED | Docker daemon `unless-stopped` restarts crashed containers |
| **ForgeLAB Continuous Self-Healing** | NOT IMPLEMENTED | — | Background crash-loop detection / auto-rollback deferred |
| **Secret Encryption at Rest** | IMPLEMENTED | NOT RECORDED | AES-256-GCM with unique 12-byte nonces |
| **Secret Log Redaction** | IMPLEMENTED | NOT RECORDED | In-memory pattern replacement (`[REDACTED]`) before write |
| **WebSocket Subscription Auth** | IMPLEMENTED | NOT RECORDED | Verifies user ownership of deployment project before subscribe |
| **Deployment-Scoped Log Streaming** | IMPLEMENTED | NOT RECORDED | Streams live build & runtime logs to `deployment:<uuid>` |
| **Frontend UI (Next.js 16 App Router)** | IMPLEMENTED | NOT RECORDED | Next.js 16.3.6 LTS, React 19, Tailwind CSS, landing page, dashboard, proxy.ts |
| **GitHub Webhook Auto-Deploy** | DEFERRED | — | Planned future feature |
| **Zero-Downtime Rolling Updates** | NOT IMPLEMENTED | — | Blue/green or proxy traffic shifting deferred |
| **Caddy Reverse Proxy & TLS** | NOT IMPLEMENTED | — | Reverse proxy routing not in MVP |
| **OpenTelemetry Observability** | NOT IMPLEMENTED | — | Metrics and distributed tracing deferred |
| **Container Resource Quotas** | NOT IMPLEMENTED | — | CPU/Memory caps not passed to Docker HostConfig |
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
