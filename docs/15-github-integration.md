# ForgeLAB — GitHub Integration Architecture

**Status:** GITHUB APP & DURABLE IMPORT LIFECYCLE IMPLEMENTED; PUBLIC & PRIVATE REPOSITORY INGESTION ACTIVE  
**Authentication Milestone:** GitHub OAuth 2.0 Web Flow is implemented as an isolated sign-in identity provider (`read:user user:email`).  
**Repository Access Milestone:** IMPLEMENTED. Dedicated GitHub App installation flow provides short-lived installation access tokens with read-only Contents & Metadata permissions. Backward-compatible OAuth (`repo` scope) and Personal Access Token (PAT) fallbacks are retained. Tokens are encrypted at rest with AES-256-GCM.  
**Import Pipeline Milestone:** IMPLEMENTED. Durable backend import lifecycle (`repository_imports` table) with single immutable snapshot reuse across discovery, plan generation, project creation, and deployment.

---

## Related Documents

- [INSTRUCTION.md](../INSTRUCTION.md) — Operational memory & architectural constraints
- [docs/00-current-state.md](00-current-state.md) — Current state snapshot & matrix
- [docs/02-functional-requirements.md](02-functional-requirements.md) — R4 GitHub import requirements
- [docs/05-security-trust-boundaries.md](05-security-trust-boundaries.md) — Token encryption & untrusted source code trust model
- [docs/09-api-contract.md](09-api-contract.md) — GitHub OAuth, App, Repository Integration, and Webhook endpoints
- [docs/13-decisions.md](13-decisions.md) — DEC-008 (Repository Selection), DEC-010 (OAuth Authentication), DEC-011 (Universal Source Ingestion), DEC-015 (DeploymentPlan & Import Pipeline), & DEC-017 (Stabilization & Recoverable Imports)
- [docs/14-known-limitations.md](14-known-limitations.md) — Known limits & boundary constraints

---

## 0. Architectural Separation: Authentication vs. Repository Access

ForgeLAB strictly separates identity authentication from repository authorization:

1. **GitHub as an Authentication Provider (Sign-In Only):**
   - Implemented via `/api/auth/github` and `/api/auth/github/callback`.
   - Requests minimal identity scopes: `read:user user:email`.
   - Stores user identity in the `auth_identities` table.
   - Does **not** request or store repository access tokens (`repo` scope).
   - Issues authoritative ForgeLAB session tokens via HttpOnly cookies.

2. **GitHub App for Repository Authorization (Preferred):**
   - Server-side RS256 JWT minted from configured Private Key to authenticate as the GitHub App.
   - User installs App on personal account or organization with read-only **Contents** and **Metadata** permissions.
   - Installation verified server-side and recorded in `github_app_installations`.
   - Short-lived installation access tokens (`ghs_...`) minted on demand and cached in-memory with automatic expiry renewal.
   - Retains zero long-lived repository secrets in the frontend.

3. **Public Repository Ingestion (No GitHub Account Required):**
   - Accessible to both registered users and anonymous visitors (Guest Mode).
   - Direct inspection and archive retrieval using unauthenticated GitHub API endpoints.
   - Enforces strict archive safety limits (100MB compressed, 100MB uncompressed, 10,000 files, 50MB single file, path traversal guards).

4. **OAuth / PAT Fallbacks (Legacy & Fine-Grained):**
   - For environments where a GitHub App cannot be registered or for personal developer PAT tokens.
   - Tokens encrypted at rest in `github_integrations` via AES-256-GCM.

---

## 1. Durable Import Pipeline & Single Immutable Snapshot

ForgeLAB eliminates redundant multi-stage git fetches by creating a durable, tracked import lifecycle:

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                       ForgeLAB Import Lifecycle                             │
└─────────────────────────────────────────────────────────────────────────────┘
  1. POST /api/integrations/github/imports (Start Import)
     │
     ▼
  2. Verify Access (GitHub App installation token / OAuth / Public)
     │
     ▼
  3. Resolve Commit SHA (authoritative 40-character git commit hash)
     │
     ▼
  4. Download & Extract Archive (Tarball stream -> data/sources/imports/<id>)
     - Traversal & symlink escaping protection
     - 100MB compressed / 100MB uncompressed ceiling
     - 10,000 file count & 50MB single file limit
     │
     ▼
  5. Discovery Engine (Scans extracted snapshot; respects monorepo root_dir)
     │
     ▼
  6. Generate Deployment Plan (Pinned to resolved SourceRevision / Commit SHA)
     │
     ▼
  7. Status = "ready" (Frontend polls /api/integrations/github/imports/<id>)
     │
     ▼
  8. Create Project (Transfers snapshot_path & commit_sha to sources table)
     │
     ▼
  9. Deployment Engine (DockerEngine copies snapshot directly without re-fetching!)
```

### Immutable Snapshot Guarantees
- The exact git tree scanned during discovery is the exact code built and deployed.
- Eliminates race conditions where a commit push between repository selection and build triggers unexpected changes.
- Inactive or abandoned imports are automatically pruned from disk and database after 24 hours.

---

## 2. GitHub App Setup & Configuration

To enable GitHub App installation-based repository importing:

1. **Create a GitHub App** in GitHub Developer Settings:
   - **GitHub App name:** `ForgeLAB-Local` (or your company name)
   - **Homepage URL:** `http://localhost:3000`
   - **Callback URL:** `http://localhost:3000/api/integrations/github/app/callback`
   - **Webhook URL:** `https://<your-public-host>/api/webhooks/github?project_id=<uuid>` (or per-project secret configured via **Settings -> Webhooks**)
   - **Permissions:**
     - Repository Permissions -> **Contents**: Read-only
     - Repository Permissions -> **Metadata**: Read-only
   - **Where can this GitHub App be installed?** -> Any account (personal & organization).

2. **Generate Private Key:**
   - In GitHub App settings, click **Generate a private key**.
   - Download the `.pem` file to a secure directory (e.g. `certs/forgelab-app.private-key.pem`).

3. **Configure ForgeLAB Environment:**
   ```bash
   GITHUB_APP_ID=123456
   GITHUB_APP_PRIVATE_KEY_PATH=/path/to/forgelab-app.private-key.pem
   # Or inline multi-line / escaped PEM:
   # GITHUB_APP_PRIVATE_KEY="-----BEGIN RSA PRIVATE KEY-----\n..."
   GITHUB_APP_SLUG=forgelab-local
   GITHUB_APP_WEBHOOK_SECRET=your-random-webhook-secret
   # Optional for GitHub App user-to-server OAuth flow:
   GITHUB_APP_CLIENT_ID=...
   GITHUB_APP_CLIENT_SECRET=...
   ```

---

## 3. Restart Recovery, Push Webhooks & API Summary

### Restart-Recoverable Imports (`RecoverInterruptedImports`)
- Every import creates a durable row in `repository_imports` before background work starts.
- On backend startup (`backend/cmd/server/main.go`), `RepositoryImportService.RecoverInterruptedImports` transitions non-expired interrupted jobs (`pending`, `verifying`, `resolving`, `downloading`, `extracting`) or orphaned intermediate states back through bounded recovery execution or marks corrupt snapshots failed with clear remediation messages.
- Clients can list recent imports (`GET /api/integrations/github/imports`), cancel active imports (`POST .../cancel` — returning the updated `RepositoryImport` record), or retry failed/cancelled imports (`POST .../retry`).

### Push-to-Deploy Webhooks (`WebhookService`)
- Each GitHub-backed project can enable branch-filtered automatic deployments and generate an AES-256-GCM encrypted webhook secret (`GET` / `PATCH /api/projects/{id}/webhook`). The plaintext secret is returned **once** upon generation/rotation.
- `POST /api/webhooks/github?project_id=<uuid>` enforces a 1 MiB body limit, verifies `X-Hub-Signature-256` with constant-time HMAC-SHA256 comparison (`ErrInvalidWebhookSignature`), deduplicates `X-GitHub-Delivery` IDs (`webhook_deliveries` / bounded memory map) against replay attacks, ignores non-matching branches/deletions, and enqueues new deployments onto Redis (`DeploymentEnqueuer`).

| Endpoint | Method | Description | Auth Required |
|---|---|---|---|
| `/api/integrations/github` | GET | Connection state, auth type (`app`, `oauth`, `pat`), installations, and reconnect guidance | Guest or Auth |
| `/api/integrations/github/app/install` | GET | URL to install the GitHub App | Auth |
| `/api/integrations/github/app/callback` | GET | GitHub App installation callback | Public |
| `/api/integrations/github/connect` | GET / POST | Initiate GitHub OAuth `repo` authorization flow | Auth |
| `/api/integrations/github/callback` | GET | GitHub OAuth `repo` authorization callback | Public |
| `/api/integrations/github/imports` | GET / POST | List durable imports or initiate background import lifecycle (`repository`, `branch`, `root_dir`) | Guest or Auth |
| `/api/integrations/github/imports/{id}` | GET | Poll import progress, stage, error, discovery, and deployment plan | Guest or Auth |
| `/api/integrations/github/imports/{id}/cancel` | POST | Cancel in-flight import, cleanup workspace, and return updated import record | Guest or Auth |
| `/api/integrations/github/imports/{id}/retry` | POST | Retry failed or cancelled import | Guest or Auth |
| `/api/integrations/github/repositories` | GET | List authorized personal and organization repositories | Auth |
| `/api/integrations/github/token` | POST | Connect using Personal Access Token (AES-256-GCM encrypted at rest) | Auth |
| `/api/integrations/github/disconnect` | POST | Disconnect integration and purge stored credentials | Auth |
| `/api/projects/{id}/webhook` | GET / PATCH | View or update push-to-deploy branch/auto-deploy settings & rotate secret | Guest or Auth |
| `/api/webhooks/github` | POST | Public HMAC-SHA256 verified GitHub push webhook receiver | HMAC-SHA256 |
