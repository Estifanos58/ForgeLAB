# ForgeLab — Implementation Roadmap

**Status:** Current  
**Last Updated:** 2026-09-25  

---

## Roadmap Structure

This roadmap tracks implementation progress across all core modules. Every requirement is categorized according to its current status in the codebase:
- **CURRENT AND IMPLEMENTED**: Complete, verified, and active in the repository.
- **CURRENT BUT NOT YET IMPLEMENTED**: Planned for immediate upcoming MVP scope.
- **FUTURE**: Post-MVP features.
- **DEFERRED / REJECTED**: Explicitly excluded from single-node control-plane architecture.

---

## Phase 0: Project Foundation ✅ [CURRENT AND IMPLEMENTED]
> Documentation + project scaffolding + Docker environment

- [x] Product vision document
- [x] Functional requirements (MVP vs. future)
- [x] Technology decisions
- [x] Architecture decisions (data model, state machine, identity)
- [x] Security & trust boundaries
- [x] WebSocket contract & isolation rules
- [x] Implementation roadmap
- [x] Open questions & future roadmap document
- [x] Go project initialization (`backend`)
- [x] Docker Compose environment (PostgreSQL + Redis + Backend + Frontend)
- [x] Database migration infrastructure (golang-migrate)
- [x] Database schema & active deployment unique constraints

---

## Phase 1: Authentication & User Management ✅ [CURRENT AND IMPLEMENTED]
> Foundation for all authorization

- [x] User registration endpoint (`POST /api/auth/register`)
- [x] Password hashing (bcrypt)
- [x] User login endpoint (`POST /api/auth/login`)
- [x] JWT access token generation with unified `sub` claim (HS256, strictly validated issuer `forgelab`)
- [x] Atomic refresh token rotation & single-use replay prevention (`POST /api/auth/refresh`)
- [x] Authentication middleware & cookie/header token retrieval
- [x] Session revocation endpoint (`POST /api/auth/logout`)

---

## Phase 2: Project Management ✅ [CURRENT AND IMPLEMENTED]
> CRUD operations for projects & local source security

- [x] Create project (`POST /api/projects`)
- [x] List user's projects (`GET /api/projects`)
- [x] Get project details (`GET /api/projects/:id`)
- [x] Update project settings (`PATCH /api/projects/:id`)
- [x] Delete project with runtime container cleanup (`DELETE /api/projects/:id`)
- [x] Server-side owner authorization enforcement
- [x] Host path security validator (canonicalization, path traversal defense, system dir blocking)

---

## Phase 3: Deployment Engine (Core) ✅ [CURRENT AND IMPLEMENTED]
> The critical vertical slice & Docker Engine integration

- [x] Trigger deployment (`POST /api/projects/:id/deployments`)
- [x] Atomic active deployment check & deployment record creation (`QUEUED`)
- [x] Redis deployment queue (`LPUSH` / `BRPOP`)
- [x] Background deployment worker loop with execution lock (`SETNX`)
- [x] Isolated deployment snapshotting (copies host path into isolated working directory)
- [x] Docker image build via Docker SDK
- [x] Container creation and host-port allocation
- [x] Enforced state transitions (`QUEUED` -> `BUILDING` -> `STARTING` -> `HEALTH_CHECKING` -> `RUNNING` / `FAILED`)
- [x] Build log capture & persistent storage
- [x] Deployment status API (`GET /api/projects/:id/deployments/:did`)
- [x] List deployments (`GET /api/projects/:id/deployments`)
- [x] **Deployment Safety Invariant**: Failed deployments preserve existing running containers and `current_deployment_id`

---

## Phase 4: Health Checks ✅ [CURRENT AND IMPLEMENTED]
> Verify deployed applications are actually working

- [x] HTTP health check polling runner against container host port
- [x] Health check integration into deployment pipeline
- [x] `HEALTH_CHECKING` -> `RUNNING` transition upon HTTP 2xx/3xx response
- [x] Health check failure -> `FAILED` transition preserving previous deployment
- [x] Runtime health status transitions (`RUNNING` / `CRASHED` / `STOPPED`)

---

## Phase 5: Application Lifecycle Management ✅ [CURRENT AND IMPLEMENTED]
> Stop, start, restart, rollback

- [x] Stop application container (`POST /api/projects/:id/stop`)
- [x] Start application container (`POST /api/projects/:id/start`)
- [x] Restart application container (`POST /api/projects/:id/restart`)
- [x] Rollback to previous deployment (`POST /api/projects/:id/rollback`)
- [x] Rollback creates new deployment record from prior known-good image & configuration
- [x] Rollback adheres strictly to the deployment safety invariant

---

## Phase 6: Environment Variables & Secrets ✅ [CURRENT AND IMPLEMENTED]
> Secure configuration management

- [x] AES-256-GCM encryption with unique 12-byte nonces
- [x] Create/update env var (`POST /api/projects/:id/env`)
- [x] List env vars (`GET /api/projects/:id/env`) — values masked (`••••••••`)
- [x] Delete env var (`DELETE /api/projects/:id/env/:key`)
- [x] In-memory decryption and injection into container at deploy time
- [x] Streaming secret value redaction (`[REDACTED]`) in build & runtime logs

---

## Phase 7: WebSocket & Live Realtime Logs ✅ [CURRENT AND IMPLEMENTED]
> Realtime streaming to browser

- [x] WebSocket endpoint (`GET /api/ws`)
- [x] JWT authentication on WebSocket upgrade (query parameter, Authorization header, or HttpOnly cookie)
- [x] WebSocket Hub & Client manager
- [x] Subscribe (`subscribe`) / Unsubscribe (`unsubscribe`) protocol
- [x] Server-side subscription authorization (project ownership verification)
- [x] Redis Pub/Sub -> WebSocket Hub bridge (`forgelab:pubsub:<channel>`)
- [x] Realtime log streaming for build and runtime outputs
- [x] Realtime deployment status change notifications
- [x] Channel isolation (`project:<uuid>`, `deployment:<uuid>`) preventing cross-user/cross-project leakage

---

## Phase 8: REST API & Frontend ✅ [CURRENT AND IMPLEMENTED]
> Complete control plane UI & REST endpoints

- [x] Get deployment build & runtime logs (`GET /api/projects/:id/deployments/:did/logs`)
- [x] Next.js 14 + TypeScript frontend (`frontend`)
- [x] Login & Registration pages (`/login`, `/register`)
- [x] Dashboard with project list & project creation modal (`/dashboard`)
- [x] Project details page with live status, secrets manager, deployment history, lifecycle controls, and live WebSocket log viewer (`/projects/[id]`)

---

## Phase 9: Automated Testing & Verification ✅ [CURRENT AND IMPLEMENTED]
> High-confidence automated test suite

- [x] Auth & JWT claims validation unit tests (`internal/auth`)
- [x] Path security validator unit tests (`internal/security`)
- [x] AES-GCM encryption unit tests (`internal/crypto`)
- [x] Log redactor unit tests (`internal/logging`)
- [x] State machine transition unit tests (`internal/models`)
- [x] WebSocket channel isolation unit tests (`internal/websocket`)

---

## Future Phases (NOT MVP / DEFERRED)

These remain explicitly deferred post-MVP:

- **GitHub OAuth integration** — import from public/private GitHub repos
- **Automated deployments** — GitHub webhook-triggered builds
- **Caddy integration** — automated reverse-proxy / custom domains / HTTPS
- **OpenTelemetry** — metrics, tracing, observability dashboard
- **Resource limits** — CPU/memory caps per container
- **Teams / RBAC** — organization roles and permissions
- **CLI** — `forge deploy`, `forge logs`, etc.
- **Self-healing** — automatic restart/rollback on container crash
- **Zero-downtime deployments** — blue/green or rolling updates
- **Multi-environment** — staging/production environments per project
