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

---

## Future Roadmap (Post-MVP Scope)

These items are explicitly deferred to post-MVP development:

### Near-Term
1. **GitHub OAuth Integration**: Direct OAuth authentication with GitHub for repo selection and cloning.
2. **Caddy Reverse Proxy Integration**: Automated domain management, subdomain routing, and Let's Encrypt TLS certificate generation.
3. **Container Resource Quotas**: Configurable CPU cores and memory limits enforced via Docker HostConfig.
4. **Enhanced Health Monitoring**: Configurable interval, backoff, and proactive container crash detection loops.

### Mid-Term
5. **Teams & RBAC**: User organizations, project sharing, and role-based permissions (Admin, Developer, Viewer).
6. **Audit Trail**: Persistent audit log of configuration edits, deployment triggers, and secret updates.
7. **OpenTelemetry Integration**: Direct export of deployment traces and container metrics.
8. **ForgeLab CLI**: Command-line tool for project management and deployment triggering (`forge deploy`, `forge logs`).

### Long-Term
9. **Automatic Webhook Deployments**: Automatic triggers on `git push` to monitored branches.
10. **Zero-Downtime Rolling Deployments**: Blue/green traffic switching via proxy prior to container tear-down.
11. **Multi-Node Worker Support**: External worker nodes pulling deployment jobs from Redis queue.
