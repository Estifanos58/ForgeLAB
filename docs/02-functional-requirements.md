# ForgeLab — Functional Requirements (MVP)

**Status:** Current  
**Last Updated:** 2026-09-25  
**Scope:** First vertical slice — the smallest complete end-to-end deployment lifecycle

---

## MVP Goal

> Take a project containing a Dockerfile and demonstrate the complete deployment lifecycle from authentication through health-checked running application with deployment history.

## Requirement Categories

### R1. Authentication

| ID | Requirement | Status |
|----|------------|--------|
| R1.1 | ForgeLab users must be authenticated | MVP |
| R1.2 | ForgeLab user identity is completely separate from deployed application user identity | MVP |
| R1.3 | Authentication must use secure token-based approach (JWT) | MVP |
| R1.4 | Authorization boundaries must be explicit — a user can only access their own projects/deployments | MVP |

### R2. Project Management

| ID | Requirement | Status |
|----|------------|--------|
| R2.1 | Create/import a project | MVP |
| R2.2 | Configure project name | MVP |
| R2.3 | Configure repository/source | MVP |
| R2.4 | Configure branch | MVP |
| R2.5 | Configure build settings (Dockerfile path at minimum) | MVP |
| R2.6 | Project registration must NOT imply a running application | MVP |
| R2.7 | Projects have durable unique identifiers (UUIDs), not just names | MVP |
| R2.8 | Projects have an owner (user_id) | MVP |

### R3. Project Import — Local Repository

| ID | Requirement | Status |
|----|------------|--------|
| R3.1 | User must be able to import from a local repository path | MVP |
| R3.2 | The mechanism for making local files available to ForgeLab must be explicitly defined | MVP |
| R3.3 | Local import = ForgeLab reads from a path on the host machine accessible via bind mount or copy | MVP |

### R4. Project Import — GitHub

| ID | Requirement | Status |
|----|------------|--------|
| R4.1 | GitHub import must be explicit, permission-based, user-initiated | Future |
| R4.2 | GitHub OAuth flow: Connect → Auth → Grant access → Show repos → Select → Import | Future |
| R4.3 | Private repositories must be supported | Future |
| R4.4 | Branch selection during import | Future |
| R4.5 | GitHub tokens encrypted at rest | Future |
| R4.6 | Foundation for future webhooks | Future |

> **Note:** GitHub integration is documented here for architectural awareness but is NOT an MVP requirement. The MVP uses local repository import.

### R5. Deployment Lifecycle

| ID | Requirement | Status |
|----|------------|--------|
| R5.1 | Trigger deployment via API | MVP |
| R5.2 | Queue deployment work | MVP |
| R5.3 | Obtain project source (clone/copy) | MVP |
| R5.4 | Build Docker image from Dockerfile | MVP |
| R5.5 | Start application container | MVP |
| R5.6 | Track deployment state through explicit transitions | MVP |
| R5.7 | Expose deployment status through API | MVP |
| R5.8 | Deployment creates a durable deployment record | MVP |
| R5.9 | Failed deployment must NOT destroy last working deployment | MVP |
| R5.10 | Allow stop/start/restart of running application | MVP |
| R5.11 | Provide a basic rollback path | MVP |

### R6. Deployment History

| ID | Requirement | Status |
|----|------------|--------|
| R6.1 | Every deployment becomes a durable record | MVP |
| R6.2 | Record: deployment ID, commit SHA, branch, status, timestamps, duration, image reference, failure reason | MVP |
| R6.3 | Build/deployment logs preserved with the record | MVP |
| R6.4 | Deployment history is operational history, not disposable UI state | MVP |

### R7. Health Checks

| ID | Requirement | Status |
|----|------------|--------|
| R7.1 | Support application health checking (e.g., GET /health) | MVP |
| R7.2 | Determine states: RUNNING, UNHEALTHY, STOPPED, CRASHED, DEPLOYING | MVP |
| R7.3 | Health check path must be configurable (not hardcoded) | MVP |
| R7.4 | Configurable interval, timeout, failure threshold | Future |

### R8. Live Logs & Realtime

| ID | Requirement | Status |
|----|------------|--------|
| R8.1 | Deployment logs stream live: clone → build → start → health → success/failure | MVP |
| R8.2 | Runtime/container logs streamable | MVP |
| R8.3 | Log pipeline: container → ForgeLab → WebSocket → browser | MVP |
| R8.4 | Realtime, not polling ("browser refreshes every few seconds" is NOT acceptable) | MVP |
| R8.5 | Logs must NOT leak secrets/env vars | MVP |

### R9. WebSocket Isolation (CRITICAL)

| ID | Requirement | Status |
|----|------------|--------|
| R9.1 | WebSocket traffic scoped to correct project/deployment | MVP |
| R9.2 | Events from deployment A must never appear in deployment B's stream | MVP |
| R9.3 | Events from project A must never leak to project B | MVP |
| R9.4 | Realtime streams use durable unique identifiers, not names/URLs/branches | MVP |
| R9.5 | Server must authorize subscriptions (user cannot subscribe to another user's streams) | MVP |
| R9.6 | WebSocket event contract must be documented before implementation | MVP |

### R10. Security

| ID | Requirement | Status |
|----|------------|--------|
| R10.1 | ForgeLab users authenticated | MVP |
| R10.2 | Authorization: users can only view/deploy/manage their own projects | MVP |
| R10.3 | Environment variables/secrets must not be stored as plaintext | MVP |
| R10.4 | Secrets redacted from logs | MVP |
| R10.5 | GitHub tokens encrypted at rest | Future |
| R10.6 | Container resource limits | Future |
| R10.7 | Threat model documented before executing arbitrary user repos | MVP |

### R11. API-First

| ID | Requirement | Status |
|----|------------|--------|
| R11.1 | Major operations available through well-defined REST API | MVP |
| R11.2 | API is a first-class product surface, not just a UI backend | MVP |
| R11.3 | Operations: create project, inspect, list deployments, trigger deploy, inspect deployment, get logs, rollback, restart, stop/start, delete | MVP |

---

## Out of Scope for MVP (Explicitly)

- GitHub OAuth integration
- Automatic deployments from webhooks
- Custom domains / DNS
- HTTPS automation
- Teams / RBAC / multi-user organizations
- CLI tool
- Self-healing / automatic recovery
- Zero-downtime deployments
- Multi-environment (dev/staging/prod)
- Multi-node / worker separation
- Audit logging (beyond basic)
- Observability dashboards / OpenTelemetry metrics
- PostgreSQL replication
- Resource limits (CPU/memory caps)
