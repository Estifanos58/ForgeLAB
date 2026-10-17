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

### Critical Trust Realities:

```text
ForgeLab backend = highly trusted control-plane component.

Backend has Docker Engine control via /var/run/docker.sock.

User repositories = untrusted input.

Repository source code must NEVER be treated as trusted merely because
the user imported it.

A compromise of the ForgeLab backend becomes a host-level Docker
control/security event.
```

### Why This Trust Model Matters:
1. **Local Repositories:** Even though a repository resides on the local host machine, the control plane must not trust its file hierarchy. A malicious repository could contain symlinks pointing to `/etc/shadow`, `C:\Windows\System32`, or developer SSH keys. `PathValidator` strictly resolves symlinks and validates canonical paths before snapshotting.
2. **Future GitHub Repositories:** Remote repositories imported via OAuth are completely untrusted. They may contain poisoned build scripts, dependency confusion attacks, or malicious Dockerfiles.
3. **Docker Builds:** The `docker build` process executes commands (`RUN`) as specified in the repository's `Dockerfile`. Any `RUN` command executes within the build container. Build contexts must **never** mount the host root or the Docker socket.
4. **Runtime Containers:** User applications execute inside container sandboxes. They must never be granted `--privileged` mode, host network mode (`--net=host`), host filesystem volume mounts, or access to `/var/run/docker.sock`.

---

## 2. Authentication Architecture & Token Security

ForgeLAB implements backend-owned, cookie-based session management:

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
| **No Host Network** | ENFORCED | Host networking rejected; binds container port to dynamic host port on `0.0.0.0` |
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
- **Startup Reconciliation:** Initial Docker daemon container and runtime state reconciliation executes synchronously on server startup before deployment queue workers are launched, eliminating races between queue job processing and daemon state synchronization.
- **State Machine & WebSocket Consistency:** Deployment status transitions are broadcast over WebSocket only when database persistence succeeds. The state machine strictly prevents direct invalid jumps from `queued` to `running`.


