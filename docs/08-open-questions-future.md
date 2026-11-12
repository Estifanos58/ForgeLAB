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
- [docs/15-github-integration.md](15-github-integration.md) — Implemented GitHub App, OAuth, recoverable imports & webhooks design

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

### OQ-11: Reverse-Proxy Integration, Custom Domains, & Blue-Green Switching [RESOLVED & IMPLEMENTED]
- **Caddy Reverse-Proxy Integration:** `caddy:2-alpine` is integrated into `docker-compose.yml` (`infra/caddy/Caddyfile`, persistent `forgelab_caddy_data` & `forgelab_caddy_config` volumes) and dynamically configured via `CADDY_ADMIN_URL` (`http://caddy:2019` in Compose).
- **Custom Domain Routing & Truthful HTTPS Status:** Users register custom hostnames (`/api/projects/{id}/domains`), verify DNS TXT ownership (`_forgelab-verify.<domain>`), and sync TLS routes into Caddy. When Caddy Admin API is unconfigured, `DomainService` reports `tls_status: "proxy_unconfigured"` and `https_active: false`.
- **Opt-In Blue-Green Traffic Switching:** Per-service `deployment_mode: "blue_green"` shifts traffic via `TrafficRouter` (`/proxy/services/{serviceId}`) only after the replacement container passes health checks.

### OQ-12: GitHub Push-to-Deploy Webhooks & Recoverable Imports [RESOLVED & IMPLEMENTED]
- **Webhook Receiver Endpoint:** `POST /api/webhooks/github` verifies `X-Hub-Signature-256` with AES-GCM encrypted per-project webhook secrets and deduplicates `X-GitHub-Delivery` IDs (`webhook_deliveries`) against replays before enqueuing branch-matched deployments.
- **Restart-Recoverable Imports:** `RepositoryImportService.RecoverInterruptedImports` resumes interrupted `repository_imports` across backend restarts with cancel and retry APIs.

### OQ-13: Self-Healing, Verified Auto-Rollback, Native Metrics & Audit Trail [RESOLVED & IMPLEMENTED]
- **Configurable Self-Healing & Auto-Rollback:** `CheckContinuousRuntimeHealth` enforces bounded restarts (`FORGELAB_SELF_HEAL_*`), crash-loop detection (`FORGELAB_CRASH_LOOP_*`), and cooldown-guarded automatic rollback (`FORGELAB_AUTO_ROLLBACK_*`), preserving and inspect-verifying prior healthy containers.
- **Runtime Metrics Cache, Historical Telemetry & Diagnostics:** `Engine.GetProjectRuntimeMetricsWithOptions` caches Docker stats (`FORGELAB_METRICS_CACHE_TTL`), a background sampler stores historical CPU/memory/network/restart telemetry (`metrics_history`, `FORGELAB_METRICS_RETENTION_DAYS`), and `/api/diagnostics` diagnoses Docker, deployment, resource, and import health.
- **Redacted Operational Audit Trail & Volume Backups:** `AuditService` records redacted events in `audit_logs`, while `EngineVolumes` provides volume disk usage visibility, streaming `.tar.gz` backups, and size-bounded restores.

---

## Remaining Future Product Stage Roadmap (Deferred Scope)

The following capabilities remain intentionally deferred beyond the single-node stabilization pass:

### Priority 1: Commit Status Reporting & Ephemeral Pull-Request Environments
- **Commit Status Check Reporting:** Post deployment lifecycle status and preview URLs back to GitHub commit status / Deployments API.
- **Ephemeral Pull Request Environments:** Automatically provision and tear down isolated multi-service preview environments for open pull requests.

### Priority 2: OpenTelemetry / Prometheus Exporter & Frontend Log Virtualization
- **Prometheus & OTLP Exporter:** Expose platform-wide Prometheus scrape metrics (`/api/metrics`) and distributed traces alongside the existing native runtime metrics API.
- **Virtualized Terminal Log Viewer:** Add DOM virtualization for large multi-thousand-line runtime log streams.

### Priority 3: Team Governance & Role-Based Access Control (RBAC)
- **Role-Based Access Control (RBAC):** Organization-level workspaces with granular permissions (Admin, Operator, Developer, Viewer).

### Priority 4: Enhanced Local-Agent Packaging & Multi-Node Execution
- **Packaged Desktop Service / Installer:** Native OS service installer wrappers alongside the current `cmd/agent` binary and PowerShell launcher.
- **Distributed Worker Runners:** Decouple the embedded worker loop to run across remote worker nodes when multi-host scheduling is warranted.
