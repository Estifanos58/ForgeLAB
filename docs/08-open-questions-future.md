# ForgeLab — Open Questions & Future Roadmap

**Status:** Current Reference Specification  
**Scope:** Resolved MVP Decisions & Deferred Platform Capabilities  

---

## Related Documents

- [INSTRUCTION.md](../INSTRUCTION.md) — Operational memory & architectural constraints
- [docs/00-current-state.md](00-current-state.md) — Current state snapshot & matrix
- [docs/04-architecture-decisions.md](04-architecture-decisions.md) — Core architectural decisions
- [docs/11-development-environment.md](11-development-environment.md) — Development environment setup & ports
- [docs/13-decisions.md](13-decisions.md) — Architecture Decision Records (ADRs)
- [docs/14-known-limitations.md](14-known-limitations.md) — Known limitations and investigation items
- [docs/15-github-integration.md](15-github-integration.md) — Future GitHub integration design

---

## Resolved MVP Architectural Decisions

These questions were evaluated and implemented in the MVP control-plane architecture:

### OQ-1: Local Repository Path — Validation & Security [RESOLVED & IMPLEMENTED]

**Resolution:** Standardized on `internal/security/PathValidator`.
- Validates path existence on host and ensures it is a directory.
- Canonicalizes path via `filepath.EvalSymlinks` to defend against symlink bypasses.
- Rejects dangerous system directories (`/etc`, `/var`, `/usr`, `/sys`, `/proc`, `C:\Windows`, `C:\Program Files`, etc.).
- Enforces boundary checking against `FORGELAB_ALLOWED_SOURCE_ROOTS`.
- Copies repository snapshot into isolated build working directory before build starts.

### OQ-2: Docker Network & Port Allocation [RESOLVED & IMPLEMENTED]

**Resolution:** Standardized on `internal/network/PortManager`.
- Assigns managed host port mappings in the range **10000–60000** (`network.NewPortManager(10000, 60000)`).
- Dynamically scans active ports to avoid collisions.
- Stores assigned port in `projects.port` database record for UI links & container mapping.

### OQ-3: Refresh Token Security & Rotation [RESOLVED & IMPLEMENTED]

**Resolution:** Atomic single-use rotation implemented via PostgreSQL `UPDATE ... RETURNING`.
- Eliminates race conditions in concurrent token refresh attempts.
- Revokes refresh tokens upon usage and generates a new pair atomically.

### OQ-4: Active Deployment Concurrency [RESOLVED & IMPLEMENTED]

**Resolution:** Enforced at both database and application levels.
- Partial unique index `uq_active_deployment_per_project` on active states (`QUEUED`, `CLONING`, `BUILDING`, `STARTING`, `HEALTH_CHECKING`).
- Application-level `SELECT ... FOR UPDATE` locks during deployment initialization.

### OQ-5: Container & Log Redaction [RESOLVED & IMPLEMENTED]

**Resolution:** Streaming `LogRedactor` intercepts container build & runtime logs.
- Redacts plaintext secret values loaded from AES-256-GCM storage before writing to PostgreSQL or streaming over WebSocket.

### OQ-6: GitHub Integration & OAuth [RESOLVED & IMPLEMENTED]

**Resolution:** Multi-tier GitHub access implemented via GitHub App and OAuth flows.
- GitHub App supports short-lived installation tokens with read-only repository permissions.
- Direct OAuth and fine-grained Personal Access Tokens (PAT) supported with AES-256-GCM token encryption.
- Public repository inspection and deployment without user account linking (`source_type: "github_public"`).

### OQ-7: Container Resource Quotas & Trust Boundaries [RESOLVED & IMPLEMENTED]

**Resolution:** Enforced in `ValidateAndBuildSecureHostConfig`.
- Limits CPU (`NanoCPUs`), Memory (`Memory`), and PIDs (`PidsLimit`) in `container.HostConfig.Resources`.
- Enforces `CapDrop: ["ALL"]`, `SecurityOpt: ["no-new-privileges:true"]`, `Privileged: false`.
- Rejects Docker socket mounts (`/var/run/docker.sock`) and unsupported ephemeral storage quotas.

### OQ-8: Continuous Runtime Health Monitoring [RESOLVED & IMPLEMENTED]

**Resolution:** Reconciler periodic background inspection (`CheckContinuousRuntimeHealth`).
- Detects stopped containers and kernel OOM kills (`insp.State.OOMKilled`), marking service records as `FAILED` with diagnostics.
- Preserves intentional user stops (status `stopped`, exit code 0/137/143).

### OQ-9: Realtime WebSocket Hardening & Origin Policies [RESOLVED & IMPLEMENTED]

**Resolution:** Single-use ticket authentication and strict origin validation.
- Token query parameters (`?token=<jwt>`) strictly rejected with HTTP 401.
- Short-lived single-use tickets (`POST /api/ws/ticket`) with 60-second TTL consumed via `?ticket=<uuid>`.
- Supports HttpOnly cookies and `Authorization: Bearer <token>` headers.
- Handshakes validate `Origin` header against configured `CORS_ALLOWED_ORIGINS` and `FRONTEND_URL`.

### OQ-10: Host Port Allocation Conflict Retries & Remote Access [RESOLVED & IMPLEMENTED]

**Resolution:** Port manager conflict retry and public host resolution.
- Dynamic retry loop (up to 3 retries) detects Docker daemon port collision errors (`isPortConflictError`), marks conflicted ports, and binds new ports.
- Synchronous container reconciliation tracks ports across `running`, `restarting`, `created`, and `paused` states.
- Configurable `FORGELAB_PUBLIC_HOST` / `PUBLIC_HOST` prevents remote browser URLs from defaulting to `localhost`.

---

## Next Product Stage Roadmap (Post-MVP Scope)

Following completion of the core reliability and security hardening, subsequent platform capabilities are prioritized in order:

### Priority 1: Reverse-Proxy Integration, Stable URLs & HTTPS (Next Stage Focus)
- **Caddy Reverse-Proxy Integration:** Embed or orchestrate a dedicated Caddy reverse-proxy service to manage dynamic domain routing and automatic Let's Encrypt / ZeroSSL TLS certificate issuance.
- **Stable Application URLs & Virtual Hosts:** Replace raw dynamic host port access (`http://host:10423`) with predictable, stable hostnames (e.g. `https://<project-slug>.example.com` or `https://<service-name>.<project-slug>.example.com`).
- **Custom Domain Routing:** Enable users to map custom top-level domains (e.g. `api.mycompany.com`) directly to target public services with automated TLS provisioning and DNS validation.
- **Zero-Downtime Traffic Switching:** Shift incoming HTTP traffic via the reverse proxy to new service containers only after health checks pass, eliminating connection gaps during redeployments.

### Priority 2: GitHub Push-to-Deploy & Webhook Receivers
- **Webhook Receiver Endpoint:** Secure `POST /api/webhooks/github` verifying GitHub HMAC-SHA256 payload signatures (`X-Hub-Signature-256`).
- **Branch-Filtered Auto-Deployments:** Automatically trigger release or service deployments when commits are pushed to configured production/tracking branches.
- **Commit Status Check Reporting:** Post deployment lifecycle status and preview URLs directly back to GitHub commit status checks / deployment API.

### Priority 3: Preview Environments & Ephemeral Workspaces
- **Ephemeral Pull Request Environments:** Automatically provision isolated multi-service environments for active pull requests.
- **Auto-Teardown Lifecycle:** Synchronously decommission preview containers, networks, and ephemeral volumes when pull requests are closed or merged.

### Priority 4: Runtime Observability & Container Metrics
- **Prometheus & OpenTelemetry Exporters:** Expose continuous container CPU, memory, network I/O, and disk usage metrics via `/api/metrics` or OpenTelemetry OTLP collectors.
- **Resource Usage Dashboards:** Render real-time and historical resource consumption graphs within the Next.js service dashboard.

### Priority 5: Audit Logging & Access Governance
- **Persistent Structured Audit Trail:** Record security-relevant operational events (project creation/deletion, secret modifications, deployment triggers, rollbacks, OAuth connections) with actor identity, source IP, timestamp, and metadata.
- **Role-Based Access Control (RBAC):** Organization-level user workspaces with granular permissions (Admin, Operator, Developer, Viewer).

### Priority 6: Enhanced Local-Agent & Multi-Node Execution
- **One-Command Agent Installer:** Simplified CLI setup and background daemon service management across Linux, macOS, and Windows.
- **Distributed Worker Runners:** Decouple the deployment worker into standalone daemon agents pulling jobs from the Redis queue across multiple host nodes.
