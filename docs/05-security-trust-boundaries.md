# ForgeLab — Security & Trust Boundaries

**Status:** Current  
**Last Updated:** 2026-09-25  

---

## Threat Model

### Trust Boundaries

```
┌─────────────────────────────────────────────────────┐
│                UNTRUSTED                             │
│                                                      │
│  ┌───────────────┐    ┌──────────────────────┐      │
│  │ Browser/Client│    │ User Repositories    │      │
│  │               │    │ (arbitrary code)     │      │
│  └───────┬───────┘    └──────────┬───────────┘      │
│          │                       │                   │
└──────────┼───────────────────────┼───────────────────┘
           │                       │
     ══════╪═══════════════════════╪═══ TRUST BOUNDARY
           │                       │
┌──────────┼───────────────────────┼───────────────────┐
│          ▼          TRUSTED      ▼                   │
│  ┌──────────────┐       ┌────────────────┐           │
│  │ ForgeLab API │       │ Docker Engine  │           │
│  │ (Go)         │       │ (sandboxed)    │           │
│  └──────┬───────┘       └────────────────┘           │
│         │                                            │
│  ┌──────┴───────┐  ┌──────────┐                     │
│  │ PostgreSQL   │  │  Redis   │                      │
│  └──────────────┘  └──────────┘                      │
│                                                       │
│               HOST MACHINE                            │
└───────────────────────────────────────────────────────┘
```

### Threat Categories

| Threat | Mitigation |
|--------|-----------|
| Unauthorized API access | JWT authentication on all API endpoints |
| Cross-user data access | Authorization checks: user can only access own projects/deployments |
| WebSocket eavesdropping | Subscription authorization — server validates user owns the project/deployment |
| Secret leakage in logs | Secret redaction in log pipeline before storage and streaming |
| Secret leakage in API | Secrets never returned in plaintext via API (write-only or masked) |
| Secret leakage in DB | AES-GCM encryption at rest for secret values |
| Arbitrary code execution | Docker container isolation; future: resource limits |
| Host filesystem access | Containers do NOT get host filesystem access; source is copied into build context |
| Container resource exhaustion | Future: CPU/memory limits via Docker container config |
| Token theft | JWT short expiry + refresh token rotation; HTTPS in production |
| SQL injection | Parameterized queries via pgx (never string concatenation) |
| CSRF | SameSite cookies + CSRF tokens for browser requests |

---

## Authentication Design

### ForgeLab User Authentication

- **Registration:** email + password (bcrypt hashed, cost 12)
- **Login:** email + password → JWT access token + refresh token
- **Access Token:** Short-lived (15 min), contains user_id and email
- **Refresh Token:** Longer-lived (7 days), stored in DB, single-use with rotation
- **Token Storage (Client):** httpOnly secure cookies (preferred) or localStorage (development)

### JWT Claims

```json
{
  "sub": "<user-uuid>",
  "email": "user@example.com",
  "iat": 1695648000,
  "exp": 1695648900
}
```

### Authorization Model (MVP)

Simple ownership-based authorization:

```
Request → Extract JWT → Validate → Extract user_id
→ Load resource → Check resource.owner_id == user_id
→ Allow or 403
```

Future: RBAC with roles (Owner, Admin, Developer, Viewer) layered on top.

---

## Secret Management

### Encryption

- **Algorithm:** AES-256-GCM
- **Key Management:** Encryption key loaded from environment variable `FORGELAB_ENCRYPTION_KEY`
- **Key Format:** 32-byte key (base64 encoded in env var)
- **Storage:** Encrypted values stored as BYTEA in PostgreSQL

### Secret Lifecycle

```
User sets env var via API
→ Value encrypted with AES-GCM (unique nonce per value)
→ Stored as encrypted bytes in environment_variables table
→ At deployment time: decrypted in memory
→ Passed to Docker container as environment variables
→ Never logged, never returned in plaintext via API
```

### Secret Redaction in Logs

Before any log line is stored or streamed:

1. Collect all secret keys for the project
2. Collect all decrypted secret values
3. Replace any occurrence of a secret value in the log line with `[REDACTED]`
4. This applies to both build logs and runtime logs

### API Behavior for Secrets

| Operation | Behavior |
|-----------|----------|
| Create/Update | Accept plaintext value, encrypt, store |
| List | Return key names only, no values |
| Read | Return key + masked value (`••••••••`) |
| Delete | Delete the record |
| Deploy-time | Decrypt in memory, pass to container |

---

## Container Isolation

### MVP Constraints

- Containers run with default Docker isolation
- No `--privileged` flag
- No host network mode
- No host filesystem bind mounts (source is copied)
- Docker socket is NOT exposed to user containers

### Future Constraints (Not MVP)

- CPU limits: `--cpus`
- Memory limits: `--memory`
- PID limits: `--pids-limit`
- Read-only filesystem: `--read-only` (where applicable)
- No-new-privileges: `--security-opt=no-new-privileges`
- Network isolation between user containers
