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
- **Strict Container Isolation:** All user containers are created with `CapDrop: ["ALL"]`, `SecurityOpt: ["no-new-privileges:true"]`, `Privileged: false`, isolated bridge networks (rejecting `--net=host` and `container:` network namespace sharing), and restricted resource limits (CPU millicores, RAM, and PIDs).
- **Source Root Sandboxing:** All host directory mounts are validated with canonical symlink resolution and constrained strictly beneath administrator-configured `FORGELAB_ALLOWED_SOURCE_ROOTS`, rejecting system directories (`/etc`, `/proc`, `/sys`, `/dev`, etc.).
- **Build Reproducibility:** Pinning immutable source revisions (commit SHAs or content fingerprints `fp_...`) ensures that builds fail closed if host files change after plan review.

---

## 2. Authentication Architecture & Token Security

ForgeLAB implements backend-owned, cookie-based session management and hardened realtime streaming:

```text
┌─────────────────────────────────────────────────────────────┐
│                 Session & Identity Security                 │
│                                                             │
│   Transport & Storage:                                      │
│   • Issued as HttpOnly, Secure, SameSite cookies:           │
│     - forgelab_access_token (15-minute expiry)              │
│     - forgelab_refresh_token (7-day expiry)                 │
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
- **`PathValidator` Security Boundary:** Validates directory existence, verifies that the target is a directory (not a regular file or device), and canonicalizes paths with full symlink resolution (`filepath.EvalSymlinks`).
- **Configured Allowed Roots (`FORGELAB_ALLOWED_SOURCE_ROOTS`):** Only paths situated beneath explicitly configured root directories are permitted. All attempts to navigate outside (e.g. traversal via `..` or symlink escape) are rejected with `ErrPathNotAllowed`.
- **System Directory Rejection:** System directories (`/etc`, `/var`, `/usr`, `C:\Windows`, `C:\Program Files`, etc.) are unconditionally rejected with `ErrRestrictedSystemPath`.
- **Docker Compose Host-to-Container Translation:** In containerized setups, the backend mounts `${FORGELAB_HOST_SOURCE_ROOT}:/host-projects:ro` as a read-only volume. The backend safely translates host paths beneath `FORGELAB_HOST_SOURCE_ROOT` to `/host-projects/...` preventing arbitrary host container path spoofing.
- **Cross-User Project Path Authorization:** Project creation and deployments verify that the authenticated user owns the project record. A user cannot hijack or redeploy another user's project path.
- **Direct Build Security:** Build context is streamed concurrently into Docker with early `.dockerignore` pruning. In-memory `bytes.Buffer` buffering is completely eliminated, and virtual files (such as auto-generated `Dockerfile.forgelab`) are injected into the TAR stream without requiring write access to the source filesystem.

### 2. Browser-Based Archive Uploads (`source_type: "local_upload"`)
- **Zip Slip & Path Traversal Prevention:** Archive extraction via sequential `r.MultipartReader()` evaluates every relative path with `filepath.Clean`. Any entry attempting to traverse outside the allocated directory (e.g. `../`, absolute paths, or symlink escapes) is immediately rejected with `ErrPathTraversalDetected` and the directory is purged.
- **Decompression Bomb Protection:** Imposes a strict `MaxUncompressedBytes` limit (100MB). Exceeding this limit halts decompression and wipes the target folder.
- **Workspace Isolation:** Each upload is stored in a dedicated, random UUID workspace (`data/sources/<source_id>`) isolated from other projects and host system directories.

### 3. GitHub Repository Tarball Ingestion (`source_type: "github"`)
- **Server-Side Token Verification:** Only repositories accessible via the authenticated user's encrypted OAuth token are cloned/fetched.
- **Encrypted Token Handling:** Tokens are decrypted in-memory only when communicating with `api.github.com` and are never logged or exposed to the client.
- **Tarball Extraction Isolation:** Remote tarballs are unpacked using the same path-traversal safeguards and size limits into isolated source directories.

### 4. Local Agent Connected Source (`source_type: "local_agent"`)
- **Unauthenticated Identity Verification Probe:** The control plane never sends bearer credentials or session tokens to unverified fallback endpoints. When communicating with a local agent candidate (whether running natively on the host at `127.0.0.1:8081` or via Docker bridge at `host.docker.internal:8081`), the control plane first issues an unauthenticated identity verification probe (`POST /api/agent/verify-identity` or `GET /api/agent/status` with `X-Expected-Agent-ID`). Only endpoints that affirmatively match the expected agent identity are sent bearer session credentials.
- **Short-Lived & Revocable Credentials:** Agent session tokens default to a 15-minute TTL (configurable via `FORGELAB_AGENT_SESSION_TTL`, reduced from legacy 30 minutes). Tokens can be revoked explicitly via `POST /api/sources/agent/session/revoke`, and all active agent sessions bound to a source workspace are automatically revoked when the source workspace is deleted (`DELETE /api/sources/{id}`).
- **Scrubbing & Encryption:** Unauthenticated agent status checks scrub `agent_id` and active session counts. Session serialization scrubs host filesystem paths. Raw session tokens are encrypted at rest with AES-256-GCM and never exposed over WebSocket events, logs, or unredacted REST responses.
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
| **Capability Dropping** | ENFORCED | `CapDrop: ["ALL"]` enforced to prevent kernel privilege escalation |
| **No Host Network** | ENFORCED | Host networking rejected; binds container port to dynamic host port on loopback `127.0.0.1` by default via `FORGELAB_APP_HOST_IP` (eliminating direct LAN/WAN bypass) |
| **No Docker Socket Mount** | ENFORCED | `/var/run/docker.sock` and named pipes are strictly rejected with security violation |
| **No Host Root/Sensitive Mounts** | ENFORCED | User containers cannot mount `/proc`, `/sys`, `/dev`, `/etc`, `/root`, or host roots |
| **Restart Policy (Docker Engine)** | ENFORCED | `RestartPolicy: unless-stopped` (Docker daemon restarts container on crash) |
| **Resource Quotas (CPU/RAM/PIDs)** | ENFORCED | `ValidateAndBuildSecureHostConfig` clamps `NanoCPUs`, `Memory`, and `PidsLimit` in `HostConfig.Resources` |
| **Continuous Runtime Health Monitoring** | ENFORCED | Background reconciler (`CheckContinuousRuntimeHealth`) detects stopped/crashed containers and kernel OOM kills (`OOMKilled`), transitioning service records to `FAILED` with diagnostics |
| **Continuous Self-Healing / Auto-Rollback** | NOT IMPLEMENTED | Automated remediation and auto-rollback deferred |
| **Read-Only Root Filesystem** | DEFERRED | Future hardening option for stateless containers |

> **Note on Health Monitoring vs Self-Healing vs Docker Restart Policy:** User containers run with Docker's `unless-stopped` restart policy (`IMPLEMENTED`), allowing the Docker engine itself to restart crashed containers. ForgeLAB also implements continuous runtime health monitoring (`CheckContinuousRuntimeHealth`) to detect stopped/crashed containers and kernel OOM kills, updating service records with actionable diagnostics. However, ForgeLAB does **not** implement automated self-healing, automated rollback, or automatic re-promotion upon runtime failure (`NOT IMPLEMENTED`).

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

4. **GitHub Features are Registration-Only:**
   - All GitHub OAuth, repository listing, branch inspection, detection, and personal access token endpoints (`/api/integrations/github/*`) remain strictly behind `middleware.AuthMiddleware` requiring valid user JWT tokens.
   - `GeneratePlan` and `CreateProject` strictly reject `source_type: "github"` when requested by guests with HTTP `403 Forbidden` (`registration required for GitHub deployments`).
   - Frontend tabs indicate account requirement for GitHub and prompt visitors to sign in or create an account.

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

### 3. Share-Scoped Request Router (`internal/tunnel/share_router.go`)
- **Path Traversal Defense:** Both raw `RequestURI` and normalized `URL.Path` are validated. Any path containing `..` or URL-encoded variations (`%2e%2e`) is immediately rejected with HTTP `400 Bad Request`.
- **Management Plane Protection:** The router strictly blocks proxying to ForgeLAB management ports (:8080), local source agent (:4142), PostgreSQL (:5432), Redis (:6379), and the Docker socket.
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




