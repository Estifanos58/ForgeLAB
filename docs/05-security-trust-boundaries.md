# ForgeLAB — Security Architecture & Trust Boundaries

**Status:** Current Reference Specification  
**Control Plane Trust Level:** Highly Trusted Component  
**User Repository Trust Level:** Untrusted Input  

---

## Related Documents

- [INSTRUCTION.md](../INSTRUCTION.md) — Operational memory & architectural constraints
- [docs/00-current-state.md](00-current-state.md) — Current state snapshot & matrix
- [docs/09-api-contract.md](09-api-contract.md) — Authentication headers & secret masking
- [docs/11-development-environment.md](11-development-environment.md) — Docker socket mount & path configuration
- [docs/14-known-limitations.md](14-known-limitations.md) — Security hardening backlog

---

## 1. Primary Trust Boundary & Threat Model

ForgeLAB’s security model rests on a fundamental distinction between the **ForgeLAB control plane** and **user application repositories**:

```text
┌────────────────────────────────────────────────────────────────────────┐
│                        UNTRUSTED DOMAIN                                │
│                                                                        │
│   ┌──────────────────────────────┐    ┌────────────────────────────┐  │
│   │   Browser / Client Input     │    │     User Repositories      │  │
│   │   (Untrusted web requests)   │    │  (Arbitrary untrusted code │  │
│   │                              │    │   and untrusted Dockerfile)│  │
│   └──────────────┬───────────────┘    └──────────────┬─────────────┘  │
│                  │                                   │                 │
└──────────────────┼───────────────────────────────────┼─────────────────┘
                   │                                   │
═══════════════════╪═══════════════════════════════════╪═══════════════════
                   │ PRIMARY SECURITY TRUST BOUNDARY   │
                   ▼                                   ▼
┌────────────────────────────────────────────────────────────────────────┐
│                    HIGHLY TRUSTED CONTROL PLANE                        │
│                                                                        │
│   ┌────────────────────────────────────────────────────────────────┐  │
│   │                  ForgeLAB Go Backend API & Worker              │  │
│   │               • Root-equivalent Docker Socket Access           │  │
│   │               • Database & Secret Master Encryption Keys       │  │
│   └──────────────┬───────────────────┬───────────────────┬─────────┘  │
│                  │                   │                   │            │
│                  ▼                   ▼                   ▼            │
│         ┌────────────────┐  ┌────────────────┐  ┌─────────────────┐   │
│         │   PostgreSQL   │  │     Redis      │  │  Docker Engine  │   │
│         │ (Encrypted DB) │  │  (Job Queue)   │  │ (/var/run/docker│   │
│         │                │  │                │  │     .sock)      │   │
│         └────────────────┘  └────────────────┘  └────────┬────────┘   │
│                                                          │            │
└──────────────────────────────────────────────────────────┼────────────┘
                                                           │
                                                           ▼
                                                ┌───────────────────┐
                                                │ Sandboxed Runtime │
                                                │    Containers     │
                                                │(Isolated non-root)│
                                                └───────────────────┘
```

### Critical Trust Realities & Docker Socket Exposure Risk:

```text
ForgeLAB backend = highly trusted control-plane component.

Backend has Docker Engine control via /var/run/docker.sock.

User repositories = untrusted input.

Repository source code must NEVER be treated as trusted merely because
the user imported it.

A compromise of the ForgeLAB backend becomes a host-level Docker
control/security event.
```

#### Inherent Docker Socket Security Risk (Architectural Reality):
The ForgeLAB control plane interacts directly with the Docker daemon via the Unix socket (`/var/run/docker.sock`). Because Docker Engine Unix sockets do not provide fine-grained, per-tenant Access Control Lists (ACLs), any entity capable of executing arbitrary commands against the Docker socket possesses **root-equivalent control over the host system**.

**Why this risk cannot be eliminated without architectural isolation:**
As long as the control plane shares the host Docker socket, the boundary between the control plane and the host OS relies entirely on software-level input validation, path normalization, and API enforcement within ForgeLAB itself. True isolation requires architectural separation:
1. Running user workloads in isolated **Docker-in-Docker (DinD)** or **Rootless Docker** daemons.
2. Moving container execution to isolated microVM hypervisor sandboxes (e.g. AWS Firecracker, Kata Containers, or gVisor runsc).
3. Utilizing remote, physically isolated worker nodes rather than sharing the control-plane host daemon.

**Active Operational Guardrails (Defense-in-Depth):**
To mitigate this risk under the single-node architecture, ForgeLAB enforces strict multi-layered defenses:
- **Zero Socket Exposure to Workloads:** Host Docker socket mounts (`/var/run/docker.sock` and `\\.\pipe\docker_engine`) are unconditionally rejected during preflight validation and sanitized from user Compose files. User containers are strictly prohibited from mounting or accessing the daemon socket.
- **Strict Container Isolation & Capability Allowlist:** All user containers are created with `CapDrop: ["ALL"]`, `SecurityOpt: ["no-new-privileges:true"]`, `Privileged: false`, and a strict security allowlist (`allowedContainerCapabilities`: `CHOWN`, `SETGID`, `SETUID`, normalizing `CAP_` prefixes) rejecting disallowed capabilities (including `ALL`, `SYS_ADMIN`, `NET_ADMIN`, `DAC_OVERRIDE`, `FOWNER`) fail-closed with `ErrCapabilityNotAllowed`. Workloads also preserve declared non-root `user` execution profiles, run on isolated bridge networks (rejecting `host` and `container:` network namespace sharing), enforce per-service CPU/RAM/PID resource limits, enforce owner-level cumulative resource quotas (`ValidateOwnerResourceQuota`) and a 1GB cumulative source upload quota, and truthfully reject unsupported `ephemeral_storage_mb` disk quotas.
- **Control-Plane Network Isolation:** The privileged ForgeLAB backend container is never attached to tenant application Docker networks. Preview routing resolves strictly via published container `HostPort` bindings on loopback or `host.docker.internal`, with explicit upstream blocking of management ports (:8080, :4142, :3000, :5432, :6379, the Docker socket, `forgelab-backend`, and `forgelab-frontend`).
- **Source Root Sandboxing & TAR Symlink Defense:** All host directory mounts are validated with Windows/Unix canonical symlink and NTFS junction/8.3 short-path resolution (`CanonicalizeExistingPath` / `CanonicalizeWithAncestor`) and constrained strictly beneath administrator-configured `FORGELAB_ALLOWED_SOURCE_ROOTS`, rejecting system directories (`/etc`, `/proc`, `/sys`, `/dev`, `C:\Windows`, etc.) and UNC paths. TAR build-context streaming (`internal/tararchive`) additionally enforces `ErrSymlinkEscape` against symbolic links escaping the build context root.
- **Build Reproducibility:** Pinning immutable source revisions (commit SHAs or content fingerprints `fp_...`) ensures that builds fail closed if host files change after plan review.
- **Tightened Frontend CSP & HTTP Security Headers:** `frontend/next.config.ts` enforces Content Security Policy (`default-src 'self'`, `script-src 'self' 'unsafe-inline'` omitting `'unsafe-eval'` outside development builds, explicit `connect-src` allowlist covering `'self'`, `ws:`, `wss:`, Local Agent loopback `127.0.0.1:4142`/`localhost:4142`, and configured env origins without wildcard `http:`/`https:`, `frame-ancestors 'none'`, `object-src 'none'`, `base-uri 'self'`, `form-action 'self'`) paired with `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy`, `Permissions-Policy`, and conditional HSTS (`COOKIE_SECURE=true` or `ENABLE_HSTS=true`).
- **Operational Audit Trail & Metadata Redaction:** `AuditService` records security-relevant lifecycle events in `audit_logs` and recursively redacts sensitive metadata keys (`password`, `secret`, `token`, `api_key`, `private_key`, `encryption_key`, etc.) to `"[REDACTED]"` before database storage.

---

## 2. Authentication Architecture & Token Security

ForgeLAB implements backend-owned, cookie-based session management and hardened realtime streaming:

```text
┌─────────────────────────────────────────────────────────────┐
│                 Session & Identity Security                 │
│                                                             │
│   Transport & Storage:                                      │
│   • Issued as HttpOnly, Secure, SameSite cookies:           │
│     - forgelab_access_token (48-hour default expiry, > 1 day)│
│     - forgelab_refresh_token (14-day default expiry, > 1 week)│
│   • Zero client-side persistence in localStorage            │
│   • API client requests include 'credentials: include'      │
│                                                             │
│   WebSocket Realtime Security:                              │
│   • URL query parameter tokens (?token=<jwt>) strictly      │
│     REJECTED (HTTP 401) to prevent log & referrer leakage   │
│   • Single-use short-lived tickets (POST /api/ws/ticket,    │
│     60s TTL) or HttpOnly cookies / Authorization headers    │
│   • Strict Origin validation enforced on all WS handshakes  │
│                                                             │
│   Multi-Provider Identity:                                  │
│   • Email/password (bcrypt hashed)                          │
│   • Google OAuth 2.0 (OpenID profile, email verified)       │
│   • GitHub OAuth 2.0 (read:user, user:email scopes)         │
│                                                             │
│   GitHub Repository Token Protection:                       │
│   • Repository access tokens encrypted at rest              │
│     using AES-256-GCM in github_integrations table          │
│   • Tokens are never sent to browser or frontend storage    │
└─────────────────────────────────────────────────────────────┘
```

### WebSocket Token Hardening & Ticket Protocol
Passing bearer tokens in URL query strings (`/api/ws?token=<jwt>`) creates security exposure: URLs are frequently recorded in reverse-proxy access logs, server telemetry, browser history, and HTTP `Referer` headers. ForgeLAB eliminates this vulnerability:
1. **Query Token Rejection:** Connecting with `?token=...` is immediately rejected with HTTP `401 Unauthorized`.
2. **Short-Lived Single-Use Tickets:** Clients obtain a ticket via `POST /api/ws/ticket` using their existing HttpOnly cookie or `Authorization: Bearer <jwt>` header. The server generates a random UUID ticket with a 60-second time-to-live stored in Redis (or in-memory store). The ticket is consumed via `GET /api/ws?ticket=<uuid>`. Upon redemption or expiration, the ticket is destroyed immediately, preventing replay attacks.
3. **Origin Checking:** WebSocket handshake enforces strict origin verification against configured `CORS_ALLOWED_ORIGINS` and `FRONTEND_URL`, rejecting cross-site WebSocket hijacking (CSWSH) attempts.
4. **Diagnostics Access Control:** Detailed queue metrics and container log-collector diagnostics on `/ready`, `/health/queue`, and `/health/logs` are restricted strictly to trusted internal loopback/private network callers or authenticated users, preventing unauthenticated external information disclosure.

---

## 3. Source Ingestion Security & Path Boundaries

ForgeLAB treats all source code and filesystem paths as untrusted input across all three ingestion modes:

### 1. Direct Local-Directory Ingestion (`source_type: "local_directory"`)
- **`PathValidator` & Windows Canonicalization Security Boundary:** Validates directory existence, verifies that the target is a directory (not a regular file or device), and resolves symbolic links, Windows NTFS junctions/reparse points, and Windows 8.3 short paths (e.g., `RUNNER~1`) via `CanonicalizeExistingPath` and `CanonicalizeWithAncestor`. `NormalizeReadlinkTarget` strips `\??\` and `\??\UNC\` object-manager prefixes before root-containment validation (`IsWithinRoot`), and UNC network paths (`\\server\share`) are rejected with `ErrRestrictedSystemPath`.
- **Configured Allowed Roots (`FORGELAB_ALLOWED_SOURCE_ROOTS`):** Only paths situated beneath explicitly configured root directories are permitted. All attempts to navigate outside (e.g., traversal via `..` or symlink/junction escape) are rejected with `ErrPathNotAllowed`.
- **System Directory Rejection:** System directories (`/etc`, `/var`, `/usr`, `/proc`, `/sys`, `/dev`, `C:\Windows`, `C:\Program Files`, etc.) and UNC paths are unconditionally rejected with `ErrRestrictedSystemPath`.
- **Docker Compose Host-to-Container Translation:** In containerized setups, the backend mounts `${FORGELAB_HOST_SOURCE_ROOT:-./}:/host-projects:ro` as a read-only volume. The backend safely translates host paths beneath `FORGELAB_HOST_SOURCE_ROOT` to `/host-projects/...` preventing arbitrary host container path spoofing.
- **Cross-User Project Path Authorization:** Project creation and deployments verify that the authenticated user owns the project record. A user cannot hijack or redeploy another user's project path.
- **Direct Build Security & TAR Symlink Defense:** Build context is streamed concurrently into Docker with early `.dockerignore` pruning. In-memory `bytes.Buffer` buffering is eliminated, virtual files (`Dockerfile.forgelab`) are injected into the TAR stream without mutating the source directory, and `internal/tararchive` validates symlink/reparse targets via `CanonicalizeWithAncestor` and `IsWithinRoot`, aborting escapes with `ErrSymlinkEscape`.

### 2. Browser-Based Archive Uploads (`source_type: "local_upload"`)
- **Zip Slip & Path Traversal Prevention:** Archive extraction via sequential `r.MultipartReader()` evaluates every relative path with `filepath.Clean`. Any entry attempting to traverse outside the allocated directory (e.g. `../`, absolute paths, or symlink escapes) is immediately rejected with `ErrPathTraversalDetected` and the directory is purged.
- **Decompression Bomb Protection:** Imposes a strict `MaxUncompressedBytes` limit (100MB). Exceeding this limit halts decompression and wipes the target folder.
- **Workspace Isolation:** Each upload is stored in a dedicated, random UUID workspace (`data/sources/<source_id>`) isolated from other projects and host system directories.

### 3. GitHub Repository Tarball Ingestion (`source_type: "github"`)
- **Server-Side Token Verification:** Only repositories accessible via the authenticated user's encrypted OAuth token are cloned/fetched.
- **Encrypted Token Handling:** Tokens are decrypted in-memory only when communicating with `api.github.com` and are never logged or exposed to the client.
- **Tarball Extraction Isolation:** Remote tarballs are unpacked using the same path-traversal safeguards and size limits into isolated source directories.

### 4. Local Agent Connected Source (`source_type: "local_agent"`)
- **Unauthenticated Identity Verification Probe:** The control plane never sends bearer credentials or session tokens to unverified fallback endpoints. When communicating with a local agent candidate (whether running natively on the host at `127.0.0.1:4142` or via Docker bridge at `host.docker.internal:4142`), the control plane first issues an unauthenticated identity verification probe (`POST /api/agent/verify-identity` or `GET /api/agent/status` with `X-Expected-Agent-ID`). Only endpoints that affirmatively match the expected agent identity are sent bearer session credentials.
- **Interactive Desktop Picker Enforcement & Direct Path Selection:** Native OS folder selection (`POST /api/agent/select-folder`) verifies an interactive desktop session on Windows via `InspectInteractiveSession` (returning HTTP `503` with `reason: "non_interactive_session"` in headless/Session 0 contexts), while `POST /api/agent/select-path` applies the identical `PathValidator` boundary before registering a session.
- **Short-Lived & Revocable Credentials:** Agent session tokens default to a 15-minute TTL (configurable via `FORGELAB_AGENT_SESSION_TTL`, reduced from legacy 30 minutes). Tokens can be revoked explicitly via `POST /api/sources/agent/session/revoke`, and all active agent sessions bound to a source workspace are automatically revoked when the source workspace is deleted (`DELETE /api/sources/{id}`).
- **Scrubbing & Encryption:** Unauthenticated agent status checks scrub `agent_id` and active session counts. Session serialization (`renderSessionResponse`) and `/api/agent/sources/*` responses scrub `CanonicalPath` so host filesystem paths are never leaked to the browser. Raw session tokens are encrypted at rest with AES-256-GCM and never exposed over WebSocket events, logs, or unredacted REST responses.
- **Supported Environments:** Preserves both host-run agent workflows and containerized Docker development environments safely.

---

## 4. Secret Management & Log Redaction

### Encryption at Rest (AES-256-GCM)
- Environment variables configured with `is_secret: true` are encrypted using **AES-256-GCM** before database insertion.
- Master key is supplied via `FORGELAB_ENCRYPTION_KEY` (32 bytes, Base64-encoded).
- Every secret encryption operation generates a unique cryptographically secure 12-byte nonce (IV). Ciphertext and nonce are stored in `environment_variables.encrypted_value`.

### Log Redactor Pipeline
Before any build log line or container runtime log line is stored in PostgreSQL or broadcast over WebSocket:
1. `LogRedactor` loads all active plaintext secret values for the target project.
2. It performs an in-memory scan across every log line.
3. Any exact match with a secret value is replaced with `[REDACTED]`.
4. Only redacted text is persisted to `deployment_logs` and streamed to WebSockets.

---

## 5. Container Sandboxing Constraints

All user application containers created by ForgeLAB enforce these constraints:

| Constraint | MVP Status | Implementation |
| :--- | :--- | :--- |
| **No Privileged Mode** | ENFORCED | `Privileged: false` strictly enforced via `ValidateAndBuildSecureHostConfig` |
| **No-New-Privileges** | ENFORCED | `SecurityOpt: ["no-new-privileges:true"]` enforced on all deployed containers |
| **Capability Dropping & Strict Allowlist** | ENFORCED | `CapDrop: ["ALL"]` enforced on all containers; `allowedContainerCapabilities` strictly permits only `CHOWN`, `SETGID`, and `SETUID` (normalizing `CAP_` prefixes) and rejects disallowed capabilities (e.g., `ALL`, `SYS_ADMIN`, `NET_ADMIN`, `DAC_OVERRIDE`, `FOWNER`) fail-closed with `ErrCapabilityNotAllowed` |
| **Non-Root Execution Profiles** | ENFORCED | Preserves declared non-root `user` execution profile from Compose/Service definitions in `containerConfig.User` |
| **Owner Resource & Storage Quotas** | ENFORCED | `ValidateOwnerResourceQuota` validates cumulative CPU/memory across owner's active and incoming services; 1GB cumulative storage quota on source uploads |
| **Control-Plane Network Isolation** | ENFORCED | Privileged backend container is not attached to tenant Docker networks; reverse proxy routes via published host ports with management port blocking |
| **No Host Network** | ENFORCED | Host networking rejected; binds container port to dynamic host port on loopback `127.0.0.1` by default via `FORGELAB_APP_HOST_IP` (eliminating direct LAN/WAN bypass) |
| **No Docker Socket Mount** | ENFORCED | `/var/run/docker.sock` and named pipes are strictly rejected with security violation |
| **No Host Root/Sensitive Mounts** | ENFORCED | User containers cannot mount `/proc`, `/sys`, `/dev`, `/etc`, `/root`, or host roots |
| **Restart Policy (Docker Engine)** | ENFORCED | `RestartPolicy: unless-stopped` (Docker daemon restarts container on crash; `no` for one-shot jobs) |
| **Resource Quotas (CPU/RAM/PIDs)** | ENFORCED | `ValidateAndBuildSecureHostConfig` enforces explicit `NanoCPUs`, `Memory`, and `PidsLimit` in `HostConfig.Resources`, and rejects unsupported `ephemeral_storage_mb` |
| **Continuous Runtime Health Monitoring** | ENFORCED | Background reconciler (`CheckContinuousRuntimeHealth`) detects stopped/crashed containers, restart deltas, unhealthy Docker checks, and kernel OOM kills (`OOMKilled`), transitioning degraded/failed service records with diagnostics |
| **Continuous Self-Healing / Auto-Rollback** | ENFORCED (CONFIGURABLE) | Bounded crash self-heal restarts (`FORGELAB_SELF_HEAL_*`), sliding-window crash-loop escalation (`FORGELAB_CRASH_LOOP_*`), automatic rollback after sustained unhealthy sweeps or crash loops (`FORGELAB_AUTO_ROLLBACK_*`), and strict previous-container verification (`VerifyPreviousHealthyContainer` / `RestorePreviousHealthyContainer`) before reporting recovery |
| **Read-Only Root Filesystem** | DEFERRED | Future hardening option for stateless containers |

> **Note on Runtime Health Monitoring, Self-Healing & Auto-Rollback:** User containers run with Docker's `unless-stopped` restart policy (`ENFORCED`). In addition, `CheckContinuousRuntimeHealth` actively monitors running services on `FORGELAB_RUNTIME_HEALTH_CHECK_INTERVAL` (default `30s`): when enabled, it attempts bounded container restarts on unexpected crashes up to `FORGELAB_SELF_HEAL_MAX_RESTARTS`, detects crash loops within `FORGELAB_CRASH_LOOP_WINDOW`, and triggers automatic rollback to the previous healthy release (`FORGELAB_AUTO_ROLLBACK_ENABLED`, `FORGELAB_AUTO_ROLLBACK_THRESHOLD`, `FORGELAB_AUTO_ROLLBACK_COOLDOWN`). During failed deployments or crash rollbacks, `VerifyPreviousHealthyContainer` and `RestorePreviousHealthyContainer` verify that the preserved container is actually running and healthy before restoring proxy routing and reporting recovery.

---

## 6. Environment Configuration Hardening & Production-Grade Validation

ForgeLAB enforces strict configuration validation across all non-development environments (`staging`, `production`, etc.):
- **Secret & Key Strength:** Master encryption key (`FORGELAB_ENCRYPTION_KEY`) must be a valid 32-byte key rejecting default development placeholders and low-entropy keys. JWT secret must be at least 32 characters and cannot match known dev placeholders (`forgelab_dev_jwt_secret_change_me_in_production...`).
- **Database Credentials:** Default dev passwords (`forgelab_dev_password`) are strictly rejected.
- **Transport Security:** `COOKIE_SECURE=true` is unconditionally enforced in non-development environments.
- **Source Root Constraints:** `FORGELAB_ALLOWED_SOURCE_ROOTS` must be explicitly configured and cannot be set to root (`/`), drive roots (`C:\`), or system directories (`/etc`, `/var`, `/usr`, `/var/run`, etc.).
- **Boundary Restriction:** Development defaults (such as CORS and frontend URL defaulting to `http://localhost:3000`) are strictly confined to local development (`isDev`).

---

## 7. Multi-Tenant Cross-Project Authorization Boundaries

ForgeLAB strictly validates project ownership and cross-project isolation across both HTTP API boundaries and privileged internal engine operations:
- **HTTP Endpoint Enforcement:** In `ServiceHandler` and `ProjectHandler`, all service lifecycle endpoints (`start`, `stop`, `restart`, `deploy`, `rollback`) and deployment read/log endpoints (`ListDeployments`, `GetDeployment`, `GetLogs`, `GetDeploymentLogs`, `ListServiceDeployments`) verify that the project is owned by the authenticated user, that the service belongs to the project (`service.ProjectID == projectID`), and that any deployment belongs to the specified service and project. Cross-project access attempts are rejected immediately with `400 Bad Request`, `404 Not Found`, or `403 Forbidden`.
- **Privileged Engine Invariant Checks:** Inside `docker.Engine`, privileged lifecycle and execution methods (`StopService`, `StartService`, `RestartService`, `ExecuteServiceDeployment`, and `ExecuteDeployment`) assert that services, parent deployments, and target deployments belong to the authorized project ID before touching Docker daemon containers or state.

---

## 8. Lifecycle Correctness, Worker Draining & Startup Reconciliation

- **Project Deletion & Safe Worker Draining:** When a project is deleted, all in-flight deployment workers are cancelled and synchronously drained via `CancelAndDrainProjectDeployments` before Docker container, network, or image cleanup commences. This ensures workers never create resources after cleanup has completed. Database and cancellation errors are strictly propagated to the caller, and all new deployment and rollback requests are rejected with `409 Conflict` (`ErrProjectDeleting`) while deletion is pending or retrying.
- **Lifecycle Error Propagation:** `StopApp`, `StartApp`, `RestartApp`, `StopService`, `StartService`, and `RestartService` strictly propagate all Docker and database errors, ensuring failures are never reported as successes.
- **Intentional Stop Protection:** `CheckContinuousRuntimeHealth` distinguishes between intentional clean stops (service status `stopped` with exit code 0, 137, or 143 from SIGTERM/SIGKILL) and unexpected crashes, preventing intentional user stops from being erroneously marked as crashes.
- **Startup Reconciliation & Interrupted In-Flight Deployments:** Initial Docker daemon container and runtime state reconciliation executes synchronously on server startup before deployment queue workers are launched, eliminating races between queue job processing and daemon state synchronization. In addition, any deployment or service deployment interrupted mid-progress by a backend restart (`queued`, `cloning`, `building`, `starting`, `health_checking`) is detected and transitioned to `FAILED` with diagnostics (`Deployment interrupted: backend restarted while deployment was in progress`), unblocking redeployments.
- **Port Exposure & Internal Services:** Only web/API services configured with public exposure have host ports allocated. Internal infrastructure (databases, caches, message brokers), background workers, and batch jobs are strictly forbidden from binding host ports (`shouldExposePort`), shielding internal communication within the project Docker network.
- **State Machine & WebSocket Consistency:** Deployment status transitions are broadcast over WebSocket only when database persistence succeeds. The state machine strictly prevents direct invalid jumps from `queued` to `running`.

---

## 9. Registration-Free Local Deployments & Shared Guest Workspace

ForgeLAB supports registration-free local deployments for self-hosted and single-user installations while maintaining strict tenant isolation between anonymous browser visitors and registered users:

1. **Isolated Anonymous Browser Sessions & Safe Local Fallback:**
   - **Anonymous Browser Isolation:** Anonymous users accessing ForgeLAB via a browser are isolated from one another. On their initial request, the server assigns an isolated guest UUID and sets a secure HttpOnly cookie (`forgelab_guest_session`) and response header (`X-Guest-Session`). A dedicated guest user record (`guest-<uuid>@forgelab.local`) is automatically provisioned in PostgreSQL via `userService.EnsureGuestUserWithID`. Anonymous browser visitors cannot view, modify, or access each other's projects, source uploads, deployment logs, or secrets.
   - **Registration-Free Local Usage:** To preserve frictionless registration-free workflows for local CLI tools (`curl`), automated local scripts, and in-memory test harnesses originating from loopback without browser headers, requests without an existing guest session cookie safely resolve to the installation-level guest identity (`models.GuestUserID = uuid.MustParse("00000000-0000-0000-0000-000000000001")`, email `guest@forgelab.local`).
   - User registration and login explicitly reject `guest@forgelab.local`, any `@forgelab.local` domain address, and `models.IsGuest(userID)`.

2. **Guest Identity Resolution & Middleware (`AuthOrGuestMiddleware`):**
   - Endpoints supporting local deployment (`/api/sources/*`, `/api/discovery/plan`, `/api/projects/*`, `/api/ws`, and `/api/ws/ticket`) use `middleware.AuthOrGuestMiddleware`.
   - If an authenticated JWT token is present, the registered user's ID is injected into the request context.
   - If unauthenticated and `FORGELAB_ALLOW_GUEST_MODE=true` (or running in local development `isDev`), `ResolveGuestSession` determines the isolated guest UUID (from cookie/header for browsers/remote clients, or `models.GuestUserID` for local CLI), ensures the user record exists in PostgreSQL, and injects the guest UUID into context with `IsGuestKey=true`.
   - If unauthenticated and guest mode is disabled, the middleware rejects the request with `401 Unauthorized`.
   - For state-changing operations (`POST`, `PUT`, `PATCH`, `DELETE`), incoming `Origin` and `Referer` headers are strictly validated against `allowedOrigins` to prevent unauthorized cross-origin mutations.

3. **Multi-Tenant & Guest Isolation:**
   - Multi-tenant isolation is maintained through existing `owner_id` constraints across `projects`, `services`, `deployments`, `sources`, and `environment_variables`.
   - Anonymous browser sessions cannot view, mutate, deploy, or delete resources owned by other anonymous sessions or registered users.
   - Registered users cannot view or mutate guest-owned projects.
   - WebSocket tickets and log streams (`POST /api/ws/ticket`) verify project ownership against the specific caller's guest session ID, preventing unauthorized log sniffing across anonymous sessions.

4. **Account-Linked GitHub Features vs Public Repository Access:**
   - Endpoints accessing user GitHub accounts, OAuth grants, installations, PAT storage, private repositories, or account-linked repository detection (`GET /api/integrations/github`, `/connect`, `/token`, `/app/install`, `/orgs`, `/disconnect`, `/repositories`, `/repositories/{owner}/{repo}/detect`) invoke `getRegisteredUserID`, strictly rejecting guest identities with `403 Forbidden` (`registration required for GitHub integration`).
   - `GeneratePlan` (`discovery_handler.go`) and `CreateProject` (`project_service.go`) strictly reject `source_type: "github"` when requested by guests (`403` / `400` `registration required for GitHub deployments`), while permitting public repository workflows (`source_type: "github_public"` and public `/repositories/{owner}/{repo}`, `/branches`, `/imports*` where `IsPrivate == false`) for frictionless evaluation.
   - Frontend tabs clearly indicate account requirements for private/linked GitHub features and prompt visitors to sign in or connect an account when needed.

5. **Deployment Scope & Public Exposure Restriction:**
   - ForgeLAB is a self-hosted platform. Guest mode is enabled by default for single-user local development installations (`isDev`).
   - For remote or publicly accessible production installations, guest mode is disabled by default unless explicitly configured via `FORGELAB_ALLOW_GUEST_MODE=true`. This ensures shared guest access is never unintentionally exposed to untrusted public networks.

6. **Abuse Protection, Rate Limiting & Resource Ceilings:**
   - Rate limiting for guest requests is enforced per client IP (`ip:<client_ip>:<resource>`) rather than using a single shared user counter, preventing one visitor from exhausting the global guest rate limit.
   - Client IP extraction uses a secure trusted-proxy model: incoming `X-Forwarded-For` chains are traversed right-to-left stripping configured trusted proxies (`RATE_LIMIT_TRUSTED_PROXIES`), preventing header spoofing from untrusted clients. The Next.js API proxy also drops spoofed forwarding headers from untrusted external clients.
   - Guest workspace enforces a maximum project limit (20 projects) and service limit (5 services per project).
   - Guest services enforce strict resource ceilings: maximum 1000 millicores (1 core) CPU and 1024 MB RAM per service both at project creation and resource update.
   - In-flight concurrent deployments for guests are strictly capped at 5 concurrent builds across both project and service deployments.
   - Source upload body size limits (`100MB`) and strict timeout/decompression guards apply equally to guest uploads.

7. **Lifecycle & Docker Cleanliness:**
   - Guest projects use the identical lifecycle engine, worker draining, and Docker container/image cleanup routines.
   - Deleting a guest project stops active workers, drains pending jobs, and prunes containers and images.
   - The shared guest identity record persists across application restarts and project deletions; it is never removed when projects are deleted.

8. **Migration Rollback & Operator Cleanup Procedure:**
   - Migration `000017_add_guest_identity.down.sql` protects against orphaned Docker containers and resource leaks by refusing to delete the guest user identity while any guest-owned projects or sources exist in PostgreSQL.
   - If an operator attempts to run `migrate down` while guest projects exist, PostgreSQL raises an exception:
     `Cannot rollback migration 000017_add_guest_identity: X guest project(s) and Y guest source(s) exist. Operators must delete all guest-owned projects and clean up runtime resources via ForgeLAB before rolling back the guest identity.`
   - **Operator Procedure to decommission guest mode:**
     1. With the ForgeLAB server running, delete all guest projects via the ForgeLAB UI or API (`DELETE /api/projects/{id}`). This triggers the engine's clean shutdown, container removal, port deallocation, and volume pruning.
     2. Verify no guest projects remain: `SELECT COUNT(*) FROM projects WHERE owner_id = '00000000-0000-0000-0000-000000000001';`
     3. Apply database migration rollback: `migrate -path backend/migrations -database "$DATABASE_URL" down 1`.

---

## 10. Public Project Sharing & Outbound Gateway Trust Boundaries

ForgeLAB’s public sharing feature enables users to share locally deployed projects over the internet without compromising control-plane integrity, exposing private network topology, or allowing arbitrary host access. ForgeLAB supports both domainless Cloudflare Quick Tunnels (default) and self-hosted public gateways.

### 1. Cloudflare Quick Tunnel Provider Security Model (`FORGELAB_SHARING_PROVIDER=cloudflare`)
- **Zero Inbound Attack Surface:** Outbound QUIC/HTTP2 tunnel initiated by a supervised child process (`cloudflared tunnel --url http://127.0.0.1:<LOCAL_PORT>`). No router port forwarding, static IP, or public domain required.
- **Safe Subprocess Execution:** Cloudflared is executed via Go's safe `os/exec.CommandContext` using explicit argv slices without shell interpretation (`sh -c`). Process arguments are never constructed via string formatting with user or visitor input.
- **Strict Ephemeral Port & Loopback Binding:** The local router (`LocalShareServer`) binds strictly to loopback (`127.0.0.1:0`), obtaining an ephemeral random port allocated by the OS kernel. It is never exposed on `0.0.0.0` or external network interfaces.
- **URL Discovery & Validation:** Captures tunnel output into bounded line buffers (capped at 512 lines) and validates the provider-issued URL with a strict regex (`^https://[a-zA-Z0-9-]+\.trycloudflare\.com$`). Rejects any malformed, non-HTTPS, or placeholder domain (`example.com`, `localhost`).
- **Config Conflict Detection:** Cloudflare Quick Tunnels fail if a pre-existing `~/.cloudflared/config.yml` exists. ForgeLAB detects this conflict and surfaces an actionable notice without modifying or deleting user files.
- **Process Supervision & Lifecycle Cleanup:** The child process is supervised with an in-memory registry. Revocation, expiration, project deletion, or server shutdown immediately terminates the process via `os.Interrupt`, falling back to `Kill()` if unresponsive after 3 seconds.
- **Startup Stale Share Reconciliation:** Ephemeral trycloudflare URLs cannot survive a ForgeLAB restart. On server boot, `ReconcileStaleShares` discovers orphaned active cloudflared shares in the database and marks them `revoked`, guaranteeing the UI never displays dead ghost links.

### 2. Self-Hosted Gateway Security Model (`FORGELAB_SHARING_PROVIDER=gateway`)
- **Architectural Separation:** The public gateway (`cmd/gateway`) runs on an isolated internet-facing server with zero access to Docker, PostgreSQL, Redis, or local source files.
- **Constant-Time Secret Comparison:** Handshake authentication uses `crypto/subtle.ConstantTimeCompare`, neutralizing timing side-channel attacks.
- **Fail-Closed Secret Validation:** Gateway refuses to start if `GATEWAY_AUTH_SECRET` is empty or shorter than 16 bytes.
- **Per-Session Registration Isolation:** Registered share routes are tracked per Yamux tunnel session. A tunnel cannot deregister or overwrite shares belonging to another active tunnel session.
- **Synchronous Acknowledgment Handshake:** `TunnelManager.RegisterShare` waits for an explicit gateway acknowledgment frame with a bounded 5-second timeout; if unacknowledged or disconnected, the operation fails closed without reporting the share as online.
- **Native HTTPS & TLS Ingress:** The gateway natively supports TLS on `:443` or an upstream TLS-terminating reverse proxy.
- **Strict Verified TLS in Production:** Connecting to a public gateway in production (`!isDev`) strictly requires verified TLS. The backend rejects `GATEWAY_TLS_SKIP_VERIFY=true` during startup configuration validation and eliminates automatic TLS-to-plaintext TCP fallback.

### 3. Share-Scoped Request Router (`internal/tunnel/share_router.go`) & Control-Plane Isolation
- **Control-Plane Network Isolation:** The backend is never attached to tenant Docker networks. Upstream routing resolves strictly via published container `HostPort` bindings on loopback or `host.docker.internal`.
- **Management Plane Protection:** The router and upstream resolver strictly block proxying to ForgeLAB management ports (:8080), local source agent (:4142), frontend (:3000), PostgreSQL (:5432), Redis (:6379), `forgelab-backend`, `forgelab-frontend`, and the Docker socket.
- **Dynamic Preview Origin Validation:** Rather than trusting broad `*.trycloudflare.com` wildcards in CORS, the backend maintains a dynamic thread-safe host registry (`RegisterPreviewHost`, `UnregisterPreviewHost`) synchronized with active database shares. Untrusted forwarding headers are stripped before origin evaluation.
- **Path Traversal Defense:** Both raw `RequestURI` and normalized `URL.Path` are validated. Any path containing `..` or URL-encoded variations (`%2e%2e`) is immediately rejected with HTTP `400 Bad Request`.

- **Longest-Prefix Deterministic Matching:** Routes are sorted by prefix length in descending order, guaranteeing `/api/*` requests reach the backend API rather than being swallowed by the frontend `/` fallback handler.
- **Authoritative Route Allowlists:** Target service IDs and host ports are resolved solely from verified database records. Visitors cannot control upstream targets via headers or query parameters.
- **WebSocket Hijacking & Streaming:** Supported via safe `http.Hijacker` connection takeover with bidirectional byte copying, bounded timeouts, and strict error handling.

### 4. Container Host Port Loopback Binding
- Deployed application container host ports bind to `127.0.0.1` by default via `FORGELAB_APP_HOST_IP`.
- Prevents LAN/WAN bypass attacks where an attacker on the same local network attempts to reach deployed containers directly on raw host ports (`10000–60000`).

### 5. Immediate Revocation & Expiration Invariant
- Revoking a share marks the DB record `revoked`, terminates the child tunnel process or deregisters the gateway route, and closes the local ephemeral HTTP listener.
- Expired or revoked shares return HTTP `404 Not Found` or `410 Gone`.
- Revocation never alters running container state, does not rebuild containers, and does not affect local preview URLs.

---

## 11. Custom Domains, Push-to-Deploy Webhooks & Volume Backup/Restore Security

### 1. Custom Domain Lifecycle & Caddy Truthfulness (`internal/services/domain_service.go`, `internal/proxy/caddy_syncer.go`)
- **Hostname Validation:** `proxy.ValidateCustomDomain` normalizes domains to lowercase and rejects malformed, IP-literal, wildcard, or loopback/localhost hostnames.
- **DNS Ownership & Routing Verification:** `VerifyDomain` requires an exact cryptographic verification token match on the DNS TXT record `_forgelab-verify.<domain>`. When a non-local `FORGELAB_INGRESS_HOST` is configured, it additionally requires CNAME or A/AAAA IP alignment before passing verification (failing with HTTP `422` otherwise).
- **Fail-Closed HTTPS Status (`tls_status` & `https_active`):** ForgeLAB **never** reports `tls_status: "active"` or `https_active: true` based solely on DNS checks. It requires `CADDY_ADMIN_URL` to be configured **and** route synchronization against the Caddy Admin API to succeed (`502` on sync failure). When Caddy is unconfigured, verified domains explicitly report `tls_status: "proxy_unconfigured"` and `https_active: false`.

### 2. Push-to-Deploy Webhook Receiver (`internal/handlers/webhook_handler.go`, `internal/services/webhook_service.go`)
- **Payload Size Guard:** `POST /api/webhooks/github` enforces `http.MaxBytesReader` at `1 MiB` (`maxWebhookBodyBytes`), rejecting oversized payloads with HTTP `413 Payload Too Large`.
- **Constant-Time HMAC-SHA256 Verification:** Signatures in `X-Hub-Signature-256: sha256=<hex>` are verified against the project's encrypted webhook secret using `crypto/hmac` + `hmac.Equal`. Missing headers, invalid signatures, or unavailable encryption keys fail closed with HTTP `401 Unauthorized`.
- **Replay Prevention & Secret Protection:** Delivery UUIDs in `X-GitHub-Delivery` are deduplicated to prevent replay triggering. Secrets are encrypted at rest with AES-256-GCM; plaintext `webhook_secret` is returned **once** upon initial generation or explicit rotation (`PATCH /api/projects/{id}/webhook`) and omitted thereafter.

### 3. Persistent Volume Backup & Restore Safeguards (`internal/docker/engine_volumes.go`, `internal/handlers/volume_handler.go`)
- **Project Ownership & Volume Name Validation:** Volume endpoints validate caller ownership and strictly sanitize volume names with `ValidateVolumeName` (`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`), rejecting path traversal before touching Docker resources.
- **Live Mount Conflict Protection:** Restoring a volume mounted by running containers is rejected with HTTP `409 Conflict` (`ErrVolumeInUse`), preventing filesystem corruption.
- **Pre-Restore Archive Validation & Sandboxed Execution:** Incoming `.tar.gz` streams are pre-staged and validated before clearing volume data, enforcing bounded limits (`100 MB` compressed, `512 MB` uncompressed, `50,000` files -> HTTP `413`), rejecting path traversal (`sanitizeTarEntryPath`), escaping symlinks (`validateInternalSymlinkTarget`), hardlink escapes, and device/FIFO nodes (`tar.TypeChar`/`TypeBlock`/`TypeFifo`), and stripping `setuid`/`setgid` mode bits (`0o6000`). Extraction runs inside a locked-down Alpine helper container (`CapDrop: ["ALL"]`, `SecurityOpt: ["no-new-privileges:true"]`, `NetworkMode: "none"`).

