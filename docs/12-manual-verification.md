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
| **User Logout** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests `POST /api/auth/logout` & cookie clearing |
| **Token Refresh** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests atomic single-use rotation |
| **Authentication Failure Behavior** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests invalid/expired tokens (HTTP 401) |
| **Project Creation** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests `POST /api/projects` & slug generation |
| **Project Listing** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests `GET /api/projects` user-scoped list |
| **Project Ownership Enforcement** | IMPLEMENTED | NOT RECORDED | — | — | — | — | User B cannot view User A's project (HTTP 403/404) |
| **Invalid Project Access** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Tests non-existent UUIDs and unauthorized access |
| **Local Source Path Validation** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Rejects system dirs & paths outside allowed roots |
| **Local Source Snapshotting** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Copies source to `data/builds/<id>` before build |
| **Redis Deployment Queue** | IMPLEMENTED | NOT RECORDED | — | — | — | — | `LPUSH` enqueue and worker `BRPOP` dequeue |
| **Docker Image Build** | IMPLEMENTED | NOT RECORDED | — | — | — | — | Builds image from tar stream via Docker SDK |
| **Deployment State Transitions** | IMPLEMENTED | NOT RECORDED | — | — | — | — | `QUEUED`→`CLONING`→`BUILDING`→`STARTING`→`HEALTH` |
| **Health-Check Deployment Gate** | IMPLEMENTED | NOT RECORDED | — | — | — | — | 10 attempts HTTP polling before promotion |
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

---

### Procedure 1: Authentication & Token Lifecycle

#### Purpose
Verify user registration, login, authenticated requests, cookie/token handling, logout, and rejection of invalid credentials.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- Stack is running: `docker compose up --build`
- Database migrations applied
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
- Backend logs: `docker compose logs backend`
- PostgreSQL `users` table: `docker compose exec postgres psql -U forgelab -c "SELECT id, email FROM users;"`

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 2: Project Creation & Ownership Enforcement

#### Purpose
Verify that projects can be created, configured, listed, and that cross-user access is strictly prevented.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- User 1 registered (`user1@example.com`)
- User 2 registered (`user2@example.com`) in an Incognito window

#### Test Procedure
1. As User 1 on `http://localhost:3000/dashboard`, click "+ Create Project".
2. Fill in project form:
   - Name: `Demo Web Service`
   - Repository Path: valid local directory path containing a Dockerfile
   - Branch: `main`
   - Dockerfile: `Dockerfile`
   - Build Context: `.`
   - Health Check: `/health`
3. Click "Create Project". Verify redirection to `/projects/<project-id>`.
4. Copy the project UUID from the URL (`/projects/<uuid>`).
5. As User 2 (in Incognito window), log in and verify `Demo Web Service` is **not** visible on User 2's dashboard.
6. In User 2's browser, manually paste the URL `/projects/<user-1-project-uuid>`.
7. Inspect the network response for `GET /api/projects/<user-1-project-uuid>`.

#### Expected Result
- User 1 successfully creates and views project details.
- User 2 cannot see User 1's project on dashboard.
- User 2's request to `/api/projects/<user-1-project-uuid>` receives `404 Not Found` or `403 Forbidden`.

#### Failure Conditions
- User 2 can view or modify User 1's project configuration or deployments.

#### What to Inspect if it Fails
- Project service query in `backend/internal/services/project_service.go` (`GetProject` must filter by `owner_id`).

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 3: Local Deployment & Lifecycle (The Happy Path)

#### Purpose
Verify the complete vertical slice: host path validation, snapshot copy, Docker build, dynamic port allocation, health checking, WebSocket log streaming, and container execution.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- Prepare a minimal test application on the host machine (e.g. `C:\dev\testapp` or `/tmp/testapp`):
  - `Dockerfile`:
    ```dockerfile
    FROM alpine:latest
    RUN apk add --no-cache python3
    WORKDIR /app
    RUN echo 'import http.server, socketserver; handler = http.server.SimpleHTTPRequestHandler; socketserver.TCPServer(("", 8080), handler).serve_forever()' > server.py
    RUN echo '{"status":"ok"}' > health
    CMD ["python3", "-m", "http.server", "8080"]
    ```
- Project created in ForgeLAB with repository path pointing to the test application.

#### Test Procedure
1. Navigate to `/projects/<id>` in ForgeLAB.
2. Click "🚀 Deploy Release".
3. Observe the UI:
   - Status badge transitions: `deploying` (`queued` → `cloning` → `building` → `starting` → `health_checking` → `running`).
   - Live Terminal header displays "WebSocket Live" (green indicator).
   - Build logs stream line-by-line into the terminal window.
   - Health check logs appear showing attempts and HTTP 200 pass.
4. Verify project status updates to `running`.
5. Locate the "Live Port" card in the Configuration panel (e.g. `http://localhost:10001`).
6. Click the link or open a browser tab to `http://localhost:<port>/health`.
7. Verify the response is returned from the deployed container.
8. In ForgeLAB, test lifecycle buttons:
   - Click "Stop" → verify status updates to `stopped` and port is inaccessible.
   - Click "Start" → verify status returns to `running` and container serves requests.
   - Click "Restart" → verify container restarts without errors.

#### Expected Result
- Complete automated deployment pipeline succeeds.
- Logs stream smoothly over WebSockets.
- Container is live on an allocated port in the `10000–60000` range.
- Lifecycle controls (Stop/Start/Restart) correctly manage container state.

#### Failure Conditions
- Build stalls or does not stream logs.
- Port collision occurs or port is not exposed on `0.0.0.0`.
- Health check fails to detect healthy container.

#### What to Inspect if it Fails
- Docker Engine: `docker ps`
- Backend worker logs: `docker compose logs backend`
- Redis queue: `docker compose exec redis redis-cli lrange forgelab:queue:deployments 0 -1`

#### Verification Record
- **Status:** NOT RECORDED
- **Verified by:** —
- **Date:** —
- **Environment:** —
- **Notes:** —

---

### Procedure 4: Failed Deployment & Safety Invariant Preservation

#### Purpose
Verify that a failing deployment (broken build or failed health check) fails gracefully and **never** terminates or displaces the currently running deployment.

#### Implementation Status
`IMPLEMENTED`

#### Physical Verification Status
`NOT RECORDED`

#### Prerequisites
- Project has a running deployment (Deployment #1 from Procedure 3 is active and serving traffic).

#### Test Procedure
1. In the host test application directory, modify `Dockerfile` to introduce a build failure (e.g., `RUN non_existent_command_that_fails`).
2. In ForgeLAB, click "🚀 Deploy Release".
3. Observe the deployment progress:
   - Deployment #2 is created with status `building`.
   - Error logs appear in the terminal showing build failure.
   - Deployment #2 transitions to `failed` with a recorded failure reason.
4. Verify the overall project status remains `running`.
5. Verify the active release link still points to Deployment #1's port.
6. Open the live URL (`http://localhost:<port-deploy-1>/health`) and verify Deployment #1 is still responding.
7. Next, fix the build error in `Dockerfile`, but configure a non-existent health check path in project settings (`/non-existent-endpoint`).
8. Deploy Release again (Deployment #3):
   - Image builds successfully.
   - Container starts on a new host port.
   - Health check logs show 10 failed retries.
   - Deployment #3 transitions to `failed`.
   - The broken container for Deployment #3 is stopped and cleaned up.
9. Open Deployment #1's live URL again; verify Deployment #1 was never terminated.

#### Expected Result
- **Deployment Safety Invariant holds:** Broken deployments transition to `failed` without interrupting the existing active deployment.
- `projects.current_deployment_id` stays pegged to the healthy release.

#### Failure Conditions
- Old container is stopped before new container passes health check.
- `current_deployment_id` is updated to a failed deployment ID.

#### What to Inspect if it Fails
- `backend/internal/docker/engine.go:312-326` (promotion logic)
- Database: `docker compose exec postgres psql -U forgelab -c "SELECT id, status, current_deployment_id FROM projects;"`

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
