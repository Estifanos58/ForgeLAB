# ForgeLAB — Agent Operational Memory & Engineering Instructions

You are an AI coding agent operating inside Antigravity, working on the **ForgeLAB** repository (`Estifanos58/ForgeLAB`).

Treat this document as the project's **persistent engineering memory**. It establishes the mandatory developer workflow, architectural invariants, documentation truth rules, and implementation constraints.

---

## 1. Mandatory Agent Workflow

Every agent working on this codebase must adhere strictly to the following lifecycle.

### Before Changing Any Code:
1. **Read `INSTRUCTION.md`** (this document).
2. **Read `docs/00-current-state.md`** to know the audited implementation baseline commit, documentation status, environment requirements, and verification state.
3. **Read the relevant functional requirements** in [docs/02-functional-requirements.md](docs/02-functional-requirements.md).
4. **Read the relevant architecture, security, API, or frontend document** (e.g., [docs/04-architecture-decisions.md](docs/04-architecture-decisions.md), [docs/05-security-trust-boundaries.md](docs/05-security-trust-boundaries.md), [docs/09-api-contract.md](docs/09-api-contract.md), [docs/10-frontend-architecture.md](docs/10-frontend-architecture.md), [docs/11-development-environment.md](docs/11-development-environment.md)).
5. **Determine whether the requested task is:**
   - Current implementation bug fix
   - Current implementation refactor
   - New implementation for an MVP capability
   - Future platform functionality (must **not** be started prematurely)
   - Documentation update
   - Unresolved architectural question
6. **Do not implement future functionality** simply because an architecture document or roadmap mentions it.
7. **Do not replace an established architectural decision** without recording an updated Architecture Decision Record in [docs/13-decisions.md](docs/13-decisions.md).

### After Implementation:
1. **Re-check current implementation** against the modified files using static analysis and compilation checks.
2. **Update `docs/00-current-state.md`** if the project state or capabilities matrix changed.
3. **Update the relevant architectural / contract documents** (e.g., [docs/09-api-contract.md](docs/09-api-contract.md), [docs/06-websocket-contract.md](docs/06-websocket-contract.md)).
4. **Do not claim physical verification.** Automated tests, mocks, or compilation success are **not** physical verification.
5. **Provide a detailed manual verification procedure** in [docs/12-manual-verification.md](docs/12-manual-verification.md) for the human project owner.

---

## 2. Core Documentation & Verification Principles

### Documentation Truth Rule
```text
Code tells us what is currently implemented.

Documentation must distinguish:
- CURRENT BEHAVIOR (what the code actually does today)
- HISTORICAL DESIGN (earlier proposals or replaced approaches)
- FUTURE DESIGN (planned, agreed, but not yet implemented)
- UNRESOLVED IDEAS (open questions and alternatives)
- VERIFIED BEHAVIOR (manually validated by the project owner)

Never collapse these categories into one.
```

Whenever documentation and code disagree:
1. Determine what the code actually does now.
2. Document the current implementation honestly.
3. Classify older behavior as historical/proposed/future when appropriate.
4. Do not silently change code to make the documentation true.

### Durable Commit Baseline Rule
The documentation intentionally records the implementation commit against which the implementation state was audited (`468940881338054d0771cac4be633da698a89d0f`) and the documentation overhaul commit (`fb974c216f3f3150b6e90c7da7952e4490d31f57`). It does not attempt to track repository HEAD because documentation commits themselves change HEAD.

### Development Environment Reality Rule
ForgeLAB requires access to the host filesystem to snapshot local repositories. The documentation must maintain a clear distinction between:
- **Environment A (Host-Run Backend):** The Go backend runs directly on the host (with PostgreSQL and Redis in Compose). This is required when deploying arbitrary host repository paths outside the configured Docker bind mount.
- **Environment B (Docker Compose 6-Service Stack):** All services (`postgres`, `redis`, `migrate`, `backend`, `frontend`, `caddy`) run via `docker compose up --build`. The backend container mounts `${FORGELAB_HOST_SOURCE_ROOT:-./}:/host-projects:ro` for mapped local repositories, the frontend proxies `/api/*` internally via `BACKEND_INTERNAL_URL=http://backend:8080`, and `caddy` (`infra/caddy/Caddyfile`) provides reverse-proxy routing and persistent ACME TLS storage (`forgelab_caddy_data`, `forgelab_caddy_config`) via `CADDY_ADMIN_URL`. Host directories outside `FORGELAB_HOST_SOURCE_ROOT` still require Environment A or the host Local Agent (`cmd/agent`).

### Physical Verification Rule
The project's authoritative validation methodology is:
```text
IMPLEMENT  ──►  PHYSICAL MANUAL VERIFICATION  ──►  RECORD RESULT  ──►  PROCEED
```

It is **NOT**:
```text
IMPLEMENT  ──►  WRITE AUTOMATED TESTS  ──►  CALL FEATURE COMPLETE
```

- **The human user is the person who performs physical verification.**
- Agents may inspect code, build binaries, run static analysis, review logs, and construct verification procedures.
- Agents must **never** mark a feature as `PHYSICALLY VERIFIED` merely because unit tests pass, a container compiles, or a mock succeeds.
- Existing automated tests in `backend/internal/...` are useful existing artifacts, but they are **non-authoritative**.

---

## 3. Product Vision & Context

ForgeLAB is a **self-hosted application deployment platform**.

A developer gives ForgeLAB a project repository, and ForgeLAB manages the resulting application's lifecycle: building, deploying, running, monitoring, logging, restarting, and rolling back.

### Why ForgeLAB Exists
- The developer already has extensive experience with TypeScript/NestJS, Spring Boot, PostgreSQL, Redis, Kafka, Docker, WebSockets, and microservices.
- **Go for the control plane is non-negotiable.** It is a deliberate learning dimension. Do not replace Go with NestJS or Spring Boot.
- The project is designed to teach when additional infrastructure is justified. Do not introduce microservices or Kubernetes prematurely.

### Non-Negotiable Constraints
1. **Self-hosted and local-first.** The core project runs on local infrastructure with $0 development cost.
2. **Single control-plane architecture for MVP.** Backend Go binary + PostgreSQL 16 + Redis 7 + Docker Engine + Next.js 16.3.6 LTS + React 19 frontend.
3. **No premature distributed systems.** Worker separation, multi-node scheduling, and service meshes are earned by real operational needs.

---

## 4. Key Architectural Invariants

### Invariant 1: Deployment Safety Invariant
```text
A is running  ──►  B is deployed  ──►  B fails health check  ──►  A remains running
```
- A failed deployment must **never** terminate or replace the previous running deployment.
- `projects.current_deployment_id` updates **only** after the new deployment passes its HTTP health check gate.

### Invariant 2: Rollback Semantics
```text
Current deployment A is active
        ↓
Rollback is requested
        ↓
New deployment record C is created using previous known-good deployment B's image tag
        ↓
Deployment C follows normal deployment pipeline (container creation, port allocation, health check)
        ↓
Deployment C is promoted to current ONLY after passing health check
```

### Invariant 3: Direct Build Mode & Streaming Docker Build Context
- For direct local-directory deployments (`source_type: "local_directory"`), ForgeLAB builds directly from the validated host directory without duplicating tens of thousands of files into `data/builds/<deployment-id>`.
- The Docker build context is streamed concurrently using `io.Pipe()` and `tar.Writer`, completely eliminating in-memory `bytes.Buffer` and `io.ReadAll` memory buffering.
- Build log streams are bounded with maximum line sizes (64KB) to prevent runaway memory consumption.
- `.dockerignore` rules early-prune ignored directories (e.g. `node_modules`, `.git`, `.next`, cache dirs) before filesystem descent.
- Snapshot mode is preserved as a configurable option or for uploaded archives (`data/sources/<source-id>`) where isolated extraction is appropriate with restricted `0700`/`0600` permissions.

### Invariant 4: Server-Side Authorization & Channel Isolation
- Projects and deployments belong to a specific `owner_id` (User UUID).
- Server validates ownership on every REST request and WebSocket subscription.
- Events published to `deployment:<uuid-A>` must **never** be delivered to subscribers of `deployment:<uuid-B>`.

### Invariant 5: Identifier Standard (UUID)
- All durable entities (`users`, `projects`, `deployments`, `environment_variables`) use **UUIDv4**.
- ULID proposals from past discussions remain deferred. Do not migrate identifiers to ULID without an explicit architectural change.

### Invariant 6: Container Isolation & Trust Boundaries
- Containers must never mount the host Docker socket (`/var/run/docker.sock` or `\\.\pipe\docker_engine`) or sensitive host system directories.
- Privileged mode is strictly forbidden; all containers run with `no-new-privileges:true`, `CapDrop: ["ALL"]`, and a strict security capability allowlist (`CapAdd` restricted exclusively to `["CHOWN", "SETGID", "SETUID"]`, failing closed with `ErrCapabilityNotAllowed` on any disallowed capability rather than filtering a blacklist).
- Network isolation strictly rejects `host` networking and `container:` network namespace sharing.
- Resource limits are truthful: unsupported `ephemeral_storage_mb` limits are rejected with an explicit error rather than silently accepted.

### Invariant 7: Hardened Source Ingestion, Recovery & Agent Privacy
- GitHub imports enforce strict limits: 100MB compressed, 100MB uncompressed, 10,000 files, 50MB single file, and a 3-minute extraction timeout. Import jobs persist state in `repository_imports`, support cancellation and retries, and interrupted in-flight jobs recover safely across backend restarts (`RecoverInterruptedImports`).
- Source archives use atomic cache creation (`.tmp.<uuid>` directories renamed to destination upon completion marker verification) with restricted `0700` directory and `0600` file permissions. Partial or failed extractions are cleaned up reliably.
- Host path validation (`PathValidator`, Local Agent, discovery, and TAR archiving) enforces Windows and POSIX canonicalization: normalizes Windows extended/reparse prefixes (`\\?\`, `\??\`, 8.3 short paths), rejects UNC/device paths and drive roots, evaluates symlinks/junctions safely, and rejects traversal escapes.
- The local agent (`cmd/agent`) binds strictly to loopback `127.0.0.1:4142` by default unless explicitly configured otherwise.
- The agent's `/api/agent/status` endpoint scrubs `agent_id` and active session counts for unauthenticated requests, preventing credential discovery and enumeration.
- Asynchronous queue workers perform heartbeat cancellation checks to abort cancelled jobs promptly, and queue reconciliation covers parent release deployments.

---

## 5. Scope Boundaries (Current MVP vs. Future)

### Current Implemented MVP Scope:
- User registration, login, JWT token auth, atomic refresh token rotation, and isolated guest sessions (`FORGELAB_ALLOW_GUEST_MODE`)
- Project CRUD, ownership enforcement, host path security validation (`PathValidator` with allowed roots, Windows junction/8.3/`\??\` canonicalization, and host-to-container translation)
- Direct local-directory deployment (`source_type: "local_directory"`): directory validation (`POST /api/sources/local/validate`), zero-upload ingestion, direct filesystem build source
- Streaming Docker build context (`io.Pipe()`) without in-memory archive buffering; bounded 64KB log lines
- Local archive upload fallback (`source_type: "local_upload"`) via `/api/sources/upload` and isolated source workspaces with restricted 0700/0600 permissions
- Authorized and public GitHub repository import (`source_type: "github"` / `"github_public"`) with GitHub App, OAuth, or fine-grained PAT, hardened extraction quotas, atomic cache creation, pinned commit SHA resolution, and restart-recoverable background import jobs with cancel & retry
- Automated GitHub push-to-deploy webhook receiver (`POST /api/webhooks/github`) with HMAC-SHA256 verification and replay delivery deduplication (`GET`/`PATCH /api/projects/{id}/webhook`)
- Local agent CLI (`cmd/agent`) with loopback `127.0.0.1:4142` default binding, identity verification probe, Windows interactive STA folder picker, and unauthenticated status protection
- Docker SDK image builds, dynamic host port allocation (`10000–60000`) with conflict retries
- Deployment state transitions (`QUEUED` → `CLONING` → `BUILDING` → `STARTING` → `HEALTH_CHECKING` → `RUNNING` / `FAILED`)
- Deployment-time health check gating (`auto`, `http`, `tcp`, `docker`, `none`) and DAG multi-service compose orchestration
- Continuous runtime container health monitoring (`CheckContinuousRuntimeHealth`) with configurable self-healing restarts and automatic rollback (`FORGELAB_SELF_HEAL_*`, `FORGELAB_AUTO_ROLLBACK_*`), preserving healthy releases and verifying restored containers
- Opt-in `blue_green` service deployment mode (`/proxy/services/{serviceId}/*`) alongside `standard` mode
- Caddy reverse proxy integration in Docker Compose (`infra/caddy/Caddyfile`, persistent `forgelab_caddy_data` & `forgelab_caddy_config`) and custom domain management (`GET/POST/DELETE /api/projects/{id}/domains`, `POST .../verify`) with truthful `proxy_unconfigured` / `https_active` status reporting
- Persistent volume inspection, Docker `system/df` disk usage visibility with 85% threshold warning, and hardened `.tar.gz` volume backup/restore (`/api/projects/{id}/volumes/...`)
- Container resource quotas (CPU `NanoCPUs`, Memory `Memory`, and PIDs `PidsLimit` enforced in Docker HostConfig) and strict capability allowlist (`CapDrop: ["ALL"]`, `CapAdd` allowlist only)
- Non-development environment validation hardening (rejecting weak secrets, default passwords, insecure cookies, unsafe source roots) and Next.js Content-Security-Policy headers (`next.config.ts`)
- Application lifecycle controls: Stop, Start, Restart, and Rollback creating a new release from prior known-good image
- AES-256-GCM encrypted environment variables & streaming log secret redactor
- Scoped WebSocket log and status streaming (`deployment:<uuid>`, `service-deployment:<uuid>`, `project:<uuid>`) with single-use tickets (`POST /api/ws/ticket`), strict Origin validation, and PostgreSQL sequence replay
- Backend-sampled & TTL-cached runtime container metrics (`GET /api/projects/{id}/metrics`), PostgreSQL historical metrics retention (`GET /api/projects/{id}/metrics/history`), redacted operational audit trail (`GET /api/audit-logs`, `GET /api/projects/{id}/audit-logs`), and operational diagnostics (`GET /api/diagnostics`, `GET /api/projects/{id}/diagnostics`)
- Next.js 16 frontend: Dashboard, project console, domains/volumes/metrics/topology/audit tabs, secrets manager, live terminal viewer, and multi-mode project import wizard

### Explicitly Deferred Future Scope (Do NOT Implement):
- Pull-request preview environments (ephemeral PR environments and auto-teardown)
- Ephemeral storage quotas (requires host filesystem quota support; rejected explicitly)
- Multi-user organizations & Role-Based Access Control (RBAC)
- ForgeLAB CLI (`forge deploy`, `forge logs`)
- Multi-node distributed workers & Kubernetes orchestration
- OpenTelemetry (`OTel`) distributed tracing & external Prometheus `/api/metrics` scraping exporter (internal container sampled/historical runtime metrics and diagnostics are implemented)
