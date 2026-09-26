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
ForgeLAB is a single control-plane, self-hosted application deployment platform. Built with a **Go control plane**, **PostgreSQL 16**, **Redis 7**, **Docker Engine SDK**, and a **Next.js 14 (TypeScript)** operational web UI, it manages the application lifecycle: host source validation, snapshot copying, Docker image builds, container creation on dynamic host ports, HTTP health-check gating, live WebSocket log streaming, AES-256-GCM secret management, and rollback safety.

### Current Architectural State
- **Control Plane Pattern:** Single Go control-plane process (`backend/cmd/server/main.go`). It serves HTTP REST endpoints (via `chi`), WebSocket connections (via `gorilla/websocket`), and hosts an embedded asynchronous background deployment worker consuming jobs from a Redis list (`BRPOP`).
- **Data Persistence:** PostgreSQL 16 stores durable records for `users`, `projects`, `deployments`, `environment_variables` (secrets), `deployment_logs`, and `refresh_tokens`.
- **Source Ingestion & Host Visibility:**
  - Local repository paths (`source_type: "local"`) are validated by `internal/security/PathValidator` and snapshotted into `data/builds/<deployment-id>` before building.
  - **Filesystem Visibility Reality:** `PathValidator` and the snapshot copy run against the filesystem visible to the Go backend process.
    - When the backend runs directly on the host (**Environment A**), arbitrary host repository paths (e.g. `C:\dev\testapp` or `/home/user/app`) are directly accessible.
    - When the backend runs inside the Docker Compose container (**Environment B**), arbitrary host directories are **not** mounted into the container (`docker-compose.yml` only mounts `/var/run/docker.sock` and `forgelab_builds`). Therefore, host paths are not visible inside the containerized backend.
- **Port Allocation:** Dynamic host port allocation in the range **`10000–60000`** managed by `internal/network/PortManager`.
- **Realtime Pipeline:** Deployment events and container logs are pushed to Redis Pub/Sub channels (`forgelab:pubsub:deployment:<uuid>`), bridged to an in-memory WebSocket Hub, and streamed to authenticated client subscriptions.
- **Rollback & Safety Invariant:** When a deployment fails at any stage (snapshot, build, container startup, or HTTP health check), the previous running deployment container remains untouched, and `projects.current_deployment_id` remains unchanged. Rollback creates a new deployment referencing the prior known-good Docker image.
- **Container Restart vs. Platform Self-Healing:**
  - **Docker Runtime Restart Policy:** Containers are created with `RestartPolicy: "unless-stopped"`. The Docker daemon itself automatically restarts crashed containers.
  - **ForgeLAB-Level Self-Healing:** ForgeLAB does **not** implement continuous background runtime health monitoring, crash-loop detection, or automated application rollback after promotion.

### Current Implementation vs. Verification Stage
- **Implementation Stage:** The local-deployment MVP vertical slice is **IMPLEMENTED** in the codebase.
- **Verification Stage:** **NOT RECORDED**. Per project policy, automated unit tests and mocks are non-authoritative artifacts. No capability may be classified as `PHYSICALLY VERIFIED` until the project owner executes the manual procedures defined in [docs/12-manual-verification.md](12-manual-verification.md) in the live development environment.

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
| **User Registration & Login** | IMPLEMENTED | NOT RECORDED | bcrypt password hashing, JWT access + refresh tokens |
| **Atomic Refresh Token Rotation** | IMPLEMENTED | NOT RECORDED | Single-use rotation via PostgreSQL atomic updates |
| **Project CRUD & Ownership** | IMPLEMENTED | NOT RECORDED | Server-side user ownership checks on all endpoints |
| **Local Host Source Validation** | IMPLEMENTED | NOT RECORDED | Works when backend runs on host; containerized backend lacks arbitrary host mounts |
| **Local Source Snapshotting** | IMPLEMENTED | NOT RECORDED | Source copied to isolated working directory before build |
| **Redis Deployment Queue** | IMPLEMENTED | NOT RECORDED | `LPUSH` / `BRPOP` queue with `SETNX` worker concurrency lock |
| **Docker Build Pipeline** | IMPLEMENTED | NOT RECORDED | Docker SDK ImageBuild via tar stream, log parsing |
| **Dynamic Host Port Allocation** | IMPLEMENTED | NOT RECORDED | Scans available ports in range `10000–60000` |
| **Deployment State Machine** | IMPLEMENTED | NOT RECORDED | Enforced: `QUEUED`→`CLONING`→`BUILDING`→`STARTING`→`HEALTH_CHECKING`→`RUNNING`/`FAILED` |
| **Health-Check Deployment Gate** | IMPLEMENTED | NOT RECORDED | HTTP polling gate (10 attempts, 2s interval) before promotion |
| **Deployment Safety Invariant** | IMPLEMENTED | NOT RECORDED | Failed health checks keep previous running deployment alive |
| **Lifecycle Controls (Stop/Start/Restart)** | IMPLEMENTED | NOT RECORDED | Container Stop/Start/Restart via Docker SDK |
| **Rollback Execution** | IMPLEMENTED | NOT RECORDED | Creates new deployment using prior known-good image tag |
| **Docker Container Restart Policy** | IMPLEMENTED | NOT RECORDED | Docker daemon `unless-stopped` restarts crashed containers |
| **ForgeLAB Continuous Self-Healing** | NOT IMPLEMENTED | — | Background crash-loop detection / auto-rollback deferred |
| **Secret Encryption at Rest** | IMPLEMENTED | NOT RECORDED | AES-256-GCM with unique 12-byte nonces |
| **Secret Log Redaction** | IMPLEMENTED | NOT RECORDED | In-memory pattern replacement (`[REDACTED]`) before write |
| **WebSocket Subscription Auth** | IMPLEMENTED | NOT RECORDED | Verifies user ownership of deployment project before subscribe |
| **Deployment-Scoped Log Streaming** | IMPLEMENTED | NOT RECORDED | Streams live build & runtime logs to `deployment:<uuid>` |
| **Frontend Dashboard & Project Views** | IMPLEMENTED | NOT RECORDED | Next.js 14 UI for projects, deploys, logs, and secrets |
| **GitHub OAuth Integration** | DEFERRED | — | Planned future feature; architecture specified in docs/15 |
| **GitHub Repository Selection** | DEFERRED | — | Planned future feature |
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
