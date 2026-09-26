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

## 2. Authentication Architecture: Reality vs. Hardening

### Current MVP Implementation (Reality)
The current MVP codebase implements authentication as follows:

```text
┌─────────────────────────────────────────────────────────────┐
│                      Current MVP Auth                       │
│                                                             │
│   REST Authentication:                                      │
│   • Bearer token sent in "Authorization: Bearer <token>"    │
│   • Token stored in browser "localStorage" (forgelab_token) │
│                                                             │
│   WebSocket Authentication:                                 │
│   • Token passed via query string (?token=<access_token>)   │
│                                                             │
│   Token Properties:                                         │
│   • HS256 signed JWT with user_id and email                 │
│   • 15-minute access token expiry                           │
│   • 7-day refresh token with atomic single-use DB rotation  │
└─────────────────────────────────────────────────────────────┘
```

### Production-Hardening Roadmap (Deferred to Post-MVP)
The following security hardening measures are documented as future requirements:

1. **HttpOnly Secure Cookies:** Transition frontend token storage from `localStorage` to `HttpOnly`, `Secure`, `SameSite=Lax` cookies. (Backend handlers already set these cookies, but the frontend currently relies on `localStorage` for Bearer headers).
2. **Eliminate Query String Tokens for WebSockets:** Query parameters can be captured in web server access logs, browser history, and proxy telemetry. Future hardening should authenticate WebSocket upgrades exclusively via HttpOnly cookies or an initial JSON authentication handshake frame.
3. **Strict Origin Validation:** Enforce strict CSRF origin validation on the WebSocket upgrader (`CheckOrigin` currently returns `true` for development flexibility in `internal/websocket/hub.go:24-26`).

---

## 3. Host Path Security & Source Validation

Because the MVP imports source from the host filesystem, `internal/security/PathValidator` enforces strict defense-in-depth:

```text
Candidate Path (from user)
         │
         ▼
1. filepath.Abs() ──► Ensure absolute path
         │
         ▼
2. filepath.EvalSymlinks() ──► Canonicalize and resolve all symlinks
         │
         ▼
3. os.Stat() ──► Verify path exists and is a directory
         │
         ▼
4. Restricted System Path Check ──► REJECT if matching:
         • /etc, /var, /usr, /sys, /proc, /dev, /boot, /bin, /sbin
         • C:\Windows, C:\Program Files, C:\Program Files (x86), C:\System Volume Information
         │
         ▼
5. Boundary Check (if FORGELAB_ALLOWED_SOURCE_ROOTS is set)
         • Ensures canonical path resides inside configured root whitelist
         │
         ▼
Valid Canonical Path ──► Ready for snapshot copy
```

### Snapshot Isolation
ForgeLAB copies the repository source into an isolated temporary directory (`data/builds/<deployment-id>`) before initiating the Docker build:
- Prevents concurrent edits on the host filesystem from corrupting active builds.
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
| **Restart Policy** | ENFORCED | `RestartPolicy: unless-stopped` |
| **Resource Quotas (CPU/RAM)** | DEFERRED | Future Docker `HostConfig.Resources` limit enforcement |
| **Read-Only Root Filesystem** | DEFERRED | Future hardening option for stateless containers |
