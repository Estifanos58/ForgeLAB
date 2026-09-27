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

## 3. Source Workspace Security & Ingestion Protection

ForgeLAB ingests application source through two primary channels, treating all source code as untrusted input:

### 1. Browser-Based Computer Uploads (`/api/sources/upload`)
- **Zip Slip & Path Traversal Prevention:** Archive extraction and multipart file ingestion evaluate every relative path with `filepath.Clean`. Any entry attempting to traverse outside the allocated directory (e.g. `../`, absolute paths, or symlink escapes) is immediately rejected with `ErrPathTraversalDetected` and the directory is purged.
- **Decompression Bomb Protection:** Imposes a strict `MaxUncompressedBytes` limit (100MB). Exceeding this limit halts decompression and wipes the target folder.
- **Ignore Filtering:** Automatically rejects or skips build artifacts, dependencies, and version control metadata (`node_modules`, `.git`, `.next`, `dist`, `__pycache__`, etc.) during extraction.
- **Workspace Isolation:** Each upload is stored in a dedicated, random UUID workspace (`data/sources/<source_id>`) isolated from other projects and host system directories.

### 2. GitHub Repository Tarball Ingestion
- **Server-Side Token Verification:** Only repositories accessible via the authenticated user's encrypted OAuth token are cloned/fetched.
- **Encrypted Token Handling:** Tokens are decrypted in-memory only when communicating with `api.github.com` and are never logged or exposed to the client.
- **Tarball Extraction Isolation:** Remote tarballs are unpacked using the same path-traversal safeguards and size limits into isolated source directories.

### 3. Snapshot Isolation
Before the Docker build engine starts, ForgeLAB snapshots the source files into `data/builds/<deployment-id>`:
- Prevents concurrent edits from corrupting active builds.
- Isolates build context and prevents path traversal escapes during `docker build`.
- Snapshot directory is automatically wiped after the image build finishes.

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
| **No Privileged Mode** | ENFORCED | `Privileged: false` (default in container.HostConfig) |
| **No Host Network** | ENFORCED | Binds container port to dynamic host port on `0.0.0.0` |
| **No Docker Socket Mount** | ENFORCED | `/var/run/docker.sock` is **never** mounted in user containers |
| **No Host Root Mounts** | ENFORCED | User containers mount no host volumes; files exist in image layer |
| **Restart Policy (Docker Engine)** | ENFORCED | `RestartPolicy: unless-stopped` (Docker daemon restarts container on crash) |
| **Continuous Self-Healing** | NOT IMPLEMENTED | Platform-level health polling, crash-loop detection, and auto-rollback deferred |
| **Resource Quotas (CPU/RAM)** | DEFERRED | Future Docker `HostConfig.Resources` limit enforcement |
| **Read-Only Root Filesystem** | DEFERRED | Future hardening option for stateless containers |

> **Note on Self-Healing vs Docker Restart Policy:** User containers run with Docker's `unless-stopped` restart policy (`IMPLEMENTED`), allowing the Docker engine itself to restart crashed containers. However, ForgeLAB does **not** implement platform-level continuous health monitoring, crash diagnosis, crash-loop analysis, automated rollback, or re-promotion (`NOT IMPLEMENTED`). Docker's restart policy must not be confused with platform-level self-healing.
