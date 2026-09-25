# ForgeLab — Architecture Decisions

**Status:** Current  
**Last Updated:** 2026-09-25  

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
| source_type | VARCHAR | 'local' or 'github' (future) |
| repository_path | VARCHAR | Local path or GitHub repo URL |
| branch | VARCHAR | Default: 'main' |
| dockerfile_path | VARCHAR | Default: 'Dockerfile' |
| build_context | VARCHAR | Default: '.' |
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
| started_at | TIMESTAMPTZ | When deployment was triggered |
| built_at | TIMESTAMPTZ | When image build completed (nullable) |
| deployed_at | TIMESTAMPTZ | When container started (nullable) |
| finished_at | TIMESTAMPTZ | When deployment reached terminal state |
| duration_ms | BIGINT | Total deployment duration |
| failure_reason | TEXT | Nullable — populated on failure |
| created_at | TIMESTAMPTZ | |

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

---

## Local Repository Import Mechanism

For the MVP, local repository import works as follows:

1. User specifies a **path on the host machine** where the project source lives
2. When a deployment is triggered, ForgeLab **copies** the source to a working directory
3. The working directory is used as the Docker build context
4. This avoids bind-mount complications and ensures the build is reproducible from a snapshot

The copy approach means:
- The build uses a snapshot of the source at deployment time
- Subsequent source changes don't affect running deployments
- Each deployment has an isolated build context

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
