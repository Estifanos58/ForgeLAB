# ForgeLab — Architecture Decisions

**Status:** Current Reference Specification  
**Architecture Model:** Single Control Plane (Go + PostgreSQL + Redis + Docker Engine)  

---

## Related Documents

- [INSTRUCTION.md](../INSTRUCTION.md) — Operational memory & architectural constraints
- [docs/00-current-state.md](00-current-state.md) — Current state snapshot & matrix
- [docs/09-api-contract.md](09-api-contract.md) — Authoritative REST & WebSocket API specification
- [docs/11-development-environment.md](11-development-environment.md) — Development environment setup & ports
- [docs/12-manual-verification.md](12-manual-verification.md) — Physical test procedures
- [docs/13-decisions.md](13-decisions.md) — Architecture Decision Records (ADRs)
- [docs/14-known-limitations.md](14-known-limitations.md) — Known limitations and investigation items

---

## Foundational Architecture Constraint

ForgeLab starts as a **single control-plane application** with PostgreSQL, Redis, and Docker. Not microservices.

Worker separation, multi-node scheduling, and distributed architecture are earned by real engineering requirements (concurrency, workload isolation, scale, failure domains), not by assumption.

## System Overview

```
┌─────────────────────────────────────────────────────────────┐
│                        Browser (Next.js)                     │
│  Dashboard │ Projects │ Deployments │ Logs │ Settings        │
└──────────┬──────────────────────────┬───────────────────────┘
           │ REST API                 │ WebSocket
           ▼                         ▼
┌─────────────────────────────────────────────────────────────┐
│                  ForgeLab Control Plane (Go)                 │
│                                                              │
│  ┌──────────┐  ┌──────────────┐  ┌────────────────────┐     │
│  │ Auth     │  │ Project Mgmt │  │ Deployment Engine  │     │
│  │ Service  │  │ Service      │  │ (Build/Run/Health) │     │
│  └──────────┘  └──────────────┘  └────────────────────┘     │
│                                                              │
│  ┌──────────┐  ┌──────────────┐  ┌────────────────────┐     │
│  │ Log      │  │ WebSocket    │  │ Secret             │     │
│  │ Streamer │  │ Hub          │  │ Manager            │     │
│  └──────────┘  └──────────────┘  └────────────────────┘     │
└──────┬──────────────┬───────────────────┬───────────────────┘
       │              │                   │
       ▼              ▼                   ▼
┌────────────┐  ┌───────────┐    ┌──────────────┐
│ PostgreSQL │  │   Redis   │    │    Docker     │
│            │  │           │    │    Engine     │
│ - Users    │  │ - Deploy  │    │              │
│ - Projects │  │   queue   │    │ - Build      │
│ - Deploys  │  │ - Pub/Sub │    │ - Run        │
│ - Secrets  │  │   events  │    │ - Logs       │
│ - History  │  │           │    │ - Health     │
└────────────┘  └───────────┘    └──────────────┘
```

## Data Model

### Users

| Field | Type | Notes |
|-------|------|-------|
| id | UUID (PK) | Durable unique identifier |
| email | VARCHAR | Unique, login credential |
| password_hash | VARCHAR | bcrypt hashed |
| display_name | VARCHAR | |
| created_at | TIMESTAMPTZ | |
| updated_at | TIMESTAMPTZ | |

### Projects

| Field | Type | Notes |
|-------|------|-------|
| id | UUID (PK) | Durable unique identifier |
| owner_id | UUID (FK→users) | Project ownership |
| name | VARCHAR | Display name (not used as identity) |
| slug | VARCHAR | URL-friendly unique identifier per user |
| source_type | VARCHAR | 'local' or 'github' |
| source_reference | VARCHAR | Repo 'owner/name' or uploaded source UUID |
| repository_path | VARCHAR | Optional legacy host path |
| branch | VARCHAR | Default: 'main' |
| dockerfile_path | VARCHAR | Default: 'Dockerfile' |
| build_context | VARCHAR | Default: '.' |
| build_strategy | VARCHAR | 'auto' or 'dockerfile' |
| build_command | VARCHAR | e.g. 'npm run build' |
| start_command | VARCHAR | e.g. 'npm start' |
| runtime_type | VARCHAR | Detected runtime (e.g. 'nodejs', 'python', 'go') |
| internal_port | INTEGER | Application internal port (default: 8080) |
| health_strategy | VARCHAR | 'auto', 'http', 'tcp', or 'none' |
| health_check_path | VARCHAR | Default: '/health' (nullable) |
| health_check_enabled | BOOLEAN | Default: true |
| status | VARCHAR | 'inactive', 'deploying', 'running', 'stopped', 'failed' |
| current_deployment_id | UUID (FK→deployments, nullable) | Currently active deployment |
| created_at | TIMESTAMPTZ | |
| updated_at | TIMESTAMPTZ | |

### Deployments

| Field | Type | Notes |
|-------|------|-------|
| id | UUID (PK) | Durable unique identifier |
| project_id | UUID (FK→projects) | |
| deploy_number | INTEGER | Sequential per project (1, 2, 3...) |
| status | VARCHAR | See deployment state machine below |
| commit_sha | VARCHAR | Nullable — set when available |
| branch | VARCHAR | Branch at time of deployment |
| image_tag | VARCHAR | Docker image reference |
| container_id | VARCHAR | Docker container ID (nullable until started) |
| build_strategy | VARCHAR | 'auto' or 'dockerfile' |
| runtime_type | VARCHAR | Runtime at deployment time |
| internal_port | INTEGER | Container port bound to host port |
| build_command | VARCHAR | Build command executed |
| start_command | VARCHAR | Container entrypoint/command |
| started_at | TIMESTAMPTZ | When deployment was triggered |
| built_at | TIMESTAMPTZ | When image build completed (nullable) |
| deployed_at | TIMESTAMPTZ | When container started (nullable) |
| finished_at | TIMESTAMPTZ | When deployment reached terminal state |
| duration_ms | BIGINT | Total deployment duration |
| failure_reason | TEXT | Nullable — populated on failure |
| created_at | TIMESTAMPTZ | |

### GitHub Integrations

| Field | Type | Notes |
|-------|------|-------|
| id | UUID (PK) | Unique integration record |
| user_id | UUID (FK→users) | User granting repository authorization |
| encrypted_access_token | BYTEA | AES-256-GCM encrypted token |
| github_user_id | VARCHAR | GitHub account ID |
| github_username | VARCHAR | GitHub handle for display |
| scope | VARCHAR | Granted OAuth scopes (e.g. 'repo,read:user') |
| created_at | TIMESTAMPTZ | |
| updated_at | TIMESTAMPTZ | |

### Environment Variables (Secrets)

| Field | Type | Notes |
|-------|------|-------|
| id | UUID (PK) | |
| project_id | UUID (FK→projects) | |
| key | VARCHAR | e.g., DATABASE_URL |
| encrypted_value | BYTEA | AES-GCM encrypted |
| is_secret | BOOLEAN | If true, value is never exposed in UI/API/logs |
| created_at | TIMESTAMPTZ | |
| updated_at | TIMESTAMPTZ | |

### Deployment Logs

| Field | Type | Notes |
|-------|------|-------|
| id | BIGSERIAL (PK) | |
| deployment_id | UUID (FK→deployments) | |
| timestamp | TIMESTAMPTZ | When the log line was produced |
| phase | VARCHAR | 'source', 'build', 'startup', 'health', 'runtime' |
| stream | VARCHAR | 'stdout', 'stderr', 'system' |
| message | TEXT | Log content (secret-redacted) |

---

## Deployment State Machine

This is a critical design element. Deployments transition through these states:

```
                    QUEUED
                      │
                      ▼
                   CLONING
                      │
              ┌───────┴───────┐
              ▼               ▼
           BUILDING        FAILED
              │
        ┌─────┴─────┐
        ▼            ▼
     STARTING     FAILED
        │
        ▼
   HEALTH_CHECKING
        │
   ┌────┴────┐
   ▼         ▼
RUNNING    FAILED
   │
   ├──► STOPPED (user stop)
   │       │
   │       └──► RUNNING (user start)
   │
   └──► CRASHED (process exit)
           │
           └──► FAILED (after restart threshold)
```

### State Definitions

| State | Description |
|-------|-------------|
| QUEUED | Deployment created, waiting to be processed |
| CLONING | Source code being obtained (copy for local, clone for GitHub) |
| BUILDING | Docker image being built |
| STARTING | Container being created and started |
| HEALTH_CHECKING | Container started, waiting for health check to pass |
| RUNNING | Application is healthy and serving |
| STOPPED | Application manually stopped by user |
| CRASHED | Application process exited unexpectedly |
| FAILED | Terminal failure (build failed, health check failed, unrecoverable crash) |

### State Transition Rules

1. Only forward transitions during deployment: QUEUED → CLONING → BUILDING → STARTING → HEALTH_CHECKING → RUNNING
2. Any deployment phase can transition to FAILED
3. RUNNING can transition to STOPPED (manual) or CRASHED (unexpected exit)
4. STOPPED can transition to RUNNING (manual start)
5. **A FAILED deployment NEVER destroys the previous RUNNING deployment**

### Deployment-Time Health Gating vs. Continuous Monitoring & Restart Behavior

A critical distinction in ForgeLAB's architecture:

- **Deployment-Time Health Gating (CURRENT IMPLEMENTATION — IMPLEMENTED):**
  When a container is started, the deployment worker enters `HEALTH_CHECKING`. It polls the allocated container host port via HTTP (10 attempts, 2-second interval, default endpoint `/health`).
  - If any attempt returns HTTP 2xx or 3xx: the deployment is promoted to `RUNNING`, traffic is active, and the old container is stopped.
  - If 10 attempts fail: the deployment transitions to `FAILED`, the broken container is removed, and the previous running deployment is kept intact.
- **Docker Engine Restart Behavior (CURRENT IMPLEMENTATION — IMPLEMENTED):**
  Deployed containers are configured with `RestartPolicy: { Name: "unless-stopped" }` in [`backend/internal/docker/engine.go`](file:///c:/Users/estif/Desktop/ForgeLAB/backend/internal/docker/engine.go). The underlying Docker daemon automatically restarts stopped or crashed containers at the container-engine layer.
- **ForgeLAB-Level Continuous Health Monitoring & Self-Healing (FUTURE ARCHITECTURE — NOT IMPLEMENTED):**
  ForgeLAB does **not** implement continuous control-plane application health monitoring, crash diagnosis, automatic ForgeLAB-level rollback, health-based remediation, crash-loop analysis, or deployment re-promotion. Docker's container-level restart policy must not be confused with platform-level self-healing.

---

## Deployment Safety Invariant

When a new deployment fails:

```
Deployment 14: RUNNING (current)
Deployment 15: QUEUED → BUILDING → FAILED

Result:
- Deployment 14 remains RUNNING
- Deployment 15 is recorded as FAILED with failure reason
- project.current_deployment_id still points to deployment 14
```

Traffic switching only happens after the new deployment reaches RUNNING:

```
Deployment 14: RUNNING (current)
Deployment 15: QUEUED → BUILDING → STARTING → HEALTH_CHECKING → RUNNING

Result:
- project.current_deployment_id updated to deployment 15
- Deployment 14 container stopped and marked STOPPED
```

---

## Rollback Behavior

Rollback is modeled as creating a new deployment from a previous known-good deployment's configuration:

```
User requests rollback to deployment 12
→ New deployment 16 is created using deployment 12's image
→ Container started from that image
→ Health check
→ If healthy: deployment 16 becomes current, old deployment stopped
→ If unhealthy: deployment 16 marked FAILED, previous remains current
```

This preserves the deployment safety invariant — rollback follows the same lifecycle as any other deployment.

---

## Project Identity vs. Deployment Identity

| Concept | Identity | Notes |
|---------|----------|-------|
| User | UUID | Durable, globally unique |
| Project | UUID | Durable, globally unique. Name is display-only. |
| Deployment | UUID | Durable, globally unique. deploy_number is sequential per project. |
| Container | Docker container ID | Transient, tied to a specific deployment |
| Image | Docker image tag | `forgelab/<project-id>:<deploy-number>` |

Two projects using the same repository are **completely separate entities** with separate UUIDs, deployments, logs, and WebSocket streams.

### Identifier Format Standard: UUID vs. ULID
Historical architecture discussions evaluated ULIDs for lexicographical sorting. However, **UUIDv4 is the active standard** across the entire platform:
- Generated in PostgreSQL via `gen_random_uuid()`
- Modeled in Go using `github.com/google/uuid`
- Utilized in all REST paths (`/api/projects/:id`) and WebSocket channels (`deployment:<uuid>`)
- Enforced in database unique indexes and ownership checks

ULID migration is explicitly deferred and must not be initiated without an approved Architectural Decision Record.

---

## Local Source Ingestion & Deployment Architecture

ForgeLAB supports three distinct source ingestion models:

### 1. Direct Local Directory Deployment (`source_type: "local_directory"`) — Primary Computer Workflow
- **No Browser Source Upload:** Files are never transferred across the network or enumerated as browser File objects.
- **Backend Host Visibility:** The backend accesses the project directly from disk under configured `FORGELAB_ALLOWED_SOURCE_ROOTS`. In Docker Compose, the backend mounts `${FORGELAB_HOST_SOURCE_ROOT:-./}:/host-projects:ro` as a read-only volume, and the application safely translates host paths to container paths.
- **Fast Metadata Inspection:** `POST /api/sources/local/validate` inspects project manifests and directory structure directly, pruning ignored directories without full-tree walking.
- **Direct Filesystem Build:** The deployment engine builds directly from the validated directory without duplicating tens of thousands of files into `data/builds/<deployment-id>`.
- **Streaming Build Context:** Concurrently streams the Docker build context using `io.Pipe()` and `tar.Writer`, eliminating in-memory `bytes.Buffer` allocation.
- **.dockerignore Early Pruning:** Skips `node_modules`, `.git`, `.next`, cache dirs, etc. before descending into them.
- **Virtual Dockerfiles:** Auto-detected build strategies stream generated Dockerfiles directly into the in-memory TAR stream, leaving the host filesystem pristine and supporting read-only source mounts.

### 2. Local Source Archive Upload (`source_type: "local_upload"`) — Fallback Computer Workflow
- Used when direct filesystem access is unavailable.
- Browser uploads a `.zip`, `.tar.gz`, or `.tgz` archive via `POST /api/sources/upload`.
- Extracted into an isolated, random UUID source workspace (`data/sources/<source-id>`).
- Built from the isolated source workspace.

### 3. GitHub Repository Import (`source_type: "github"`)
- Users authorize GitHub via OAuth.
- Source tarball is fetched into an isolated source workspace.

---

## How Deployment Work Is Queued

1. User triggers deployment via REST API
2. A deployment record is created in PostgreSQL with status QUEUED
3. A deployment job is published to Redis (deployment ID as payload)
4. The deployment worker (goroutine in the same process for MVP) picks up the job
5. Worker executes the deployment pipeline: clone → build → start → health check
6. At each phase, status is updated in PostgreSQL and events published to Redis pub/sub
7. WebSocket hub subscribes to Redis pub/sub and forwards scoped events to connected clients

---

## Log Pipeline

```
Docker container stdout/stderr
       │
       ▼
ForgeLab Docker SDK (log stream)
       │
       ▼
Log Processor (secret redaction)
       │
       ├──► PostgreSQL (deployment_logs table — persistence)
       │
       └──► Redis pub/sub (channel: deploy:<deployment-id>:logs)
               │
               ▼
         WebSocket Hub
               │
               ▼
         Browser (scoped to project/deployment)
```

---

## Extensibility Points

The architecture is designed so these future capabilities can be added without rewriting the foundation:

| Future Feature | Extension Point |
|----------------|-----------------|
| Worker separation | Redis queue already exists; workers can become separate processes |
| Multi-node | Deploy queue is Redis-based, not in-process |
| GitHub import | source_type field on projects; new source handler |
| Webhooks | New endpoint + deployment trigger |
| RBAC | user_id already on projects; add roles/permissions table |
| Environments | Add environment_id to deployments and env vars |
| Resource limits | Docker container config already has CPU/memory options |
| Custom domains | Caddy API integration layer |
| Audit logging | Insert audit records at service boundaries |
