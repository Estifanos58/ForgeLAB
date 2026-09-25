# ForgeLab — Implementation Roadmap

**Status:** Current  
**Last Updated:** 2026-09-25  

---

## Roadmap Structure

This roadmap shows **dependency order**, not a feature wishlist. Each phase depends on the completion of the previous phase.

---

## Phase 0: Project Foundation ✅
> Documentation + project scaffolding

- [x] Product vision document
- [x] Functional requirements (MVP vs. future)
- [x] Technology decisions
- [x] Architecture decisions (data model, state machine, identity)
- [x] Security & trust boundaries
- [x] WebSocket contract & isolation rules
- [x] Implementation roadmap (this document)
- [ ] Open questions & future roadmap document
- [ ] Go project initialization
- [ ] Docker Compose for local development (PostgreSQL + Redis)
- [ ] Database migration infrastructure
- [ ] Initial database schema

---

## Phase 1: Authentication & User Management
> Foundation for all authorization

**Dependencies:** Phase 0 complete

| Task | Description |
|------|-------------|
| 1.1 | User registration endpoint (POST /api/auth/register) |
| 1.2 | Password hashing (bcrypt) |
| 1.3 | User login endpoint (POST /api/auth/login) |
| 1.4 | JWT access token generation |
| 1.5 | JWT refresh token (POST /api/auth/refresh) |
| 1.6 | Authentication middleware |
| 1.7 | Protected route pattern |

**Validation:** Can register, login, and access protected endpoints.

---

## Phase 2: Project Management
> CRUD operations for projects

**Dependencies:** Phase 1 complete (auth middleware)

| Task | Description |
|------|-------------|
| 2.1 | Create project (POST /api/projects) |
| 2.2 | List user's projects (GET /api/projects) |
| 2.3 | Get project details (GET /api/projects/:id) |
| 2.4 | Update project settings (PATCH /api/projects/:id) |
| 2.5 | Delete project (DELETE /api/projects/:id) |
| 2.6 | Authorization: owner-only access |
| 2.7 | Local repository path validation |

**Validation:** Can create, list, view, update, delete projects. Cannot access other users' projects.

---

## Phase 3: Deployment Engine (Core)
> The critical vertical slice

**Dependencies:** Phase 2 complete

| Task | Description |
|------|-------------|
| 3.1 | Trigger deployment (POST /api/projects/:id/deployments) |
| 3.2 | Deployment record creation (QUEUED) |
| 3.3 | Redis deployment queue |
| 3.4 | Deployment worker (goroutine) |
| 3.5 | Source acquisition (copy local source) |
| 3.6 | Docker image build via Docker SDK |
| 3.7 | Container creation and startup |
| 3.8 | Deployment state transitions (with DB updates) |
| 3.9 | Build log capture and storage |
| 3.10 | Deployment status API (GET /api/projects/:id/deployments/:did) |
| 3.11 | List deployments (GET /api/projects/:id/deployments) |
| 3.12 | Deployment safety: failed deploy preserves running version |

**Validation:** Can trigger a deployment for a Dockerfile-based project, watch it build, and see the container running. Failed deployments don't destroy working ones.

---

## Phase 4: Health Checks
> Verify deployed applications are actually working

**Dependencies:** Phase 3 complete (running containers)

| Task | Description |
|------|-------------|
| 4.1 | Health check runner (HTTP GET to configurable path) |
| 4.2 | Health check integration into deployment pipeline |
| 4.3 | HEALTH_CHECKING → RUNNING transition |
| 4.4 | Health check failure → FAILED transition |
| 4.5 | Periodic health monitoring for running containers |
| 4.6 | Status transitions: RUNNING → CRASHED detection |

**Validation:** Deployment only reaches RUNNING after health check passes. Unhealthy app is marked FAILED.

---

## Phase 5: Application Lifecycle Management
> Stop, start, restart, rollback

**Dependencies:** Phase 4 complete

| Task | Description |
|------|-------------|
| 5.1 | Stop application (POST /api/projects/:id/stop) |
| 5.2 | Start application (POST /api/projects/:id/start) |
| 5.3 | Restart application (POST /api/projects/:id/restart) |
| 5.4 | Rollback to previous deployment (POST /api/projects/:id/rollback) |
| 5.5 | Rollback creates new deployment from known-good image |
| 5.6 | Rollback follows deployment safety invariant |

**Validation:** Can stop, start, restart. Rollback creates new deployment that follows full lifecycle.

---

## Phase 6: Environment Variables & Secrets
> Secure configuration management

**Dependencies:** Phase 3 complete

| Task | Description |
|------|-------------|
| 6.1 | AES-GCM encryption implementation |
| 6.2 | Create/update env var (POST /api/projects/:id/env) |
| 6.3 | List env vars (GET /api/projects/:id/env) — keys only, values masked |
| 6.4 | Delete env var (DELETE /api/projects/:id/env/:key) |
| 6.5 | Inject env vars into container at deployment time |
| 6.6 | Secret redaction in log pipeline |

**Validation:** Secrets encrypted at rest, injected at deploy time, redacted from logs, never returned in plaintext.

---

## Phase 7: WebSocket & Live Logs
> Realtime streaming to browser

**Dependencies:** Phase 3 complete (deployment logs exist), Phase 6 complete (secret redaction)

| Task | Description |
|------|-------------|
| 7.1 | WebSocket endpoint (GET /api/ws) |
| 7.2 | JWT authentication on WebSocket upgrade |
| 7.3 | WebSocket Hub implementation |
| 7.4 | Subscribe/unsubscribe protocol |
| 7.5 | Subscription authorization (ownership check) |
| 7.6 | Redis pub/sub → WebSocket Hub bridge |
| 7.7 | Deployment log streaming (build-time) |
| 7.8 | Runtime log streaming (container stdout/stderr) |
| 7.9 | Deployment status change events |
| 7.10 | Channel isolation verification |

**Validation:** Can subscribe to a deployment and see live build/runtime logs. Cannot subscribe to another user's deployment.

---

## Phase 8: REST API Completeness
> Historical logs, deployment history

**Dependencies:** Phases 3-7 complete

| Task | Description |
|------|-------------|
| 8.1 | Get deployment logs (GET /api/projects/:id/deployments/:did/logs) |
| 8.2 | Get runtime logs (GET /api/projects/:id/logs) |
| 8.3 | Deployment history with filtering/pagination |
| 8.4 | Project status summary |
| 8.5 | API error handling standardization |
| 8.6 | API documentation |

**Validation:** Complete REST API for all MVP operations.

---

## Phase 9: Frontend (Next.js)
> Dashboard and management UI

**Dependencies:** Phases 1-8 complete (stable API)

| Task | Description |
|------|-------------|
| 9.1 | Next.js project setup |
| 9.2 | Authentication pages (login, register) |
| 9.3 | Dashboard (project list, status overview) |
| 9.4 | Project detail view |
| 9.5 | Deployment trigger and progress |
| 9.6 | Live log viewer (WebSocket) |
| 9.7 | Deployment history view |
| 9.8 | Stop/start/restart/rollback controls |
| 9.9 | Environment variable management |
| 9.10 | Project settings |

**Validation:** Complete end-to-end workflow in the browser.

---

## Phase 10: Integration Testing & Polish
> Validate critical behaviors

**Dependencies:** Phase 9 complete

| Task | Description |
|------|-------------|
| 10.1 | Auth/authorization integration tests |
| 10.2 | Deployment lifecycle integration tests |
| 10.3 | Failed deployment preservation tests |
| 10.4 | Rollback behavior tests |
| 10.5 | WebSocket isolation tests |
| 10.6 | Secret redaction tests |
| 10.7 | End-to-end deployment test with sample app |

---

## Future Phases (NOT MVP)

These are explicitly deferred:

- **GitHub OAuth integration** — import from GitHub repos
- **Automated deployments** — webhook-triggered builds
- **Caddy integration** — reverse proxy / custom domains / HTTPS
- **OpenTelemetry** — metrics, tracing, observability dashboard
- **Resource limits** — CPU/memory caps per container
- **Teams / RBAC** — organization roles and permissions
- **CLI** — `forge deploy`, `forge logs`, etc.
- **Self-healing** — automatic restart/rollback on failure
- **Zero-downtime deployments** — blue/green or rolling
- **Multi-environment** — dev/staging/production
- **Audit logging** — action history for accountability
- **Multi-node workers** — distributed deployment processing
