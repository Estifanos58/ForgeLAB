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
| **GitHub Repository Connect** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests dedicated `repo` scope OAuth flow & AES encryption |
| **GitHub Repository Picker** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests scoped repository & branch listing API |
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
4. Open Browser DevTools → Application → Local Storage; verify `forgelab_token` is present.
5. In DevTools → Network, inspect request to `/api/auth/me`; verify status is `200 OK` and returns user object.
6. Click the "Logout" button on the dashboard.
7. Verify redirection to `http://localhost:3000/login`, and verify `forgelab_token` is removed from Local Storage.
8. Attempt to navigate directly to `http://localhost:3000/dashboard`; verify client immediately redirects to `/login`.
9. In `/login`, attempt login with wrong password `WrongPassword!`; verify error banner displays "invalid email or password".
10. Log in with correct password `Password123!`; verify dashboard loads successfully.

#### Expected Result
- Clean registration and login transitions.
- Auth token stored in localStorage and sent via Bearer headers.
- Direct protected page access redirects unauthenticated users to `/login`.

#### Failure Conditions
- Registration returns 500 or fails to set token.
- Invalid credentials log user in.
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
1. Prepare a test zip archive containing an entry with relative traversal (e.g. `../../etc/evil.txt`).
2. Make a direct API upload request:
   ```bash
   curl -i -X POST http://localhost:8080/api/sources/upload \
     -H "Authorization: Bearer <user1-token>" \
     -F "archive=@traversal-test.zip"
   ```
3. Verify response status: `422 Unprocessable Entity` or `400 Bad Request` with message `path traversal detected in source archive`.
4. Inspect `data/sources/` on the server to verify the directory was purged immediately.

#### Test Procedure — Part C: GitHub Repository Authorization & Import
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

#### Test Procedure — Part D: Cross-User Ownership Isolation
1. Copy the project UUID created by User 1.
2. In User 2's Incognito browser, log in and verify User 1's project is not shown on the dashboard.
3. Attempt to fetch User 1's project: `GET /api/projects/<user-1-project-uuid>`.
4. Verify response is `403 Forbidden` or `404 Not Found`.
5. In User 2's session, query `GET /api/integrations/github/repositories`.
6. Verify User 2 does NOT see User 1's GitHub repositories or credentials.

#### Expected Result
- Local computer uploads extract cleanly to isolated workspaces without host-path dependencies.
- Path traversal and Zip Slip attacks are blocked.
- GitHub integration allows seamless repo/branch selection without manual URL pasting.
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
6. In Window 1's browser DevTools Console, execute an unauthorized subscription:
   ```javascript
   const ws = new WebSocket(`ws://localhost:8080/api/ws?token=${localStorage.getItem('forgelab_token')}`);
   ws.onopen = () => ws.send(JSON.stringify({ type: 'subscribe', channel: 'project:00000000-0000-0000-0000-000000000000' }));
   ws.onmessage = (e) => console.log('WS msg:', e.data);
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


