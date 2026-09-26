# ForgeLAB — Implementation & Verification Roadmap

**Status:** Current Reference Specification  
**Current Phase:** Phase 9 — Physical Verification of Implemented MVP  
**Authoritative Verification Methodology:** Physical manual testing by the project owner ([docs/12-manual-verification.md](12-manual-verification.md))  

---

## Related Documents

- [INSTRUCTION.md](../INSTRUCTION.md) — Operational memory & mandatory agent workflow
- [docs/00-current-state.md](00-current-state.md) — Current state snapshot & matrix
- [docs/02-functional-requirements.md](02-functional-requirements.md) — Functional requirements
- [docs/04-architecture-decisions.md](04-architecture-decisions.md) — System architecture & data model
- [docs/12-manual-verification.md](12-manual-verification.md) — Master acceptance matrix & test procedures
- [docs/13-decisions.md](13-decisions.md) — Architecture Decision Records (ADRs)
- [docs/14-known-limitations.md](14-known-limitations.md) — Known limitations and investigation items

---

## Roadmap Methodology

To maintain engineering truth, this roadmap strictly distinguishes:
- **`IMPLEMENTED`**: Source code exists in the repository.
- **`PHYSICALLY VERIFIED`**: The project owner has manually tested the capability in the live environment and recorded the result.
- **`FUTURE / DEFERRED`**: Planned for subsequent development phases; must not be implemented prematurely.

> **CRITICAL RULE:** `IMPLEMENTED` must **never** be treated as equivalent to `PHYSICALLY VERIFIED`. Automated unit tests are valuable code artifacts, but physical manual verification by the human project owner is the authoritative completion gate.

---

## Phase 0: Project Scaffolding & Infrastructure ✅ [IMPLEMENTED]
> Foundation, architecture specifications, and local container stack

- [x] Engineering specifications & architecture contracts (`docs/01`–`docs/08`)
- [x] Go control-plane project initialization (`backend/`)
- [x] Multi-container Docker Compose environment (`docker-compose.yml`)
- [x] PostgreSQL 16 schema migrations (`backend/migrations/`)
- [x] Active deployment uniqueness constraint (`uq_active_deployment_per_project`)
- [x] Redis 7 queue and Pub/Sub container setup

---

## Phase 1: Authentication & User Identity ✅ [IMPLEMENTED]
> Secure user registration, login, and token management

- [x] User registration endpoint (`POST /api/auth/register`) with bcrypt hashing
- [x] User login endpoint (`POST /api/auth/login`) issuing JWT access and refresh tokens
- [x] JWT access token generation (HS256, 15m lifespan, `sub` claim)
- [x] Atomic refresh token single-use rotation (`POST /api/auth/refresh`)
- [x] Authentication middleware (`internal/middleware`) supporting headers and cookies
- [x] Session logout endpoint (`POST /api/auth/logout`)

---

## Phase 2: Project Management & Source Security ✅ [IMPLEMENTED]
> Project CRUD operations and host source path validation

- [x] Create project (`POST /api/projects`) with slug generation
- [x] List user-owned projects (`GET /api/projects`)
- [x] Get project details (`GET /api/projects/:id`)
- [x] Update project configuration (`PATCH /api/projects/:id`)
- [x] Delete project with container cleanup (`DELETE /api/projects/:id`)
- [x] Server-side project ownership authorization on all endpoints
- [x] Host path security validation (`PathValidator` blocking traversal, symlink bypasses, and system directories)

---

## Phase 3: Deployment Engine & Pipeline ✅ [IMPLEMENTED]
> End-to-end local build and execution pipeline

- [x] Trigger deployment (`POST /api/projects/:id/deployments`)
- [x] Concurrency check: database partial unique index blocks concurrent active builds
- [x] Redis deployment queue (`LPUSH` / `BRPOP`)
- [x] Background deployment worker loop with execution lock
- [x] Local source snapshotting: host directory copied to isolated build context (`data/builds/<id>`)
- [x] Docker image build from tar stream via Docker Engine SDK
- [x] Application container creation and dynamic host port allocation (`10000–60000`)
- [x] Enforced state transitions (`QUEUED` → `CLONING` → `BUILDING` → `STARTING` → `HEALTH_CHECKING` → `RUNNING` / `FAILED`)
- [x] Build and runtime log persistence in PostgreSQL (`deployment_logs`)
- [x] Deployment status API (`GET /api/projects/:id/deployments/:did`)
- [x] Deployment history API (`GET /api/projects/:id/deployments`)
- [x] **Deployment Safety Invariant:** Failed deployments preserve running container and current deployment pointer

---

## Phase 4: Health-Check Deployment Gate ✅ [IMPLEMENTED]
> Verification gate before promoting new releases

- [x] HTTP polling runner against allocated container host port
- [x] Configurable project health check path (default `/health`)
- [x] Deployment gate: 10 attempts (2-second interval) before declaring healthy
- [x] Promotion: `HEALTH_CHECKING` → `RUNNING` on HTTP 2xx/3xx, updating `current_deployment_id`
- [x] Failure gate: Failed health check transitions to `FAILED` and cleans up broken container without terminating previous release

---

## Phase 5: Lifecycle Controls & Rollback ✅ [IMPLEMENTED]
> Runtime management and release rollback

- [x] Application Stop (`POST /api/projects/:id/stop`) via Docker SDK `ContainerStop`
- [x] Application Start (`POST /api/projects/:id/start`) via Docker SDK `ContainerStart`
- [x] Application Restart (`POST /api/projects/:id/restart`) with 10-second timeout
- [x] Rollback endpoint (`POST /api/projects/:id/rollback`)
- [x] Rollback semantics: creates a new deployment referencing the prior known-good Docker image
- [x] Rollback safety: rollback follows standard pipeline and must pass health checking before promotion

---

## Phase 6: Environment Variables & Secrets ✅ [IMPLEMENTED]
> Secure configuration storage and log scrubbing

- [x] AES-256-GCM encryption with unique 12-byte nonces for project secrets
- [x] Create/update environment variable (`POST /api/projects/:id/env`)
- [x] List project environment variables (`GET /api/projects/:id/env`) with masked values (`••••••••`)
- [x] Delete environment variable (`DELETE /api/projects/:id/env/:key`)
- [x] In-memory decryption and injection into container environment at deploy time
- [x] Real-time log redactor (`LogRedactor`) scrubbing secret values (`[REDACTED]`) from build and runtime logs

---

## Phase 7: Realtime WebSocket Pipeline ✅ [IMPLEMENTED]
> Scoped event and log streaming to the browser

- [x] WebSocket endpoint (`GET /api/ws`) with token handshake authentication
- [x] Server-side subscription authorization (verifies user owns target deployment's project)
- [x] Redis Pub/Sub bridge distributing deployment events to Hub clients
- [x] Realtime log streaming for build steps and runtime stdout/stderr
- [x] Realtime deployment status change notifications
- [x] Channel isolation (`deployment:<uuid>`) preventing cross-project log leaks

---

## Phase 8: Operational Frontend Console ✅ [IMPLEMENTED]
> Next.js 14 web application

- [x] User authentication pages (`/login`, `/register`)
- [x] Dashboard with project listing and creation modal (`/dashboard`)
- [x] Project details console (`/projects/[id]`) with live status badge and port link
- [x] Realtime terminal viewer with auto-scroll and stream coloring
- [x] Deployment history browser with on-click historical log retrieval
- [x] Environment variables and secrets manager
- [x] Action bar controls: Deploy, Stop, Start, Restart, Rollback, and Delete

---

## Phase 9: Physical Verification of Implemented MVP ⏳ [ACTIVE PHASE]
> Manual verification by project owner in the live development environment ([docs/12-manual-verification.md](12-manual-verification.md))

- [ ] Procedure 1: Authentication & Token Lifecycle (NOT RECORDED)
- [ ] Procedure 2: Project Creation & Ownership Enforcement (NOT RECORDED)
- [ ] Procedure 3: Local Deployment & Lifecycle Happy Path (NOT RECORDED)
- [ ] Procedure 4: Failed Deployment & Safety Invariant Preservation (NOT RECORDED)
- [ ] Procedure 5: Rollback Semantics & Image Re-Deployment (NOT RECORDED)
- [ ] Procedure 6: WebSocket Isolation & Cross-Project Privacy (NOT RECORDED)
- [ ] Procedure 7: Secret Encryption & Streaming Log Redaction (NOT RECORDED)
- [ ] Procedure 8: Host Path Security Validation & System Dir Blocking (NOT RECORDED)

---

## Future Platform Phases (POST-MVP / DEFERRED)

These capabilities are intentionally deferred until Phase 9 physical verification is complete:

### Phase 10: GitHub OAuth Integration & Webhooks
- [docs/15-github-integration.md](15-github-integration.md) architecture implementation
- OAuth application handshake and encrypted token persistence
- Authorized repository and branch picker
- HMAC-signed GitHub webhook receiver for automated push-to-deploy

### Phase 11: Edge Routing & Reverse Proxy (Caddy)
- Caddy reverse proxy integration via dynamic configuration API
- Automatic subdomain routing (`<project-slug>.forgelab.local`)
- Automated Let's Encrypt TLS certificate issuance for public domains
- True zero-downtime rolling deployments via proxy traffic shifting

### Phase 12: Reliability & Continuous Self-Healing
- Background runtime health monitoring loop
- Automated container restart on unexpected exit with exponential backoff
- Configurable health check thresholds (interval, timeout, retries)
- Automated rollback trigger on sustained runtime container crashes

### Phase 13: Observability & Operational Hardening
- OpenTelemetry distributed tracing and metrics exporter
- Container CPU, memory, and disk I/O metrics streaming
- Enforced container resource quotas (`NanoCPUs`, `Memory`)
- Terminal log rendering virtualization in frontend

### Phase 14: Enterprise & Team Governance
- Multi-user organizations and workspaces
- Role-Based Access Control (RBAC: Owner, Admin, Developer, Viewer)
- Immutable audit log of configuration edits, secret changes, and deployments
- ForgeLAB CLI tool (`forge deploy`, `forge logs`)
