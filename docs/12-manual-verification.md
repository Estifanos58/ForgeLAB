# ForgeLAB — Manual Physical Verification System

**Document Role:** Authoritative Quality & Verification Specification  
**Methodology:** Physical manual execution by human project owner  
**Current Verification Baseline:** NOT RECORDED  

---

## Related Documents

- [INSTRUCTION.md](../INSTRUCTION.md) — Operational memory & verification rules
- [docs/00-current-state.md](00-current-state.md) — Current state snapshot & matrix
- [docs/09-api-contract.md](09-api-contract.md) — API endpoints & error codes
- [docs/10-frontend-architecture.md](10-frontend-architecture.md) — UI pages & WebSocket hook
- [docs/11-development-environment.md](11-development-environment.md) — Environment startup & ports

---

## 1. Physical Verification Methodology

ForgeLAB enforces a strict engineering principle:

> **Automated unit tests and mock validations are non-authoritative artifacts. A feature is only complete when the project owner executes the real manual procedure in the live development environment and records the physical result.**

```text
IMPLEMENT  ──►  PHYSICAL MANUAL VERIFICATION  ──►  RECORD RESULT  ──►  PROCEED
```

Unit tests do not validate real Docker socket protocol communication, dynamic host port binding, PostgreSQL transactional concurrency, Redis list popping, browser WebSocket frame parsing, or end-to-end CSS/DOM rendering. Therefore, no feature may claim `PHYSICALLY VERIFIED` status based on automated test runs.

---

## 2. Master Acceptance Matrix

All entries currently reflect the unverified baseline (`NOT RECORDED`). As tests are performed, the project owner updates the verification record.

| Feature | Implementation Status | Physical Verification Status | Verification Date | Environment | Verified By | Result | Notes |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **User Registration** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests `POST /api/auth/register` & bcrypt hash |
| **User Login** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests `POST /api/auth/login` & JWT issuing |
| **Google OAuth Sign-In** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests `/api/auth/google`, consent, callback, & session |
| **GitHub OAuth Sign-In** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests `/api/auth/github`, consent, callback, & session |
| **OAuth Identity Account Linking** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests linking verified email to existing account |
| **OAuth Placeholder Handling** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests 503 / friendly error when unconfigured |
| **User Logout** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests `POST /api/auth/logout` & cookie clearing |
| **Token Refresh** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests atomic single-use rotation |
| **Authentication Failure Behavior** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests invalid/expired tokens (HTTP 401) |
| **Project Creation & Ownership** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests `POST /api/projects` user-scoped ownership |
| **Local Source Folder/Zip Upload** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests directory & archive upload to isolated workspace |
| **Zip Slip & Traversal Guard**| IMPLEMENTED | NOT RECORDED | — | — | — | — | Rejects archive path traversal & symlink escapes |
| **GitHub App Installation & Short-Lived Tokens** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests GitHub App installation flow, token minting, permissions, and OAuth/PAT fallbacks |
| **Public GitHub Ingestion (No Account Required)** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests public repository URL import without login or GitHub connection |
| **Durable Import Pipeline & Snapshot Reuse** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests background import lifecycle (`POST /api/integrations/github/imports`), polling, single snapshot reuse in deployment |
| **Heuristic Runtime Detection**| IMPLEMENTED | NOT RECORDED | — | — | — | — | Inspects package.json, python, go, dockerfile |
| **Automatic Build Strategy** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Builds non-Dockerfile apps via multi-stage generator |
| **Dynamic Internal Port Mapping**| IMPLEMENTED | NOT RECORDED | — | — | — | — | Maps container internal port (3000, 8000) to host port |
| **Readiness Strategies** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests HTTP, TCP, and None readiness check gating |
| **Local Source Path Validation** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Rejects system dirs & paths outside allowed roots |
| **Local Source Snapshotting** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Copies source to `data/builds/<id>` before build |
| **Redis Deployment Queue** | IMPLEMENTED | NOT RECORDED | — | — | — | — | `LPUSH` enqueue and worker `BRPOP` dequeue |
| **Docker Image Build** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Builds image from tar stream via Docker SDK |
| **Deployment State Transitions** | IMPLEMENTED | NOT RECORDED | — | — | — | — | `QUEUED`→`CLONING`→`BUILDING`→`STARTING`→`HEALTH` |
| **Health-Check Deployment Gate** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Polling gate before promotion supporting HTTP, TCP, and None |
| **Live Build Log Streaming** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Docker build output streams to WebSocket |
| **Live Runtime Log Streaming** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Container runtime logs stream to WebSocket |
| **Application Stop** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Stops running container (`POST /stop`) |
| **Application Start** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Starts stopped container (`POST /start`) |
| **Application Restart** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Restarts active container (`POST /restart`) |
| **Rollback Execution** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Deploys new release using prior known-good tag |
| **Failed Deployment Preservation** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Broken build/health-check leaves old container running |
| **Invalid Deployment Access** | IMPLEMENTED | NOT RECORDED | — | — | — | — | User B cannot access User A's deployment logs |
| **Environment Variable Creation** | IMPLEMENTED | NOT RECORDED | — | — | — | — | AES-256-GCM encryption at rest |
| **Secret Masking & Redaction** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Masked in API (`••••`), redacted in logs (`[REDACTED]`) |
| **WebSocket Authorization** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Rejects unowned resource subscriptions |
| **WebSocket Deployment Isolation** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Channel A logs never appear in Channel B |
| **Frontend Historical Logs** | IMPLEMENTED | NOT RECORDED | — | — | — | — | REST log fetch on deployment selection |
| **Frontend Live Logs** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Live terminal log auto-scrolling via WebSocket |
| **Frontend Deployment Switching** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Switching deployment updates channel & log viewer |
| **Continuous Runtime Log Collection** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Runtime collector continues after deployment returns, stops on container stop |
| **Backend Restart Collector Recovery** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Daemon reconciliation restores collectors for running containers on backend startup |
| **Log UI Phase Filters & Service Scoping** | IMPLEMENTED | NOT RECORDED | — | — | — | — | All Logs, Deployment, Production filters with count badges and All Services view |
| **Historical Log Retrieval & Replay** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Recent logs query subquery, cursor pagination, and WebSocket sequence replay |
| **Hardened Docker Log Parsing** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Bounded 32KB streaming, oversized frame discard (>8MB) without stream desync, 64KB line truncation |
| **Race-Safe Collector Concurrency** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Instance-scoped registration prevents older collectors from deregistering active replacements |
| **Log Collector Observability** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Exposes active collectors, log activity timestamps, collection/persistence errors at `/health/logs` and `/ready` |
| **Configurable Log Retention** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Time-based (`FORGELAB_LOG_RETENTION_DAYS`) and volume-based (`FORGELAB_LOG_MAX_PER_DEPLOYMENT`) background pruning |
| **WebSocket Ticket Authentication** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Rejects `?token=...`, issues 60s tickets via `POST /api/ws/ticket`, single-use redemption |
| **WebSocket Origin Policy Enforcement** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Validates `Origin` header against `CORS_ALLOWED_ORIGINS` & `FRONTEND_URL` or same-origin host |
| **Diagnostics Access Control** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Redacts queue & log collectors on `/ready` for external callers; restricts `/health/queue` & `/health/logs` |
| **Host Port Conflict Retries** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Automatic conflict detection, port manager re-allocation, and 3x retry on daemon collision |
| **Build Reproducibility Fingerprinting** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Verifies local directory content fingerprint against pinned revision; fails closed on drift |
| **Local Agent Identity Verification Probe** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Probes candidate agent endpoint before sending bearer credentials; short-lived 15m TTL tokens; explicit & workspace-delete revocation |
| **Isolated Guest Browser Sessions** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Isolated cookie/header session per browser guest (`guest-<uuid>@forgelab.local`); safe loopback CLI fallback (`GuestUserID`) |
| **Deployment Failure Safety Invariant** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Failed deployments or enqueue errors never destroy healthy running release; startup reconciliation authoritativeness checked on `/ready` |
| **Live-Preview URL Suppression** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Stopped, failed, or unreachable services strictly suppress live preview URLs across services, deployments, and projects |
| **Service Connection Diagnostics & Topology** | IMPLEMENTED | NOT RECORDED | — | — | — | — | `GET /api/projects/{id}/topology` & frontend tab: DAG execution tiers, endpoints, network badges, and actionable connection diagnostics |
| **Selective Prisma Staging & Lifecycle Builds** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Automated Dockerfile staging copies `prisma/` and config before dependency install; scopes placeholder DATABASE_URL to RUN; prunes `.next/cache` in builder |
| **Node.js Preflight Validation** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Validates package manifests, build scripts, and presence of required Prisma schema files before starting Docker build with actionable diagnostics |
| **Safe Shell Command Serialization** | IMPLEMENTED | NOT RECORDED | — | — | — | — | JSON array serialization for Dockerfile `CMD ["sh", "-c", ...]` escaping quotes, backslashes, and shell operators safely |
| **Service Exposure Configuration & Isolation** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Explicit `public_exposed` selection in UI/API; database and infrastructure services default to internal; volume traversal validation error propagation |
| **WebSocket Compatibility & Deprecation Path** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Validates `?token=` query fallback with scrubbed deprecation warning while preferring single-use tickets (`POST /api/ws/ticket`) and HttpOnly cookies |

---

## 3. Detailed Manual Verification Procedures

### Execution Environment Requirement

Before executing any procedure, note the distinction between development environments documented in [docs/11-development-environment.md](11-development-environment.md):

- **Environment A (Host-Run Go Backend — Required for Local Repository Verification):**
  PostgreSQL and Redis run via `docker compose up -d postgres redis`. Migrations are applied via `go run cmd/migrate/main.go up`. The Go backend runs directly on the host (`cd backend && go run cmd/server/main.go`) and frontend runs via `npm run dev`.
  *Prerequisite Rule:* You must run the Go backend on the host for local-repository verification, unless the backend container has been explicitly given access to the source directory through a documented host bind mount. The containerized backend cannot access host filesystem paths like `C:\dev\testapp` or `/home/user/app`.
- **Environment B (Current Docker Compose Stack):**
  `docker compose up --build` runs the backend inside a container without arbitrary host source directory mounts. Migrations are executed via `docker compose exec backend /app/forgelab-migrate up`. As documented in [docs/14-known-limitations.md](14-known-limitations.md), containerized frontend rewrites and host path inaccessibility currently prevent end-to-end local repository verification in Environment B.

---

### Procedure 1: Authentication & Token Lifecycle

#### Purpose
Verify user registration, login, authenticated requests, cookie/token handling, logout, and rejection of invalid credentials.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- Stack is running: Environment A (Host backend & host frontend with `docker compose up -d postgres redis`) or Environment B (`docker compose up --build`)
- Database migrations applied (`go run cmd/migrate/main.go up` or `docker compose exec backend /app/forgelab-migrate up`)
- Browser open at `http://localhost:3000`

#### Test Procedure
1. Navigate to `http://localhost:3000/register`.
2. Register a new user with email `test-user-1@example.com` and password `Password123!`.
3. Confirm redirection to `http://localhost:3000/dashboard`.
4. Open Browser DevTools → Application → Cookies (`http://localhost:3000`); verify `forgelab_access_token` and `forgelab_refresh_token` are set as `HttpOnly`, `SameSite=Lax` cookies. Verify that no tokens are stored in `localStorage` or `sessionStorage`.
5. In DevTools → Network, inspect request to `/api/auth/me`; verify status is `200 OK` and returns user object (browser automatically includes cookies with requests).
6. **Refresh Token Rotation Check:** Send `POST /api/auth/refresh` via DevTools console or API client; verify status is `200 OK`, a new access token and rotated refresh token are set in cookies, and replaying the old refresh token fails with `401 Unauthorized`.
7. Click the "Logout" button on the dashboard.
8. Verify redirection to `http://localhost:3000/login`, and verify `forgelab_access_token` and `forgelab_refresh_token` cookies are cleared (expired).
9. Attempt to navigate directly to `http://localhost:3000/dashboard`; verify client immediately redirects to `/login`.
10. In `/login`, attempt login with wrong password `WrongPassword!`; verify error banner displays "invalid email or password".
11. Log in with correct password `Password123!`; verify dashboard loads successfully.

#### Expected Result
- Clean registration and login transitions.
- Auth tokens strictly managed via secure HttpOnly cookies (`forgelab_access_token`, `forgelab_refresh_token`), zero tokens in localStorage/sessionStorage.
- Atomic, single-use refresh token rotation with replay detection.
- Direct protected page access redirects unauthenticated users to `/login`.

#### Failure Conditions
- Registration returns 500 or fails to set HttpOnly cookies.
- Invalid credentials log user in.
- Tokens stored in client-accessible localStorage/sessionStorage.
- Protected routes allow unauthenticated rendering.

#### What to Inspect if it Fails
- Backend logs: `docker compose logs backend` (or host backend console output)
- PostgreSQL `users` table: `docker compose exec postgres psql -U forgelab -c "SELECT id, email FROM users;"`

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 1b: Multi-Provider OAuth Authentication (Google & GitHub)

#### Purpose
Verify Google and GitHub OAuth 2.0 authorization code flows, CSRF state protection in Redis, session cookie issuance, account creation/linking in `auth_identities`, and controlled error responses when placeholder credentials are used.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED` (Requires real external Google Cloud Console & GitHub OAuth App credentials for live provider consent).

#### Prerequisites
- Stack is running via `docker compose up --build`
- Database migrations applied (`000003_add_auth_identities.up.sql`)
- Browser open at `http://localhost:3000/login`

#### Test Procedure (A: Unconfigured / Placeholder Credentials)
1. On `http://localhost:3000/login`, inspect the "Continue with Google" and "Continue with GitHub" buttons.
2. Click "Continue with Google" when `GOOGLE_CLIENT_ID` is empty or placeholder.
3. Verify that the UI displays a clean, user-friendly error banner (`Google OAuth is not configured on the backend`) rather than crashing or throwing an unhandled exception.
4. Click "Continue with GitHub" when `GITHUB_CLIENT_ID` is empty or placeholder.
5. Verify that the UI displays a clean error banner (`GitHub OAuth is not configured on the backend`).

#### Test Procedure (B: Configured Provider Flow — with real credentials)
1. Configure valid `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`, and `GOOGLE_REDIRECT_URL=http://localhost:3000/api/auth/google/callback` in `.env`.
2. Configure valid `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`, and `GITHUB_REDIRECT_URL=http://localhost:3000/api/auth/github/callback` in `.env`.
3. Restart backend container: `docker compose restart backend`.
4. Click "Continue with Google". Verify redirection to Google's consent screen (`accounts.google.com`).
5. Grant consent. Verify Google redirects back to `http://localhost:3000/api/auth/google/callback?code=...&state=...`.
6. Verify backend sets `forgelab_access_token` and `forgelab_refresh_token` as HttpOnly cookies and redirects to `/dashboard`.
7. Verify `/api/auth/me` responds with the authenticated user profile.
8. Inspect PostgreSQL `auth_identities` table:
   ```bash
   docker compose exec postgres psql -U forgelab -c "SELECT * FROM auth_identities;"
   ```
   Verify `provider = 'google'`, `provider_subject` matches Google's sub ID, and `user_id` matches the user.
9. Log out. Repeat the flow for "Continue with GitHub". Verify GitHub identity is created and linked.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 2: Project Creation & Multi-Source Import (Local Computer & GitHub)

#### Purpose
Verify that users can import projects from both their local computer (direct file/archive upload) and authorized GitHub repositories, verify source isolation and security safeguards, verify heuristic runtime detection, and verify server-side ownership isolation.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- Live ForgeLAB stack running via Docker Compose (`docker compose up --build`).
- User 1 registered (`user1@example.com`).
- User 2 registered (`user2@example.com`) in an Incognito window.
- GitHub OAuth application configured in backend environment with `repo,read:user` redirect URL.

#### Test Procedure — Part A: Direct Local-Directory Deployment (Primary Computer Workflow)
1. As User 1 on `http://localhost:3000/dashboard`, click "+ Create Project".
2. Select the **"Import from Computer"** tab. Verify the sub-tabs display:
   `[ Existing Directory ]` (selected by default) and `[ Upload Archive ]`.
3. In the **"Local Directory Path"** input, enter an existing directory on your host machine (e.g. `C:\Users\username\Projects\my-app` or `/host-projects/my-app` if running in Docker with mount configured).
4. Click **"Validate Directory"** (or press Enter):
   - Verify network tab shows a single fast `POST /api/sources/local/validate` request.
   - Verify NO large file transfers occur and NO `FormData` of files is created.
   - Verify UI displays the "Validating directory..." progress state.
5. Verify validated state card:
   - Green badge displays "Directory Validated" with "Direct Build" badge.
   - Card displays canonical repository path, project name, detected framework, runtime, suggested port, total files count, and size.
   - Information callout confirms: "⚡ Direct local deployment active: Docker build context will be streamed directly from disk with .dockerignore filtering. No intermediate copies or in-memory tar buffers."
6. Click **"Continue to Configuration"** to enter Step 2 ("Configure Application").
7. Verify configuration fields:
   - Top banner displays host path and file count with "Direct Build" indicator.
   - Project name is pre-populated.
   - Application Port, build command, and start command are pre-populated.
8. Click **"Create & Deploy Project"**:
   - Verify project is created in PostgreSQL with `source_type: "local_directory"` and `repository_path` holding the host path.
   - Verify deployment engine logs in terminal viewer show:
     ```text
     SOURCE Using local directory: <path>
     SOURCE Validated local source directory.
     SOURCE Using direct filesystem build mode.
     BUILD Applying .dockerignore...
     BUILD Streaming Docker build context...
     BUILD Docker build started.
     ```
   - Verify NO complete copy was created in `data/builds/<deployment-id>`.
   - Verify container builds, starts, passes health check, and enters `RUNNING`.

#### Test Procedure — Part B: Local Archive Upload Fallback
1. In "+ Create Project" → "Import from Computer", switch to **"[ Upload Archive ]"**.
2. Click **"Choose Archive File"** and select a `.zip`, `.tar.gz`, or `.tgz` file.
3. Observe honest upload progress:
   - Network progress bar moves from 0% to 100% displaying transferred bytes, speed, and ETA.
   - Server extracts archive into isolated source workspace (`data/sources/<source-id>`) and polls `GET /api/sources/{id}`.
4. Verify transition to "Archive Imported Successfully" with detected framework and port.
5. Create project and verify it deploys from the uploaded source workspace with `source_type: "local_upload"`.

#### Test Procedure — Part C: Path Boundary & Security Rejection
1. In "Existing Directory" mode, enter a path outside `FORGELAB_ALLOWED_SOURCE_ROOTS` (or a restricted system path such as `C:\Windows` or `/etc`).
2. Click "Validate Directory".
3. Verify an error alert is displayed explaining that the path is restricted or outside configured roots.
4. Verify project creation is blocked.
5. Prepare a test zip archive containing an entry with relative traversal (e.g. `../../etc/evil.txt`).
6. Make a direct API upload request:
   ```bash
   # Session cookies from browser or explicit auth header:
   curl -i -X POST http://localhost:8080/api/sources/upload \
     -b "forgelab_access_token=<access-token>" \
     -F "archive=@traversal-test.zip"
   ```
7. Verify response status: `422 Unprocessable Entity` or `400 Bad Request` with message `path traversal detected in source archive`.
8. Inspect `data/sources/` on the server to verify the directory was purged immediately.

#### Test Procedure — Part D: GitHub Repository Authorization & Import
1. In ForgeLAB, click "+ Create Project" and select the **"Import from GitHub"** tab.
2. If GitHub repository permissions have not yet been granted:
   - Verify an informative banner explains repository permission requirements.
   - Verify there is NO text box asking the user to paste an unauthenticated git URL.
   - Click **"Authorize GitHub Repositories"**.
3. Verify redirection to GitHub's authorization consent screen requesting `repo,read:user` permissions.
4. Grant authorization. Verify GitHub redirects back to ForgeLAB (`/dashboard?github_connected=true`).
5. Open "+ Create Project" → "Import from GitHub":
   - Verify status shows `Connected as @<username>` with a green indicator.
   - Verify searchable repository picker appears listing your public and private repositories.
6. Type in the search box to filter repositories. Click on a target repository.
7. Verify branch selector populates with branches from GitHub (defaulting to default branch).
8. Click **"Analyze & Configure"**:
   - Backend calls `/api/integrations/github/repositories/:owner/:repo/detect`.
   - Advances to Step 2 with detected runtime, port, and commands populated.
9. Click **"Create & Deploy Project"**.
10. Verify project is created in PostgreSQL with `source_type: "github"` and `source_reference: "<owner>/<repo>"`.

#### Test Procedure — Part E: Local Agent Mode Connector
1. Select the **"Local Agent"** import option.
2. Verify pairing token is generated and encrypted at rest using AES-256-GCM.
3. Start the local agent CLI (`forgelab-agent connect --token <token>`).
4. Verify mutual TLS or token authentication establishes the streaming reverse tunnel.
5. Verify source discovery and deployment build stream directly from the agent without persisting plain credentials.

#### Test Procedure — Part F: Immutable Source Snapshot & Plan Invalidation Verification
1. Create a project and perform source discovery to generate a reviewed deployment plan:
   - Verify `GET /api/projects/:id/discovery` returns the service topology, detected commands, and authoritative `content_fingerprint`.
   - Verify the deployment plan's `source_revision` matches this fingerprint.
2. **Modify Source After Review:** Before triggering deployment, modify or add a file in the project's source directory (e.g. edit `package.json` or add `test.txt`).
3. Trigger deployment: `POST /api/deployments` with `{ "project_id": "<id>" }`.
4. Observe the response:
   - Backend calculates the new content fingerprint, detects mismatch (`fp != plan.SourceRevision`).
   - Backend clears the stale plan (`deployment_plan = '{}'::jsonb`).
   - Backend rejects the deployment with HTTP 409 Conflict: `source files have changed since deployment plan was reviewed; please re-run discovery and review the updated deployment plan`.
5. Verify that deployment cannot build content different from the reviewed revision.
6. Re-run discovery, review the updated plan, and trigger deployment: verify deployment succeeds.

#### Test Procedure — Part G: Hardened Fingerprinting & External Symlink Protection
1. Inside an approved local repository, create a symlink pointing outside the source boundary:
   ```bash
   ln -s /etc/passwd ./leak.txt    # Linux/macOS
   # On Windows: mklink leak.txt C:\Windows\System32\drivers\etc\hosts
   ```
2. Trigger directory validation or fingerprint calculation (`POST /api/sources/local/validate` or discovery).
3. Verify that ForgeLAB rejects the symlink or canonicalizes the path, detects that it resolves outside the root, and returns an error without following or reading external files.
4. Verify directory traversal and size limits safely abort processing on maliciously nested directories or symlink loops.

#### Test Procedure — Part H: Cross-User Ownership Isolation
1. Copy the project UUID created by User 1.
2. In User 2's Incognito browser, log in and verify User 1's project is not shown on the dashboard.
3. Attempt to fetch User 1's project: `GET /api/projects/<user-1-project-uuid>`.
4. Verify response is `403 Forbidden` or `404 Not Found`.
5. In User 2's session, query `GET /api/integrations/github/repositories`.
6. Verify User 2 does NOT see User 1's GitHub repositories or credentials.

#### Expected Result
- All source import types (`local_directory`, `local_upload`, `github`, `local_agent`) function reliably with isolated boundaries.
- Stale or tampered deployment plans are invalidated immediately upon content modification.
- External symlinks cannot escape the approved source root or leak host files.
- Path traversal and Zip Slip attacks are blocked.
- User data and repository grants remain strictly isolated per user.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 3: Dockerfile Application Deployment

#### Purpose
Verify deployment of a project containing a Dockerfile, verifying image build, container creation, dynamic host port binding, health checking, WebSocket streaming, and promotion.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Test Procedure
1. Create a project containing a valid `Dockerfile` (via Local Upload or GitHub Import).
2. Ensure Build Strategy is set to **"Dockerfile"**.
3. Navigate to the project page `/projects/<id>` in ForgeLAB.
4. Click **"🚀 Deploy Release"**.
5. Observe:
   - State machine transitions: `queued` → `cloning` → `building` → `starting` → `health_checking` → `running`.
   - Build logs stream in real-time over WebSocket.
   - Application container is started and bound to a dynamic host port in `10000–60000`.
   - Health check polls the endpoint and promotes release to `running`.
6. Click the live port link and verify the application responds.
7. Test lifecycle buttons: **Stop**, **Start**, and **Restart**.

#### Expected Result
- Complete Dockerfile build and deployment pipeline succeeds.
- Terminal logs stream smoothly without duplication.
- Application serves traffic on the allocated host port.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 3b: Non-Dockerfile Application Deployment (Automatic Build Strategy)

#### Purpose
Verify deployment of an application without a Dockerfile (e.g. Node.js/Next.js, Python FastAPI/Flask, or Go) using ForgeLAB's automatic heuristic detection, automatic multi-stage build generation, and dynamic internal port mapping.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Test Procedure
1. Prepare a minimal non-Dockerfile application:
   - **Example (Node.js/Express):** `package.json` with `"start": "node index.js"` and `index.js` listening on `process.env.PORT || 3000`.
   - **Example (Python/FastAPI):** `requirements.txt` with `fastapi`, `uvicorn`, and `main.py` listening on port 8000.
2. In ForgeLAB, import the application via Computer Upload or GitHub Import.
3. Observe Step 2 ("Configure Application"):
   - Verify Detection correctly identifies runtime (e.g. `nodejs` or `python`).
   - Verify Build Strategy defaults to **"Automatic"**.
   - Verify Application Port defaults to the detected port (e.g. 3000 or 8000).
4. Click **"Create & Deploy Project"**.
5. Observe the deployment logs:
   - Notice the deployment engine generates a multi-stage container build tailored for the detected runtime.
   - Logs stream the build and package installation steps.
   - Container starts with `PORT=<internal_port>` environment variable injected.
   - ForgeLAB binds the container's internal port to the dynamically allocated host port (e.g. host port 10005 → container port 3000).
6. Verify deployment reaches `running`.
7. Click the live host port URL and verify the application responds successfully.

#### Expected Result
- Applications without Dockerfiles build and deploy automatically.
- Internal ports (e.g. 3000, 8000) are correctly mapped to external dynamic host ports.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 3c: Generalized Health & Readiness Strategies

#### Purpose
Verify that applications without `/health` endpoints can deploy successfully using alternative readiness check strategies (`tcp` or `none`).

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Test Procedure
1. Create a project whose application serves on root `/` or does not expose a `/health` endpoint.
2. In project configuration, set:
   - Health Strategy: **"TCP"** (or **"None"**).
3. Click **"🚀 Deploy Release"**.
4. Observe deployment transition:
   - If TCP: Worker polls TCP socket connectivity on the allocated port.
   - Once TCP connection succeeds, release is immediately promoted to `running`.
   - If None: Container start immediately promotes release to `running`.
5. Verify deployment is NOT marked failed simply because `/health` was absent.

#### Expected Result
- Applications without `/health` pass readiness gating according to the selected strategy.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 4: Failed Deployment & Safety Invariant Preservation

#### Purpose
Verify that failing deployments (broken build, runtime crash, or failed readiness check) transition to `failed` without terminating or displacing the active healthy deployment.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- Project has an active, running deployment serving traffic (Deployment #1).

#### Test Procedure
1. **Broken Build Test:**
   - Introduce a syntax error or failing build command in project settings (e.g. Build Command: `exit 1`).
   - Click **"Deploy Release"** (Deployment #2).
   - Observe build fails in logs. Deployment #2 transitions to `failed`.
   - Verify project status remains `running`.
   - Verify Deployment #1's container remains running and continues serving requests on its port.
2. **Broken Runtime / Readiness Failure Test:**
   - Configure a start command that fails immediately or an unreachable HTTP health check path (`/non-existent-endpoint`).
   - Click **"Deploy Release"** (Deployment #3).
   - Container starts, but health check retries exhaust (10 attempts).
   - Deployment #3 transitions to `failed` and its broken container is cleaned up.
   - Verify Deployment #1 remains `running` and active release link is untouched.

#### Expected Result
- **Deployment Safety Invariant holds:** Active healthy deployments are never interrupted by failed releases.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 5: Rollback Semantics

#### Purpose
Verify that rollback creates a new deployment using the prior known-good deployment's image, runs through health checking, and only promotes upon success.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- **Execution Environment:** Run the Go backend on the host (**Environment A**) for local-repository verification, unless the backend container has been explicitly given access to the source directory through a documented host bind mount.
- Deployment #1 was successful (Release A).
- Deployment #2 was deployed successfully with a different version (Release B).
- Release B is currently active.

#### Test Procedure
1. Verify Release B is `running` and serving traffic.
2. In the ForgeLAB project view, click "↺ Rollback".
3. Confirm the browser dialog ("Rollback to previous known-good deployment?").
4. Inspect the resulting deployment:
   - A new deployment record (Deployment #3) is created.
   - Deployment #3 uses Deployment #1's Docker image tag.
   - Deployment #3 starts a container and passes health check.
   - Deployment #3 transitions to `running`.
   - Old container for Deployment #2 is cleanly stopped.
5. Access the live port; verify Release A's application code is active.

#### Expected Result
- Rollback operates as a standard forward deployment of a previous image.
- State machine rules and safety invariants are fully respected.

#### Failure Conditions
- Rollback attempts to revert state in-place without creating a durable deployment record.
- Rollback fails if previous container is already stopped.

#### What to Inspect if it Fails
- Rollback handler: `backend/internal/handlers/project_handler.go:340-413`
- Deployment service: `backend/internal/services/deployment_service.go:GetPreviousSuccessfulDeployment`

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 6: WebSocket Isolation & Cross-Project Privacy

#### Purpose
Verify that WebSocket channels are strictly isolated by UUID and that logs or status changes from Project A never leak to Project B.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- **Execution Environment:** Run the Go backend on the host (**Environment A**), unless the backend container has been explicitly given access to source directories through a documented host bind mount.
- Two separate projects created: Project A (`uuid-A`) and Project B (`uuid-B`).
- Two browser windows opened side-by-side:
  - Window 1 viewing Project A (`/projects/uuid-A`)
  - Window 2 viewing Project B (`/projects/uuid-B`)

#### Test Procedure
1. Trigger a deployment on Project A in Window 1.
2. Observe Window 1: Live terminal displays build and runtime logs for Project A.
3. Observe Window 2: Verify that **zero** log lines, status changes, or deployment events from Project A appear in Window 2's terminal.
4. Trigger a deployment on Project B in Window 2.
5. Verify Window 2 receives Project B logs and Window 1 receives no cross-project noise.
6. In Window 1's browser DevTools Console, execute an unauthorized subscription (cookies are automatically passed via the same-origin Next.js WebSocket proxy, or using a standalone WebSocket client with query parameter token fallback):
   ```javascript
   // In browser console on http://localhost:3000 (authenticated session cookies forwarded):
   const ws = new WebSocket('ws://localhost:3000/api/ws');
   // For direct backend port testing:
   // const ws = new WebSocket('ws://localhost:8080/api/ws?token=<accessToken>');
   ws.onopen = () => ws.send(JSON.stringify({ type: 'subscribe', channel: 'project:00000000-0000-0000-0000-000000000000' }));
   ws.onmessage = (e) => console.log('WS msg:', JSON.parse(e.data));
   ```
7. Verify the server returns:
   ```json
   { "type": "error", "code": "UNAUTHORIZED", "message": "access denied to this resource" }
   ```

#### Expected Result
- Complete event isolation between deployment channels.
- Unauthorized subscription attempts rejected by server-side ownership checks.

#### Failure Conditions
- Broadcast messages leak across different projects or deployments.
- Server accepts subscriptions to unowned project UUIDs.

#### What to Inspect if it Fails
- `backend/internal/websocket/hub.go:handleSubscribe`
- Channel mapping in WebSocket Hub.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 7: Secret Encryption & Streaming Log Redaction

#### Purpose
Verify that environment variables marked as secrets are encrypted in the database, masked in the API, and automatically redacted from live and historical logs.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- **Execution Environment:** Run the Go backend on the host (**Environment A**) for local-repository verification, unless the backend container has been explicitly given access to the source directory through a documented host bind mount.
- Project created and ready for deployment.

#### Test Procedure
1. In the project details page, locate the "Secrets & Env Vars" panel.
2. Add a secret:
   - Key: `SUPER_SECRET_KEY`
   - Value: `super_secret_token_12345`
   - Check the box: "Encrypted Secret (Masked)"
3. Click "Add". Verify the variable appears in the list as `SUPER_SECRET_KEY = ••••••••`.
4. Inspect PostgreSQL directly:
   ```bash
   docker compose exec postgres psql -U forgelab -c "SELECT key, encrypted_value, is_secret FROM environment_variables;"
   ```
   Verify `encrypted_value` is stored as binary ciphertext and does not contain `super_secret_token_12345` in plaintext.
5. In the test application's Dockerfile or source script, add a line that prints the secret:
   ```dockerfile
   RUN echo "Bootstrapping application with secret: $SUPER_SECRET_KEY"
   ```
6. Trigger a deployment.
7. Observe live terminal logs during build:
   - Look for the bootstrap log line.
   - Verify it appears as: `Bootstrapping application with secret: [REDACTED]`.
8. Refresh the browser page to fetch historical logs via REST (`/api/projects/<id>/deployments/<id>/logs`).
9. Verify the historical log in the terminal also displays `[REDACTED]`.

#### Expected Result
- Secrets encrypted at rest with AES-256-GCM.
- Secrets never exposed in plaintext in the API, UI, or build/runtime logs.

#### Failure Conditions
- Plaintext secret appears in PostgreSQL `environment_variables` table.
- Plaintext secret appears in WebSocket stream or `deployment_logs` table.

#### What to Inspect if it Fails
- `backend/internal/logging/redactor.go`
- `backend/internal/crypto/encryptor.go`

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 8: Host Path Security Validation

#### Purpose
Verify that `PathValidator` prevents path traversal, rejects restricted system directories, and enforces `FORGELAB_ALLOWED_SOURCE_ROOTS`.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- **Execution Environment:** Run the Go backend on the host (**Environment A**).
- **Environment Configuration:** Set `FORGELAB_ALLOWED_SOURCE_ROOTS` in `.env` and run the host Go backend. Note that `docker-compose.yml` does **not** inject `FORGELAB_ALLOWED_SOURCE_ROOTS` from host `.env` into the backend container; this boundary enforcement test requires running the backend on the host.
- User logged in, at project creation modal.

#### Test Procedure
1. Attempt to create a project with system directories:
   - Linux: `/etc`, `/var`, `/usr`
   - Windows: `C:\Windows`, `C:\Program Files`
2. Verify the API rejects the request with HTTP 400 and an error indicating system directory access is forbidden.
3. Attempt path traversal in repository path: `../../../../etc` or `C:\Users\..\Windows`.
4. Verify canonicalization resolves the path and blocks it.
5. Set `FORGELAB_ALLOWED_SOURCE_ROOTS=/home/user/allowed` (or `C:\allowed`).
6. Attempt to import `/home/user/other` (or `C:\other`).
7. Verify the request is rejected with `repository path is outside allowed source root directory`.

#### Expected Result
- Host path validation blocks arbitrary filesystem reads and boundary escapes.

#### Failure Conditions
- System directories or unauthorized paths are accepted.

#### What to Inspect if it Fails
- `backend/internal/security/path_validator.go`

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 9: Deploy Frontend Only

#### Purpose
Verify that deploying a frontend service independently triggers a `ServiceDeployment` pipeline without triggering or stopping the backend service, updates `services.current_service_deployment_id`, increments the frontend `deploy_number`, updates `services.status`, and leaves `projects.current_deployment_id` intact.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- A multi-service project created in ForgeLAB with at least two services (Frontend and Backend).
- Both services are initially in `inactive` or `running` state.

#### Test Procedure
1. Navigate to the project page (`/projects/<id>`).
2. Locate the **Frontend** service card in the Services section.
3. Click the **Deploy Service** button on the Frontend card only.
4. Observe the UI:
   - Frontend card transitions to `Deploying` → `Building` → `Health Checking` → `Running`.
   - Backend service card remains unchanged (in its previous state, e.g. `running` or `inactive`).
   - The Deploy button on the Backend card remains enabled and clickable.
5. In PostgreSQL, run:
   ```sql
   SELECT id, service_id, deploy_number, status, deployment_id FROM service_deployments WHERE service_id = '<frontend_service_id>' ORDER BY deploy_number DESC LIMIT 1;
   SELECT current_deployment_id, status FROM projects WHERE id = '<project_id>';
   SELECT id, current_service_deployment_id, status FROM services WHERE id = '<frontend_service_id>';
   ```
6. Verify:
   - `service_deployments.deployment_id` is NULL (indicating an independent service deployment).
   - `services.current_service_deployment_id` points to the new `service_deployments.id`.
   - `projects.current_deployment_id` was NOT replaced.
   - `projects.status` reflects the composite state (`running` if all are running, or `partially_running`).

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 10: Deploy Backend Only

#### Purpose
Verify that deploying a backend service independently triggers an isolated `ServiceDeployment` pipeline, allocates an internal mesh endpoint or host port, generates peer discovery environment variables (`BACKEND_URL`, `API_URL`), updates `services.current_service_deployment_id`, and leaves the frontend container running and undisturbed.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- Multi-service project with frontend and backend services.
- Frontend is running on an allocated port.

#### Test Procedure
1. Note the running frontend container ID and port using `docker ps`.
2. On the project page, click **Deploy Service** on the **Backend** service card only.
3. Observe live logs in the Terminal viewer scoped to the backend service.
4. Verify backend image builds, container starts on network `forgelab-net-<project-id>`, and health checks complete.
5. After backend is marked `Running`:
   - Inspect `docker ps` to verify the frontend container was NEVER stopped or restarted.
   - Send an HTTP request to the frontend to ensure it remains responsive throughout.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 11: Deploy Frontend and Backend Simultaneously (Concurrent Execution)

#### Purpose
Verify that frontend and backend service deployments can be initiated simultaneously without encountering lock contention or `409 Conflict` (confirming removal of project-wide deployment lock and addition of per-service locks).

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- Multi-service project with frontend and backend.

#### Test Procedure
1. Open the project page.
2. In rapid succession (or across two browser tabs / curl requests simultaneously), trigger:
   - Click **Deploy Service** on Frontend (`POST /api/projects/<id>/services/<frontend_id>/deploy`)
   - Click **Deploy Service** on Backend (`POST /api/projects/<id>/services/<backend_id>/deploy`)
3. Verify both requests return HTTP `201 Created` with their respective `ServiceDeployment` records.
4. Verify neither returns HTTP `409 Conflict`.
5. Observe both cards displaying active progress states (`Building` / `Starting`) concurrently.
6. Verify in Redis and logs that the worker pool processes both jobs concurrently in parallel goroutines without deadlock.
7. Both services should transition to `Running` once their respective health checks pass.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 12: Independent Health Check Failure (Frontend Fails While Backend Remains Healthy)

#### Purpose
Verify that a failure in a new frontend service deployment does not stop, crash, or replace the currently running healthy frontend container, and has zero negative effect on the running backend service.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- Both frontend and backend are currently `Running` and healthy.
- Note the active frontend container ID via `docker ps`.

#### Test Procedure
1. Update frontend service configuration or source with an intentionally failing health check (e.g. invalid start command or non-existent health check path `/failing-endpoint`).
2. Click **Deploy Service** on the Frontend service card.
3. Observe the build and startup:
   - Container starts and health checks fail after configured retries.
   - New failed container is stopped and removed (`ContainerRemove`).
   - Any allocated host port for the failed container is released.
4. Verify Safety Invariant enforcement:
   - The previously healthy frontend container is NOT stopped or destroyed.
   - `services.container_id` and `services.status` for frontend remain `running`.
   - The backend service container continues running with zero downtime or disruption.
   - Project status reflects `running` or `partially_running` without invalid enum state transitions.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 13: Independent Service Rollback (Roll Back Frontend Without Affecting Backend)

#### Purpose
Verify that clicking Rollback on an individual service rolls back only that specific service to its previous successful image tag, increments that service's `deploy_number`, promotes the rolled-back container, and does not alter or disrupt other services.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- Frontend service has had at least two deployments:
  - Deployment #1: Healthy (`Running`)
  - Deployment #2: Modified or deployed with a change.
- Backend service is running.

#### Test Procedure
1. On the Frontend service card, click **Rollback** (or expand **History** and trigger rollback to Deployment #1).
2. Verify API calls `POST /api/projects/<id>/services/<frontend_id>/rollback`.
3. Verify a new `ServiceDeployment` record (Deployment #3) is created using the image tag from Deployment #1.
4. Verify Deployment #3 is queued, started, health checked, and promoted.
5. Verify the Frontend service card updates to `Running` on Deployment #3.
6. Verify the Backend service remained running throughout the rollback with unchanged container ID and port.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 14: Rollback Fail-Closed Verification When Immutable Image is Missing

#### Purpose
Verify that when rolling back an individual service or project, if the prior known-good Docker image or digest is not available in the local Docker daemon, the deployment fails closed with status `failed` and does NOT fall back to rebuilding from the current source workspace.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- A service with at least two deployments where Deployment #1 was successful (`running`).
- Identify the Docker image tag of Deployment #1: `forgelab/<project_id>/<service_name>:1`.

#### Test Procedure
1. Stop any active containers from Deployment #1 and remove the image from the Docker daemon:
   ```bash
   docker rmi -f forgelab/<project_id>/<service_name>:1
   ```
2. In the ForgeLAB UI, click **Rollback** on the service card (or execute `POST /api/projects/<id>/services/<service_id>/rollback`).
3. Monitor the deployment progress via WebSocket or database logs:
   ```sql
   SELECT id, status, execution_mode, failure_reason FROM service_deployments WHERE service_id = '<service_id>' ORDER BY deploy_number DESC LIMIT 1;
   ```
4. Verify:
   - `execution_mode` is `reuse_image`.
   - The deployment status transitions to `failed` (NOT `running`).
   - `failure_reason` explicitly states: `Rollback failed closed: immutable image '...' is unavailable in Docker daemon...; will not rebuild from current source`.
   - The deployment engine did NOT attempt to acquire source code or execute `docker build`.
   - Any currently running healthy container for this service was NOT stopped or destroyed.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 15: Local Agent Connected Source Authentication & Deployment

#### Purpose
Verify the secure local agent credential flow: agent session creation, encrypted persistence in `sources.encrypted_session_token`, ownership verification, authenticated streaming of source files via `Authorization: Bearer <token>`, and fail-closed behavior on missing or expired credentials.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- Host machine running the ForgeLAB local agent:
  ```bash
  cd backend && go run cmd/agent/main.go
  ```
- Agent is responding to status probe on `http://127.0.0.1:4142/api/agent/status`.
- Central ForgeLAB control plane running on `http://localhost:8080` (or `http://localhost:3000`).

#### Test Procedure
1. In the ForgeLAB dashboard, click **New Project** and select **Local Agent**.
2. Select a local directory containing a Node.js or Go application.
3. Observe the agent modal:
   - Agent discovers project and generates session token.
   - Central backend receives `POST /api/sources/agent/register`.
   - Database record in `sources` has `encrypted_session_token` populated with non-empty binary ciphertext, while `metadata` has no `token` or `session_token` fields.
4. Click **Deploy**.
5. Observe the deployment logs:
   - The deployment engine decrypts the token via `SourceService.GetDecryptedAgentToken`.
   - Engine calls `GET /api/agent/sources/<id>/stream-context` sending `Authorization: Bearer <token>`.
   - Agent returns `200 OK` with `.tar` context stream.
   - The build proceeds to completion without any `401 Unauthorized` errors.
6. Verify credential failure protection:
   - Manually clear `encrypted_session_token` for the source in PostgreSQL:
     ```sql
     UPDATE sources SET encrypted_session_token = NULL WHERE id = '<source_id>';
     ```
   - Trigger a new deployment for this service.
   - Verify the deployment fails immediately with: `agent session credential missing or expired; please re-select project folder`.
   - Verify no unauthenticated HTTP call was made to the agent daemon.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 16: Multi-Service Concurrency, Build-Slot Queueing, and BuildKit Deployment Reliability

#### Purpose
Verify the end-to-end reliability of multi-service deployments on Windows with Docker Desktop:
1. Docker build slot serialization under concurrency limit 1 without deadlock.
2. Queue progress visibility (queue position, elapsed wait time, and current active slot owner).
3. Elimination of premature HTTP connection opening to the local agent before slot acquisition.
4. Reliable BuildKit context unpacking over Windows named pipes (`//./pipe/docker_engine`) without `invalid tar header` corruption.
5. Shared build artifact coordination and migration job completion gating (`service_completed_successfully`).
6. Accurate DAG dependency skip transitions (`models.DeployStatusSkipped`) instead of reporting skipped services as build failures.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- Windows 10/11 host running Docker Desktop 4.29+ with Linux containers (WSL2 / Hyper-V backend).
- Docker named pipe active at `//./pipe/docker_engine`.
- ForgeLAB agent running: `cd backend && go run cmd/agent/main.go` on `http://127.0.0.1:4142`.
- Central ForgeLAB control plane running: `cd backend && go run cmd/server/main.go` on `http://localhost:8080`.
- PostgreSQL and Redis containers active.

#### Test Procedure 16.1 — Two Services Waiting for Docker Build Slots (Scenario A/B Concurrency)
1. Configure build concurrency limit to 1:
   ```powershell
   $env:FORGELAB_MAX_CONCURRENT_BUILDS = "1"
   ```
2. Import a project with two independent buildable services (e.g. `ccms-backend` and `ccms-frontend`).
3. Trigger a full release deployment.
4. Observe the live deployment logs in the dashboard or WebSocket stream:
   - Service 1 enters the build queue and immediately acquires the Docker build slot:
     ```text
     [BUILD] Service 'ccms-backend' waiting for Docker build slot...
     [BUILD] Docker build slot acquired.
     [BUILD] Docker build started (timeout: 45m0s).
     ```
   - Service 2 enters the queue and reports its position and current owner rather than hanging or opening a stream prematurely:
     ```text
     [BUILD] Service 'ccms-frontend' waiting for Docker build slot...
     [BUILD] Queued for Docker build slot: position 1, elapsed 5s (currently held by service 'ccms-backend' in phase 'docker_build')
     ```
   - No HTTP connection to `/api/agent/sources/<id>/stream-context` is opened for Service 2 while it is queued.
5. Once Service 1 completes, verify Service 2 immediately acquires the slot, requests its context stream, builds, and starts.
6. Verify both containers become healthy and active build slots return to 0.

#### Test Procedure 16.2 — ForgeLAB Self-Deployment (Scenario A Shared Image & BuildKit)
1. In the ForgeLAB dashboard, select the ForgeLAB root repository containing `docker-compose.yml`.
2. Inspect detected services: `postgres`, `redis`, `migrate`, `backend`, `frontend`.
3. Verify `migrate` and `backend` both share the image `forgelab-backend:latest` with build context `./backend`.
4. Trigger the release deployment.
5. Expected chronological execution log:
   - Tier 1: `postgres` and `redis` start and pass health checks.
   - Tier 2: `migrate` acquires the build slot and compiles `./backend/Dockerfile` using BuildKit (`builder_version=2`).
     - Staged archive is submitted with exact `Content-Length` via memory buffering.
     - BuildKit unpacks context cleanly without `archive/tar: invalid tar header`.
     - Container `forgelab-migrate` runs `./forgelab-migrate up` and exits with code 0 (`service_completed_successfully`).
   - Tier 3: `backend` starts using the pre-built `forgelab-backend:latest` image without triggering an unnecessary redundant build, passes health check on `:8080/health`.
   - Tier 4: `frontend` builds from `./frontend/Dockerfile`, starts, and passes health check on `:3000`.
6. Confirm the overall release status transitions to `running`.

#### Test Procedure 16.3 — Stream Interruption and Cancellation Recovery
1. Start a deployment of a service with a large context.
2. During context transfer, abruptly terminate the agent process (`Ctrl+C` in agent console).
3. Observe backend log:
   - Backend reports: `failed receiving build context stream: unexpected EOF`.
   - Staged temporary archive file in `os.TempDir()` is deleted immediately.
   - Build slot is released immediately (`active_count: 0`).
4. Restart agent and re-trigger deployment; verify it proceeds immediately without permanent queue blockage or permit leaks.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 17. Project Deletion Cleanup & Image Scoping

#### Test Procedure 17.1 — Idempotent Project Deletion & Orphan Container Cleanup
1. In the ForgeLab UI, deploy a project with multiple services (e.g. `web` and `api`).
2. After deployment reaches `running`, open a terminal and inspect running containers:
   ```bash
   docker ps --filter "label=forgelab.project_id=<project_id>"
   ```
3. Manually spawn an untracked/orphan container on the Docker host carrying the project's ownership label:
   ```bash
   docker run -d --label "forgelab.project_id=<project_id>" --name orphan-test-container alpine sleep 3600
   ```
4. Click **Delete Project** in the ForgeLAB UI.
5. Expected behavior:
   - Project transitions to `deleting` status.
   - Any in-progress or queued deployments are immediately cancelled.
   - New deployment attempts during deletion are rejected with HTTP 409 Conflict (`project is currently being deleted`).
   - All containers labeled `forgelab.project_id=<project_id>` (including the manual orphan container) are stopped and removed.
   - Project bridge network (`forgelab-net-<project_id>`) is removed.
   - Only images tagged with label `forgelab.project_id=<project_id>` or repo prefix `forgelab/<project_id>` are removed.
   - Persistent volumes (`com.docker.compose.project` or named volumes) are preserved intact (`RemoveVolumes: false`).
   - Unrelated images (base images, other projects' images) remain completely untouched.
   - Once cleanup succeeds, the project is removed from PostgreSQL and the user is redirected to the dashboard.

#### Test Procedure 17.2 — Failure Atomicity & Deletion Retryability
1. Simulate a Docker daemon failure or unremovable resource during cleanup.
2. Trigger project deletion.
3. Expected behavior:
   - The deletion operation returns an explicit error to the client.
   - The project record is NOT deleted from PostgreSQL.
   - Project status transitions to `failed` (`models.ProjectStatusFailed`).
   - The project remains visible and manageable on the dashboard.
4. Resolve the Docker daemon issue and click **Delete Project** again.
5. Expected behavior:
   - Cleanup proceeds idempotently, safely skipping already-deleted resources.
   - Upon completion, the project record is successfully deleted from PostgreSQL.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 18. Startup & Continuous Runtime State Reconciliation

#### Test Procedure 18.1 — Continuous Health Check & Exit Code Discrimination
1. Deploy a multi-service project (e.g. `frontend` on `:3000` and `backend` on `:8080`).
2. Simulate an intentional container stop on the backend:
   ```bash
   docker stop <backend_container_id>
   ```
3. Observe ForgeLab dashboard:
   - Backend service transitions to `stopped` (`models.ServiceStatusStopped`) within the health check loop.
   - Parent project status transitions to `partially_running`.
   - Backend preview URL is hidden / marked `(stopped)`.
4. Now simulate a container crash or OOM kill:
   ```bash
   docker kill --signal=SIGKILL <backend_container_id>
   ```
5. Observe ForgeLab dashboard:
   - Reconciler reads non-zero exit code or OOM flag and marks service status as `failed` (`models.ServiceStatusFailed`).
   - Parent project status reflects failure.
   - Real-time updates arrive via WebSocket `project_status` without needing page reload.

#### Test Procedure 18.2 — Synchronous Startup Reconciliation
1. With containers in stopped/failed state from Test 18.1, stop the ForgeLab control plane server.
2. Restart the ForgeLab server (`./server` or `go run ./cmd/server/main.go`).
3. Simultaneously open the dashboard in a browser.
4. Expected behavior:
   - Control plane performs `ReconcileDaemonContainers` and `CheckContinuousRuntimeHealth` synchronously before accepting HTTP requests.
   - Dashboard immediately reflects `stopped` or `partially_running` status, never displaying stale `running` states.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 19. Redeploy vs. Start UI & Preview URL Consistency

#### Test Procedure 19.1 — Redeploy Button Behavior on Inactive Projects
1. Navigate to the Projects dashboard table and mobile view.
2. For an intentionally stopped project whose container is intact:
   - Verify both **Start** and **Redeploy** buttons are available.
   - Clicking **Start** resumes the existing stopped container without rebuilding.
3. For a project whose container crashed or failed:
   - Verify **Redeploy** button is prominently displayed with the `RotateCcw` icon.
   - Verify Preview link is hidden and displays `—`.
4. Click **Redeploy**:
   - Verify button shows a loading spinner during API dispatch.
   - Deployment starts and project status updates to `queued` / `deploying`.
   - Once healthy, project status transitions to `running`.
   - Preview link reappears with direct clickable link.

#### Test Procedure 19.2 — Service Card Redeploy & Preview Visibility
1. Open the project detail page.
2. Inspect individual service cards:
   - Stopped service: displays **Start** and **Redeploy** buttons; preview banner is hidden; meta grid shows `:<port> (stopped)`.
   - Failed service: displays **Redeploy** button; preview banner is hidden.
   - Running public service: displays prominent green preview banner with pulsing live indicator and direct preview link; meta grid shows clickable link with external icon.
3. Click **Redeploy** on a single service:
   - Only that service enters deployment (`Building...`, `Starting...`, `Health Checking...`).
   - Other services continue running unaffected.
   - Historical deployments in the History drawer retain snapshot metadata (`image_digest`, `env_config_hash`, source revision).

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 20. Resilient Runtime Log Collection & Backend Restart Recovery

#### Test Procedure 20.1 — Continuous Runtime Log Stream After Deployment Completion
1. Deploy a multi-service or single-service project (e.g. backend service that produces HTTP request logs on stdout/stderr).
2. Wait for deployment status to reach `running`.
3. Verify the deployment execution returned (e.g. notification badge says deployment completed, status is `running`).
4. Generate traffic against the service:
   ```bash
   curl http://localhost:<mapped_port>/health
   curl http://localhost:<mapped_port>/api/hello
   ```
5. Observe the live terminal logs in ForgeLAB UI:
   - Verify incoming runtime logs appear in real time under the `Production` tab with phase `runtime` and stream `stdout`.
   - Verify log lines are not dropped or truncated after the build/promotion phase ended.
6. Stop the service or project via ForgeLAB UI ("Stop Service" or "Stop Project"):
   - Verify the log collector terminates cleanly.
   - Verify no orphaned goroutines or docker log streams remain spinning.

#### Test Procedure 20.2 — Log Collector Recovery After Backend Restart
1. With a service confirmed running and producing logs, stop the ForgeLAB backend server:
   ```bash
   docker compose stop backend
   ```
   (or kill the host backend process if running in Environment A).
2. Generate additional traffic or background logs in the running container while the backend is down:
   ```bash
   curl http://localhost:<mapped_port>/health
   ```
3. Restart the ForgeLAB backend server:
   ```bash
   docker compose start backend
   ```
4. Check backend logs to verify daemon reconciliation:
   ```bash
   docker compose logs backend | grep -i "restoring runtime log collector"
   ```
   - Verify log collector was restored for each running container (`multi-service` and `legacy`).
5. Open the ForgeLAB UI and navigate to the project logs:
   - Verify live log streaming resumes immediately upon WebSocket connection.
   - Verify subsequent container output appears in the UI without needing to redeploy.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 21. Log UI Phase Filtering, Service Aggregation, and WebSocket Reconnect Replay

#### Test Procedure 21.1 — Phase Filtering (All Logs, Deployment, Production) & Service Scoping
1. Deploy a project with multiple services (e.g. `web` and `api`).
2. Open the project logs view.
3. Verify the Phase Filter toolbar:
   - **All Logs**: displays all logs with total count badge `(N)`.
   - **Deployment**: displays only `source`, `build`, `startup`, and `health` logs with badge count matching deployment entries.
   - **Production**: displays only ongoing runtime `stdout`/`stderr` logs with badge count matching runtime entries.
4. Click **Deployment**:
   - Verify runtime lines disappear; only build and startup steps are shown.
5. Click **Production**:
   - Verify build lines disappear; only ongoing application stdout/stderr output is shown.
6. Verify Service Scoping:
   - Click **All Services**: verify logs from all services (frontend, backend, databases, workers) are displayed in chronological order, each marked with its color-coded service badge (e.g. `[api]`, `[web]`).
   - Click individual service pill (e.g. `api`): verify only logs from that specific service are displayed.

#### Test Procedure 21.2 — WebSocket Reconnect Sequence Replay & Bounded Memory
1. In the browser, open the developer tools Console and Network (WS) tabs.
2. Observe active log streaming on `project:<id>`. Note the last log ID received (e.g. `last_sequence = 150`).
3. Simulate network drop by toggling "Offline" in Chrome DevTools Network tab for 5 seconds, then toggle back to "Online".
4. Observe WebSocket reconnection frame:
   - Reconnect sends frame: `{"type": "subscribe", "channel": "project:<id>", "last_sequence": 150}`.
   - Backend queries PostgreSQL for `id > 150` and replays missing events before live streaming resumes.
5. Verify no gap in log numbering and no duplicate log entries rendered in the terminal viewer.
6. Simulate a high-volume stream (e.g. over 2,500 log lines):
   - Verify frontend memory remains bounded at `MAX_STORED_LOGS = 2000`.
   - Verify browser performance remains smooth and responsive without memory exhaustion.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 22. Hardened Docker Log Stream Parsing & Race-Safe Collector Lifecycle

#### Test Procedure 22.1 — Oversized-Frame and Non-Terminated Stream Resilience
1. Start a service container that emits extreme log payloads:
   - Case A: A continuous stream of bytes without newline characters exceeding 64KB (e.g. continuous base64 payload or minified bundle).
   - Case B: An oversized Docker multiplexed stream header advertising a payload > 8MB.
2. In Case A (continuous newline-free output):
   - Monitor ForgeLAB backend memory consumption.
   - Verify memory does not grow unbounded; lines are chunked and flushed safely at `DefaultMaxLineLength` (64KB).
   - In UI terminal and database, verify the log entry is truncated with `... [truncated: exceeded 64KB log size limit]`.
3. In Case B (malformed/oversized multiplexed frame):
   - Verify the parser discards the oversized payload bytes cleanly via `io.Discard` without desynchronizing the stream.
   - Verify subsequent standard log frames sent on stdout or stderr are parsed accurately without corrupting the 8-byte frame header boundary.

#### Test Procedure 22.2 — Collector Replacement Race Safety on Rapid Container Redeploy
1. Trigger a fast redeploy or container recreation on an existing service while the prior container's runtime log collector is still actively draining output.
2. When the new container starts, a new collector instance is registered for the service deployment.
3. Allow the old container collector to exit its loop and execute its deferred cleanup:
   - Verify the old collector's exit hook checks instance ownership and does NOT deregister or close the replacement collector's context.
   - Verify the replacement collector continues streaming stdout/stderr without interruption.
4. Verify backend logs and health diagnostics show the replacement collector remaining registered and active.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 23. Log Collector Observability and Configurable Retention Cleanup

#### Test Procedure 23.1 — Collector Health Diagnostics via `/health/logs` and `/ready`
1. Ensure the ForgeLAB backend is running with active deployed containers.
2. Query the readiness endpoint:
   ```bash
   curl -s http://localhost:8080/ready | jq .
   ```
   - Verify HTTP 200 OK.
   - Verify response contains `log_collectors` block with fields: `active_collectors`, `total_registered`, `total_unregistered`, `total_collection_errors`, `total_persistence_errors`.
3. Query the dedicated log collector diagnostics endpoint:
   ```bash
   curl -s http://localhost:8080/health/logs | jq .
   ```
   - Verify response returns detailed collector states under `collectors`:
     - `key` (e.g. `service:<service_deployment_id>` or `container:<container_id>`)
     - `instance_id` (unique UUID)
     - `container_id`
     - `service_deployment_id`
     - `project_id`
     - `started_at`
     - `last_log_at`
     - `error_count`
     - `is_active` (boolean)
4. Verify that secrets, tokens, and raw log payloads are strictly omitted from diagnostic output.

#### Test Procedure 23.2 — Configurable Log Retention Policy & Automatic Cleanup
1. Configure test retention limits in `.env` or environment variables:
   ```bash
   FORGELAB_LOG_RETENTION_DAYS=7
   FORGELAB_LOG_MAX_PER_DEPLOYMENT=500
   FORGELAB_LOG_CLEANUP_INTERVAL=1m
   ```
2. Insert test log records into `deployment_logs` exceeding age (e.g. created 10 days ago) or exceeding count (e.g. 600 records for a single deployment).
3. Wait for the background cleanup ticker (or trigger pruning cycle):
   - Verify backend logs output: `log pruning cycle completed: pruned N log rows`.
   - In PostgreSQL, query `deployment_logs`:
     ```sql
     SELECT COUNT(*) FROM deployment_logs WHERE deployment_id = '<target-id>';
     ```
     - Verify records older than 7 days have been safely deleted.
     - Verify total log count for the over-limit deployment has been pruned down to `FORGELAB_LOG_MAX_PER_DEPLOYMENT` (500), keeping the newest records based on `id DESC`.
4. Verify other deployments below the limit are unaffected.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 24. WebSocket Security Hardening and Single-Use Ticket Protocol

#### Test Procedure 24.1 — Query String Token Rejection (`?token=...`)
1. Obtain a valid JWT access token by logging into ForgeLAB.
2. Attempt to open a WebSocket connection passing the token in the URL query string:
   ```bash
   # Using wscat or curl
   curl -i -N -H "Connection: Upgrade" -H "Upgrade: websocket" \
     -H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" \
     "http://localhost:8080/api/ws?token=<jwt_access_token>"
   ```
3. Verify that the server rejects the request with **HTTP 401 Unauthorized** (or WebSocket connection failed).
4. Verify that passing any `?token=...` parameter never establishes a WebSocket connection.

#### Test Procedure 24.2 — Ticket Generation and Single-Use Consumption
1. Mint a short-lived ticket via `POST /api/ws/ticket` using the valid Bearer token:
   ```bash
   curl -s -X POST http://localhost:8080/api/ws/ticket \
     -H "Authorization: Bearer <jwt_access_token>" | jq .
   ```
2. Verify response contains `ticket` (UUID) and `expires_in` (60).
3. Connect using the ticket parameter:
   ```bash
   curl -i -N -H "Connection: Upgrade" -H "Upgrade: websocket" \
     -H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" \
     -H "Origin: http://localhost:3000" \
     "http://localhost:8080/api/ws?ticket=<ticket_uuid>"
   ```
4. Verify the server returns **HTTP 101 Switching Protocols**.
5. Immediately attempt to connect again using the same ticket:
   ```bash
   curl -i -N -H "Connection: Upgrade" -H "Upgrade: websocket" \
     -H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" \
     -H "Origin: http://localhost:3000" \
     "http://localhost:8080/api/ws?ticket=<ticket_uuid>"
   ```
6. Verify that second connection is rejected with **HTTP 401 Unauthorized** (enforcing single-use).

#### Test Procedure 24.3 — WebSocket Origin Policy Enforcement
1. Mint a ticket via `POST /api/ws/ticket`.
2. Attempt connection with a disallowed external origin:
   ```bash
   curl -i -N -H "Connection: Upgrade" -H "Upgrade: websocket" \
     -H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" \
     -H "Origin: http://evil-attacker.com" \
     "http://localhost:8080/api/ws?ticket=<ticket_uuid>"
   ```
3. Verify that connection is rejected with **HTTP 403 Forbidden** (or HTTP 401).

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 25. Diagnostics Access Control

#### Test Procedure 25.1 — Public `/ready` Information Redaction for External Callers
1. Simulate an external, unauthenticated client by making a request to `/ready` with an external peer address:
   ```bash
   curl -s http://localhost:8080/ready | jq .
   ```
2. Verify that the response returns HTTP 200 with dependency statuses (`postgres`, `redis`, `docker`).
3. Verify that internal structures (`queue`, `build_queue`, `log_collectors`) are completely omitted or nil in the response.
4. Now perform the request with an authenticated Bearer token:
   ```bash
   curl -s -H "Authorization: Bearer <jwt_access_token>" http://localhost:8080/ready | jq .
   ```
5. Verify that full diagnostics (`queue`, `build_queue`, `log_collectors`) are present in the response.

#### Test Procedure 25.2 — Restriction of `/health/queue` and `/health/logs`
1. Request `/health/queue` as an external unauthenticated caller:
   ```bash
   curl -i -s http://localhost:8080/health/queue
   ```
2. Verify that the server returns **HTTP 401 Unauthorized**.
3. Repeat for `/health/logs`:
   ```bash
   curl -i -s http://localhost:8080/health/logs
   ```
4. Verify that the server returns **HTTP 401 Unauthorized**.
5. Supply valid `Authorization: Bearer <token>`:
   ```bash
   curl -s -H "Authorization: Bearer <jwt_access_token>" http://localhost:8080/health/queue | jq .
   ```
6. Verify that HTTP 200 OK is returned with detailed queue diagnostic metrics.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 26. Docker Host Port Allocation Conflict Retries

#### Test Procedure 26.1 — Port Conflict Detection and Transparent Retry
1. Identify the port range configured for ForgeLAB (default 10000–60000).
2. Manually bind a port in that range using `nc` or a dummy container outside ForgeLAB:
   ```bash
   nc -l 10500 &
   ```
3. Trigger a deployment in ForgeLAB where the port manager assigns 10500 (or simulate port conflict).
4. Verify from the backend deployment logs that ForgeLAB detects `port is already allocated` / port conflict.
5. Verify that ForgeLAB automatically:
   - Marks port 10500 as conflicted/used in `PortManager`.
   - Allocates a fresh port (e.g. 10501).
   - Updates container host port bindings and retries creation/start.
   - Completes deployment successfully on the new port without failing the deployment.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 27. Build Reproducibility and Source Revision Fingerprint Verification

#### Test Procedure 27.1 — Content-Fingerprint Verification for Local Directory Builds
1. Select a local directory project with reviewed plan.
2. Record the computed content fingerprint (starts with `fp_`).
3. Modify a source file in the directory (e.g. edit `index.js` or `package.json`).
4. Trigger deployment referencing the prior deployment revision.
5. Verify that deployment fails closed with:
   `source content changed since deployment was created: expected fingerprint fp_..., got fp_...`
6. Verify that the previous healthy container remains active and untouched.
7. Trigger a new plan review so the fresh content fingerprint is registered, and deploy.
8. Verify the deployment builds and starts successfully with `Verified source content matches reviewed revision fp_...` in the system build logs.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 28. Local Agent Identity Verification Probe & Short-Lived Credential Revocation

#### Test Procedure 28.1 — Identity Verification Before Session Token Delivery
1. Start the local agent daemon: `go run cmd/agent/main.go`.
2. Inspect the registered agent ID (e.g. `laptop-macos` or auto-generated UUID).
3. Attempt an unauthenticated verification probe with a spoofed/mismatched agent ID:
   ```bash
   curl -s -X POST http://127.0.0.1:8081/api/agent/verify-identity \
     -H "Content-Type: application/json" \
     -d '{"expected_agent_id":"wrong-agent-id"}'
   ```
4. Verify response returns `{"verified":false,"agent_id":""}` with HTTP 200 or 404.
5. Probe with the correct `expected_agent_id`:
   ```bash
   curl -s -X POST http://127.0.0.1:8081/api/agent/verify-identity \
     -H "Content-Type: application/json" \
     -d '{"expected_agent_id":"<correct-id>"}'
   ```
6. Verify response returns `{"verified":true,"agent_id":"<correct-id>"}`.
7. Trigger an agent-backed project deployment and verify in backend logs that the control plane probes identity and confirms identity before transmitting any bearer credentials or session tokens to candidate fallback URLs (`127.0.0.1:8081` or `host.docker.internal:8081`).
8. Explicitly revoke the session token via:
   ```bash
   curl -s -X POST http://localhost:8080/api/sources/agent/session/revoke \
     -H "Content-Type: application/json" \
     -d '{"token":"<session-token>"}'
   ```
9. Verify that subsequent agent context requests with that token return `401 Unauthorized`.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 29. Anonymous Guest Browser Session Isolation & Registration-Free Local Usage

#### Test Procedure 29.1 — Browser Guest Cookie Isolation
1. Open an Incognito / Private browsing window (Session A) and visit `http://localhost:3000`.
2. As a guest (unauthenticated), create a project `Guest Project A` and upload a sample application.
3. Inspect browser developer tools > Application > Cookies and verify `forgelab_guest_session` is present with a UUID value.
4. Open a second distinct browser or separate private window (Session B) and visit `http://localhost:3000`.
5. Verify `Guest Project A` does NOT appear in the project list for Session B.
6. Verify Session B receives its own distinct `forgelab_guest_session` cookie.
7. Attempt to fetch `Guest Project A` directly via API from Session B:
   ```bash
   curl -s http://localhost:8080/api/projects/<project-A-id> \
     -H "Cookie: forgelab_guest_session=<session-B-uuid>"
   ```
8. Verify the server returns `403 Forbidden` or `404 Not Found`.

#### Test Procedure 29.2 — Local Loopback CLI Fallback
1. Run a loopback CLI curl request without cookies:
   ```bash
   curl -s http://localhost:8080/api/projects
   ```
2. Verify the request succeeds without authentication, resolving to `models.GuestUserID` (`00000000-0000-0000-0000-000000000001`).

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 30. Deployment State Machine & Healthy Release Preservation Invariant

#### Test Procedure 30.1 — Failed Deployment Leaves Prior Release Active
1. Deploy a working service (e.g. `express-app`) and verify it reaches `RUNNING` on an allocated host port (e.g. `10025`).
2. Verify the live application is reachable at `http://localhost:10025`.
3. Introduce an intentional build or healthcheck failure (e.g. broken start command, failing healthcheck path `/fail-now`, or non-listening port).
4. Trigger a new deployment of the project or service.
5. Observe the deployment transition through `BUILDING` → `STARTING` → `HEALTH_CHECKING` → `FAILED`.
6. Inspect the running containers via `docker ps`.
7. Verify that the previous healthy container is STILL running and was NOT killed or removed during or after the failed deployment.
8. Query `GET /api/projects/{id}` and verify project status remains `running` (derived via `CalculateProjectStatus` from active healthy services), not erroneously overwritten to `failed`.
9. Send an HTTP request to `http://localhost:10025` and confirm the previous healthy release responds with zero downtime.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 31. Runtime Accuracy — Live-Preview URL Suppression

#### Test Procedure 31.1 — Preview URL Omission for Non-Running Containers
1. Deploy a project with a frontend service and verify the preview URL appears in the project header and services list when `status == "running"`.
2. Stop the service or project using `POST /api/projects/{id}/stop` or `POST /api/projects/{id}/services/{serviceId}/stop`.
3. Verify that `GET /api/projects/{id}` and `GET /api/projects/{id}/services` return `preview_url: null`.
4. Check the frontend UI and verify the "Live Preview" / external URL link is hidden or disabled, displaying the stopped state accurately without misleading click-throughs.
5. Start the project again and verify the preview URL is re-populated once the container is confirmed healthy and running.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 32. Service Connection Diagnostics & Topology View

#### Test Procedure 32.1 — Topology Tab Inspection and Diagnostics
1. Deploy a multi-service Compose project (e.g. `db` postgres infrastructure, `backend` API depending on `db`, and `frontend` depending on `backend`).
2. Navigate to the project page in the web UI and click the **Topology & Diagnostics** tab.
3. Verify that:
   - **Execution Tiers** are rendered sequentially: Tier 0 (`db`), Tier 1 (`backend`), Tier 2 (`frontend`).
   - Each service node displays its internal DNS hostname (e.g. `db:5432`, `backend:8080`), host port (if exposed), network badge (`forgelab-net-<id>`), and health check strategy.
   - Dependency relationships (`depends_on`) are visually listed on each node.
4. Stop or fail the `db` service container manually via Docker CLI: `docker stop <db-container-id>`.
5. Refresh or re-inspect the Topology tab:
   - Verify that an actionable diagnostic card appears:
     - **Severity:** Error
     - **Category:** `dependency`
     - **Title:** `Upstream Dependency Failed`
     - **Message:** Indicates that dependent services require `db` which is currently not running.
     - **Action:** Directs operator to check database container logs.
6. Verify `GET /api/projects/{id}/topology` returns this complete topology payload as defined in the API contract.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 33. Safe Node.js/Next.js Lifecycle Build — Prisma Selective Staging

#### Test Procedure 33.1 — Event_Management_System Deployment
1. Ensure ForgeLAB is running in your normal development environment:
   - Backend: Go backend running with PostgreSQL and Redis accessible.
   - Frontend: Next.js frontend running on `http://localhost:3000`.
2. Navigate to the ForgeLAB dashboard and click **New Project**.
3. Select **GitHub** tab (or import snapshot for `Estifanos58/Event_Management_System`).
4. In the Project Creation Modal / Service Review:
   - Confirm ForgeLAB detects Next.js runtime.
   - Confirm the selected service root contains `prisma/schema.prisma` and `prisma.config.ts`.
5. Trigger project deployment:
   - Observe the live build logs streamed via WebSocket.
   - Verify the builder stage emits:
     - `COPY package.json ...`
     - `COPY prisma ./prisma/` (and `COPY prisma.config.ts ./` if present)
     - `RUN DATABASE_URL=... npm ci`
     - Dependency installation runs `postinstall: prisma generate`.
   - Verify that Prisma Client generates successfully without the `schema.prisma not found` error.
   - Verify that Next.js compilation (`next build`) runs and `.next/cache` is removed from the builder image.
6. Verify the container starts, passes its configured health check, and reaches `RUNNING` status.
7. Verify that zero database credentials or secrets were printed to build logs or baked into image layers.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 34. Build Preflight Validation & Actionable Diagnostics

#### Test Procedure 34.1 — Missing Prisma Schema Preflight Diagnostic
1. Prepare a test project repository or branch that specifies `"postinstall": "prisma generate"` in `package.json` but where `prisma/schema.prisma` has been deleted or moved to an undeclared location.
2. Attempt to create and deploy the project.
3. Verify that preflight validation fails **before** invoking Docker Engine:
   - The deployment immediately transitions to `FAILED` with an actionable error.
   - Diagnostic error clearly states: `package.json invokes 'prisma generate', but Prisma schema was not found at expected path: prisma/schema.prisma`.
   - Zero orphaned Docker build contexts or builder cache layers are created.

#### Test Procedure 34.2 — Standard Next.js Application (No Prisma)
1. Deploy a standard Next.js application that does not use Prisma (e.g. standard `create-next-app`).
2. Verify that the generated Dockerfile contains **zero** `COPY prisma` instructions.
3. Verify that dependency installation (`npm ci`), `next build`, and container execution succeed without warning or error.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 35. Service Exposure Controls & Isolation Defaults

#### Test Procedure 35.1 — UI Exposure Toggle & Safe Defaults
1. Open the **New Project** modal and import a multi-service project (e.g. frontend + backend API + postgres + redis).
2. In the **Deployment Plan Review** step:
   - Verify that the frontend service defaults to `Public: :<port>` (emerald badge).
   - Verify that `postgres` and `redis` infrastructure services default to `Internal: <name>:<port>` (neutral badge).
3. Click the exposure button on the frontend service:
   - Verify it toggles to `Internal: frontend:3000 (Click to expose Publicly)`.
4. Click the exposure button on the backend service:
   - Verify it toggles to `Public: :8080 (Click to make Internal)`.
5. Deploy the project and verify:
   - Host ports are allocated and preview URLs generated only for explicitly public services.
   - Internal database and cache services are bound only to the private Docker network and cannot be reached directly from host ports.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 36. Multi-Service Inter-Service Communication & Dependency Health

#### Test Procedure 36.1 — Full-Stack Multi-Service Deployment (Postgres, Redis, API, Frontend)
1. Deploy a multi-service project containing:
   - `postgres` (infrastructure)
   - `redis` (infrastructure)
   - `api` (backend depending on postgres and redis with `condition: service_healthy`)
   - `frontend` (web service depending on api)
2. Verify deployment tier execution:
   - Tier 0 (`postgres`, `redis`) start and pass health checks.
   - Tier 1 (`api`) starts only after Tier 0 dependencies report healthy.
   - Tier 2 (`frontend`) starts after `api` is ready.
3. Test container-to-container connectivity:
   - Exec into the `api` container and verify DNS resolution: `getent hosts postgres` and `getent hosts redis` return container IPs on the project network.
   - Verify the `api` successfully executes SQL queries against `postgres` using `$DATABASE_URL` and connects to `redis` using `$REDIS_URL`.
4. Stop or pause the `postgres` container:
   - Check the **Topology & Diagnostics** tab in ForgeLAB and verify the system reports `Upstream Dependency Failed` for dependent services.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### 37. WebSocket Ticket Authentication & Compatibility Fallback

#### Test Procedure 37.1 — Frontend Ticket Authentication
1. Open browser developer tools on `http://localhost:3000/projects/<id>`.
2. Inspect the Network tab for WebSocket traffic:
   - Verify the frontend initiates `POST /api/ws/ticket` before establishing the WebSocket connection.
   - Verify the WebSocket URL connects using `GET /api/ws?ticket=<uuid>`.
   - Verify connection succeeds (HTTP 101 Switching Protocols) and logs stream live.
3. Attempt to reconnect using the same ticket UUID again:
   - Verify the ticket is single-use and the second connection attempt is rejected with HTTP 401.

#### Test Procedure 37.2 — Legacy Query-Token Compatibility
1. Mint or obtain a valid user JWT access token.
2. Using a tool like `wscat` or `websocat`, connect directly with query string token:
   `wscat -c "ws://localhost:8080/api/ws?token=<valid_jwt>"`
3. Verify connection succeeds (HTTP 101).
4. Inspect the backend log output and confirm:
   - A deprecation notice is logged: `[WARN] websocket: query parameter '?token=' authentication is deprecated, use short-lived tickets (POST /api/ws/ticket) or cookies`.
   - The token value itself is strictly redacted/absent from the log line.
5. Connect with an invalid token `?token=invalid.jwt.signature`:
   - Verify connection is rejected with HTTP 401 Unauthorized.

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —
