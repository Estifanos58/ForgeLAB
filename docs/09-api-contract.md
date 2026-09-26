# ForgeLAB — Authoritative API Contract

**Status:** Current Implementation Specification  
**Base URL:** `http://localhost:8080` (Direct) or `/api` (Proxied via Frontend)  
**Protocol:** HTTP/1.1 (JSON REST) + WebSocket (RFC 6455)  

---

## Related Documents

- [INSTRUCTION.md](../INSTRUCTION.md) — Operational memory & architectural constraints
- [docs/00-current-state.md](00-current-state.md) — Current state snapshot & matrix
- [docs/06-websocket-contract.md](06-websocket-contract.md) — Realtime WebSocket event specifications
- [docs/10-frontend-architecture.md](10-frontend-architecture.md) — Next.js client API consumption
- [docs/12-manual-verification.md](12-manual-verification.md) — Physical test procedures for these endpoints

---

## 1. Global API Conventions

### Content Type & Encoding
- All REST requests and responses use `application/json; charset=utf-8` (enforced by `middleware.ContentTypeJSON`).
- All timestamps use ISO 8601 / RFC 3339 format (`2026-09-26T12:00:00Z`).
- All durable identifiers are standard **UUIDv4** strings (`00000000-0000-0000-0000-000000000000`).

### Authentication & Token Transport
- Authenticated requests require a JWT access token in the `Authorization` header:
  ```http
  Authorization: Bearer <access_token>
  ```
- Alternatively, the backend accepts `forgelab_access_token` in cookies or `token=<jwt>` in query parameters (primarily for WebSocket upgrades).
- Access tokens expire after 15 minutes (`JWT_ACCESS_TOKEN_EXPIRY`).
- Refresh tokens expire after 7 days (`JWT_REFRESH_TOKEN_EXPIRY`) and use atomic single-use rotation.

### Server-Side Ownership Enforcement
- ForgeLAB enforces strict multi-tenant data isolation at the backend service layer:
  - A user can **only** inspect, modify, deploy, or delete projects they own (`projects.owner_id == user.id`).
  - Deployments and environment variables inherit ownership from their parent project.
  - Accessing another user's project yields `403 Forbidden` or `404 Not Found` (to prevent ID enumeration).

### Standard Error Response Format
All HTTP error responses return a JSON payload with an `error` key:
```json
{
  "error": "descriptive error message"
}
```

Common status codes:
- `400 Bad Request`: Malformed JSON, validation failure, or illegal operation.
- `401 Unauthorized`: Missing, invalid, or expired JWT.
- `403 Forbidden`: Authenticated user does not own the requested resource.
- `404 Not Found`: Target resource does not exist.
- `409 Conflict`: Conflict state (e.g. email already exists, deployment already active).
- `500 Internal Server Error`: Unhandled server or infrastructure failure.

---

## 2. Public Endpoints

### `GET /health`
- **Authentication:** None (Public)
- **Authorization:** None
- **Purpose:** Platform control-plane liveness probe for Docker Compose / container orchestration.
- **Success Response:** `200 OK`
  ```json
  {
    "status": "ok",
    "service": "forgelab"
  }
  ```

---

## 3. Authentication Endpoints (`/api/auth`)

### `POST /api/auth/register`
- **Authentication:** None (Public)
- **Authorization:** None
- **Request Body:**
  ```json
  {
    "email": "developer@example.com",
    "password": "SecurePassword123!",
    "display_name": "Developer"
  }
  ```
- **Validation Rules:**
  - `email`: Required, valid format, unique.
  - `password`: Required, minimum 8 characters.
  - `display_name`: Optional string.
- **Success Response:** `201 Created`
  ```json
  {
    "user": {
      "id": "c3d4e5f6-a7b8-4c1d-9e0f-1a2b3c4d5e6f",
      "email": "developer@example.com",
      "display_name": "Developer",
      "created_at": "2026-09-26T10:00:00Z"
    },
    "tokens": {
      "access_token": "eyJhbGciOiJIUzI1NiIsIn...",
      "refresh_token": "dGhpcy1pcy1hLXJlZnJlc2gtdG9rZW4..."
    }
  }
  ```
- **Side Effects:**
  - Sets HttpOnly cookies: `forgelab_access_token` (15m) and `forgelab_refresh_token` (7d).
  - Inserts hashed refresh token record in `refresh_tokens` table.
- **Error Responses:**
  - `400 Bad Request`: Validation failure.
  - `409 Conflict`: Email already registered.

---

### `POST /api/auth/login`
- **Authentication:** None (Public)
- **Authorization:** None
- **Request Body:**
  ```json
  {
    "email": "developer@example.com",
    "password": "SecurePassword123!"
  }
  ```
- **Success Response:** `200 OK`
  ```json
  {
    "user": {
      "id": "c3d4e5f6-a7b8-4c1d-9e0f-1a2b3c4d5e6f",
      "email": "developer@example.com",
      "display_name": "Developer",
      "created_at": "2026-09-26T10:00:00Z"
    },
    "tokens": {
      "access_token": "eyJhbGciOiJIUzI1NiIsIn...",
      "refresh_token": "dGhpcy1pcy1hLXJlZnJlc2gtdG9rZW4..."
    }
  }
  ```
- **Side Effects:**
  - Sets HttpOnly cookies: `forgelab_access_token` and `forgelab_refresh_token`.
  - Creates active refresh token entry in database.
- **Error Responses:**
  - `401 Unauthorized`: Invalid credentials.

---

### `POST /api/auth/refresh`
- **Authentication:** None (Requires valid refresh token in body or cookie)
- **Request Body (Optional if cookie present):**
  ```json
  {
    "refresh_token": "dGhpcy1pcy1hLXJlZnJlc2gtdG9rZW4..."
  }
  ```
- **Success Response:** `200 OK`
  ```json
  {
    "tokens": {
      "access_token": "eyJhbGciOiJIUzI1NiIsIn...",
      "refresh_token": "bmV3LXJlZnJlc2gtdG9rZW4..."
    }
  }
  ```
- **Side Effects:**
  - Atomically revokes existing refresh token (single-use replay prevention).
  - Issues new access and refresh token pair.
  - Updates auth cookies.
- **Error Responses:**
  - `401 Unauthorized`: Token expired, revoked, or non-existent.

---

### `POST /api/auth/logout`
- **Authentication:** None (Optional session cookies)
- **Success Response:** `200 OK`
  ```json
  {
    "message": "logged out successfully"
  }
  ```
- **Side Effects:**
  - Revokes active refresh token in PostgreSQL database if refresh cookie or header is present.
  - Clears `forgelab_access_token` and `forgelab_refresh_token` cookies (`MaxAge: -1`).

---

### `GET /api/auth/google`
- **Authentication:** None (Public)
- **Authorization:** None
- **Purpose:** Initiates the server-side Google OAuth 2.0 authorization code flow.
- **Workflow:**
  - Verifies that Google OAuth credentials (`GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`) are configured.
  - Generates a cryptographically random 32-byte hexadecimal `state` parameter.
  - Persists `oauth:state:<state>` in Redis with 10-minute TTL.
  - Redirects browser (`302 Found`) to Google's OAuth 2.0 authorization endpoint (`https://accounts.google.com/o/oauth2/v2/auth`) with scopes `openid email profile`.
- **Error Responses:**
  - `503 Service Unavailable`: Google OAuth is not configured on the backend.

---

### `GET /api/auth/google/callback`
- **Authentication:** None (Google callback with query parameters)
- **Query Parameters:**
  - `code`: Authorization code from Google.
  - `state`: Cryptographic state parameter previously generated.
- **Workflow:**
  - Validates `state` against Redis (single-use delete-on-read prevents replay attacks).
  - Exchanges authorization code with Google for tokens via back-channel HTTP POST.
  - Fetches user profile from Google's OpenID `userinfo` endpoint.
  - Identifies user via Google subject ID (`sub`).
  - Finds or creates user record in `users` and records identity link in `auth_identities`.
  - Issues ForgeLAB JWT access and refresh tokens.
  - Sets secure HttpOnly cookies (`forgelab_access_token`, `forgelab_refresh_token`).
  - Redirects browser (`302 Found`) to `${FRONTEND_URL}/dashboard`.
- **Error Responses:**
  - Redirects to `${FRONTEND_URL}/login?error=<message>` on validation, exchange, or CSRF state failure.

---

### `GET /api/auth/github`
- **Authentication:** None (Public)
- **Authorization:** None
- **Purpose:** Initiates the server-side GitHub web application OAuth flow.
- **Workflow:**
  - Verifies that GitHub OAuth credentials (`GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`) are configured.
  - Generates a cryptographically random 32-byte hexadecimal `state` parameter.
  - Persists `oauth:state:<state>` in Redis with 10-minute TTL.
  - Redirects browser (`302 Found`) to GitHub's authorization endpoint (`https://github.com/login/oauth/authorize`) with scopes `read:user user:email`. Note: Does NOT request repository access.
- **Error Responses:**
  - `503 Service Unavailable`: GitHub OAuth is not configured on the backend.

---

### `GET /api/auth/github/callback`
- **Authentication:** None (GitHub callback with query parameters)
- **Query Parameters:**
  - `code`: Authorization code from GitHub.
  - `state`: Cryptographic state parameter previously generated.
- **Workflow:**
  - Validates `state` against Redis (single-use delete-on-read prevents replay attacks).
  - Exchanges authorization code with GitHub for access token via back-channel HTTP POST.
  - Fetches GitHub profile from `https://api.github.com/user`.
  - If primary email is private, fetches verified emails from `https://api.github.com/user/emails`.
  - Identifies user via GitHub account ID (`id`).
  - Finds or creates user record in `users` and records identity link in `auth_identities`.
  - Issues ForgeLAB JWT access and refresh tokens.
  - Sets secure HttpOnly cookies (`forgelab_access_token`, `forgelab_refresh_token`).
  - Redirects browser (`302 Found`) to `${FRONTEND_URL}/dashboard`.
- **Error Responses:**
  - Redirects to `${FRONTEND_URL}/login?error=<message>` on validation, exchange, or CSRF state failure.

---

### `GET /api/auth/me`
- **Authentication:** Required (Bearer JWT or `forgelab_access_token` cookie)
- **Success Response:** `200 OK`
  ```json
  {
    "id": "c3d4e5f6-a7b8-4c1d-9e0f-1a2b3c4d5e6f",
    "email": "developer@example.com",
    "display_name": "Developer",
    "created_at": "2026-09-26T10:00:00Z"
  }
  ```
- **Error Responses:**
  - `401 Unauthorized`: Invalid or missing token.

---

## 4. Project Management Endpoints (`/api/projects`)

### `POST /api/projects`
- **Authentication:** Required (Bearer JWT)
- **Request Body:**
  ```json
  {
    "name": "Production API",
    "repository_path": "C:\\dev\\projects\\my-service",
    "branch": "main",
    "dockerfile_path": "Dockerfile",
    "build_context": ".",
    "health_check_path": "/health"
  }
  ```
- **Validation & Side Effects:**
  - Validates `repository_path` with `PathValidator`: verifies path exists, is a directory, resolves symlinks, rejects system directories, and enforces `FORGELAB_ALLOWED_SOURCE_ROOTS`.
  - Generates unique slug per user (`owner_id, slug`).
  - Sets initial project status to `inactive`.
- **Success Response:** `201 Created`
  ```json
  {
    "id": "a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d",
    "owner_id": "c3d4e5f6-a7b8-4c1d-9e0f-1a2b3c4d5e6f",
    "name": "Production API",
    "slug": "production-api",
    "source_type": "local",
    "repository_path": "C:\\dev\\projects\\my-service",
    "branch": "main",
    "dockerfile_path": "Dockerfile",
    "build_context": ".",
    "health_check_path": "/health",
    "health_check_enabled": true,
    "status": "inactive",
    "current_deployment_id": null,
    "port": null,
    "created_at": "2026-09-26T10:30:00Z",
    "updated_at": "2026-09-26T10:30:00Z"
  }
  ```
- **Error Responses:**
  - `400 Bad Request`: Invalid path or missing required fields.

---

### `GET /api/projects`
- **Authentication:** Required (Bearer JWT)
- **Authorization:** Returns only projects where `owner_id == user_id`.
- **Success Response:** `200 OK`
  ```json
  [
    {
      "id": "a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d",
      "owner_id": "c3d4e5f6-a7b8-4c1d-9e0f-1a2b3c4d5e6f",
      "name": "Production API",
      "slug": "production-api",
      "source_type": "local",
      "repository_path": "C:\\dev\\projects\\my-service",
      "status": "running",
      "current_deployment_id": "d1e2f3a4-b5c6-7d8e-9f0a-1b2c3d4e5f6a",
      "port": 10005,
      "created_at": "2026-09-26T10:30:00Z",
      "updated_at": "2026-09-26T10:35:00Z"
    }
  ]
  ```

---

### `GET /api/projects/{id}`
- **Authentication:** Required (Bearer JWT)
- **Parameters:** `id` (UUID in URL path)
- **Authorization:** Checks `project.owner_id == user_id`.
- **Success Response:** `200 OK` (Project object).
- **Error Responses:**
  - `404 Not Found`: Project does not exist.
  - `403 Forbidden`: User does not own the project.

---

### `PATCH /api/projects/{id}`
- **Authentication:** Required (Bearer JWT)
- **Request Body (Partial update):**
  ```json
  {
    "name": "Updated API Name",
    "branch": "develop",
    "health_check_path": "/api/v1/health"
  }
  ```
- **Success Response:** `200 OK` (Updated Project object).
- **Error Responses:**
  - `400 Bad Request`: Validation failure.
  - `403 Forbidden`: Ownership mismatch.

---

### `DELETE /api/projects/{id}`
- **Authentication:** Required (Bearer JWT)
- **Authorization:** Checks `project.owner_id == user_id`.
- **Side Effects:**
  - Stops and forcibly removes all associated Docker containers (`forgelab-app-<deployment-id>`).
  - Cascades deletion to `deployments`, `environment_variables`, and `deployment_logs`.
- **Success Response:** `200 OK`
  ```json
  {
    "message": "project deleted successfully"
  }
  ```

---

## 5. Application Lifecycle Endpoints

### `POST /api/projects/{id}/stop`
- **Authentication:** Required (Bearer JWT)
- **Authorization:** Checks project ownership.
- **Side Effects:**
  - Stops active container via Docker SDK (`ContainerStop`).
  - Sets deployment status to `stopped`.
  - Sets project status to `stopped`.
- **Success Response:** `200 OK`
  ```json
  {
    "message": "application stopped"
  }
  ```

---

### `POST /api/projects/{id}/start`
- **Authentication:** Required (Bearer JWT)
- **Side Effects:**
  - Starts existing stopped container (`ContainerStart`).
  - Sets deployment status to `running`.
  - Sets project status to `running`.
- **Success Response:** `200 OK`
  ```json
  {
    "message": "application started"
  }
  ```

---

### `POST /api/projects/{id}/restart`
- **Authentication:** Required (Bearer JWT)
- **Side Effects:**
  - Restarts active container (`ContainerRestart` with 10s timeout).
  - Keeps deployment status as `running`.
- **Success Response:** `200 OK`
  ```json
  {
    "message": "application restarted"
  }
  ```

---

### `POST /api/projects/{id}/rollback`
- **Authentication:** Required (Bearer JWT)
- **Authorization:** Checks project ownership.
- **Conditions:** Requires `project.current_deployment_id != null` and at least one previous successful deployment.
- **Side Effects:**
  - Fetches the most recent successful deployment prior to the current active release.
  - Creates a **NEW** deployment record referencing the previous deployment's `image_tag`.
  - Enqueues the new deployment ID in Redis queue (`forgelab:queue:deployments`).
  - The new deployment runs through container creation, port allocation, and health checking before replacing current.
- **Success Response:** `201 Created` (Returns newly created rollback Deployment object).
- **Error Responses:**
  - `400 Bad Request`: No active deployment or no previous successful deployment to rollback to.
  - `409 Conflict`: Another deployment is already in progress.

---

## 6. Environment Variables & Secrets (`/api/projects/{id}/env`)

### `GET /api/projects/{id}/env`
- **Authentication:** Required (Bearer JWT)
- **Authorization:** Checks project ownership.
- **Success Response:** `200 OK`
  ```json
  [
    {
      "id": "e1f2a3b4-c5d6-7e8f-9a0b-1c2d3e4f5a6b",
      "project_id": "a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d",
      "key": "DATABASE_HOST",
      "value": "localhost",
      "is_secret": false,
      "created_at": "2026-09-26T10:40:00Z",
      "updated_at": "2026-09-26T10:40:00Z"
    },
    {
      "id": "f2a3b4c5-d6e7-8f9a-0b1c-2d3e4f5a6b7c",
      "project_id": "a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d",
      "key": "STRIPE_API_KEY",
      "value": "••••••••",
      "is_secret": true,
      "created_at": "2026-09-26T10:41:00Z",
      "updated_at": "2026-09-26T10:41:00Z"
    }
  ]
  ```
- **Security Rule:** If `is_secret == true`, the `value` field is permanently masked with `••••••••`. Plaintext secrets are never returned over the API.

---

### `POST /api/projects/{id}/env`
- **Authentication:** Required (Bearer JWT)
- **Request Body:**
  ```json
  {
    "key": "STRIPE_API_KEY",
    "value": "sk_test_1234567890abcdef",
    "is_secret": true
  }
  ```
- **Side Effects:**
  - Encrypts `value` using AES-256-GCM with a unique 12-byte nonce before inserting into `environment_variables.encrypted_value`.
- **Success Response:** `200 OK` (Returns created/updated EnvVar object with masked value if secret).

---

### `DELETE /api/projects/{id}/env/{key}`
- **Authentication:** Required (Bearer JWT)
- **Parameters:** `id` (Project UUID), `key` (Variable name, URL-encoded).
- **Success Response:** `200 OK`
  ```json
  {
    "message": "environment variable deleted"
  }
  ```

---

## 7. Deployment Endpoints

### `POST /api/projects/{id}/deployments`
- **Authentication:** Required (Bearer JWT)
- **Authorization:** Checks project ownership.
- **Side Effects:**
  - Verifies no active deployment is in-progress (enforced by DB partial unique index `uq_active_deployment_per_project`).
  - Increments sequential `deploy_number` for the project.
  - Inserts new record in `deployments` with status `queued`.
  - Pushes deployment UUID to Redis list `forgelab:queue:deployments` (`LPUSH`).
  - Sets project status to `deploying`.
- **Success Response:** `201 Created`
  ```json
  {
    "id": "d1e2f3a4-b5c6-7d8e-9f0a-1b2c3d4e5f6a",
    "project_id": "a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d",
    "deploy_number": 1,
    "status": "queued",
    "commit_sha": null,
    "branch": "main",
    "image_tag": "forgelab/a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d:1",
    "container_id": null,
    "started_at": "2026-09-26T11:00:00Z",
    "built_at": null,
    "deployed_at": null,
    "finished_at": null,
    "duration_ms": null,
    "failure_reason": null,
    "created_at": "2026-09-26T11:00:00Z"
  }
  ```
- **Error Responses:**
  - `409 Conflict`: Another deployment is already queued or executing for this project.

---

### `GET /api/projects/{id}/deployments`
- **Authentication:** Required (Bearer JWT)
- **Authorization:** Checks project ownership.
- **Success Response:** `200 OK` (Array of Deployment objects ordered by `deploy_number DESC`).

---

### `GET /api/projects/{id}/deployments/{deploymentId}`
- **Authentication:** Required (Bearer JWT)
- **Authorization:** Checks user ownership of parent project.
- **Success Response:** `200 OK` (Single Deployment object).
- **Error Responses:**
  - `404 Not Found`: Deployment or project not found.

---

### `GET /api/projects/{id}/deployments/{deploymentId}/logs`
- **Authentication:** Required (Bearer JWT)
- **Authorization:** Checks user ownership of parent project.
- **Purpose:** Fetches historical build and runtime logs stored in PostgreSQL.
- **Success Response:** `200 OK`
  ```json
  [
    {
      "id": 101,
      "deployment_id": "d1e2f3a4-b5c6-7d8e-9f0a-1b2c3d4e5f6a",
      "timestamp": "2026-09-26T11:00:02Z",
      "phase": "source",
      "stream": "system",
      "message": "Acquiring source snapshot for project 'Production API'..."
    },
    {
      "id": 102,
      "deployment_id": "d1e2f3a4-b5c6-7d8e-9f0a-1b2c3d4e5f6a",
      "timestamp": "2026-09-26T11:00:05Z",
      "phase": "build",
      "stream": "stdout",
      "message": "Step 1/4 : FROM alpine:latest"
    }
  ]
  ```
- **Security Rule:** Messages have all project secret values redacted (`[REDACTED]`) before database storage.

---

## 8. WebSocket Endpoint (`/api/ws`)

### Connection Handshake
- **URL:** `ws://localhost:8080/api/ws?token=<access_token>`
- **Handshake Authentication:**
  1. Checks `token` query parameter.
  2. Fallback to `Authorization: Bearer <token>` header.
  3. Fallback to `forgelab_access_token` cookie.
- **Protocol:** JSON message frames.

### Client Messages:
1. **Subscribe:**
   ```json
   { "type": "subscribe", "channel": "deployment:<uuid>" }
   ```
2. **Unsubscribe:**
   ```json
   { "type": "unsubscribe", "channel": "deployment:<uuid>" }
   ```
3. **Keepalive:**
   ```json
   { "type": "ping" }
   ```

### Server Messages:
1. **Subscription Confirmation:**
   ```json
   { "type": "subscribed", "channel": "deployment:<uuid>" }
   ```
2. **Live Log Line:**
   ```json
   {
     "type": "log",
     "channel": "deployment:<uuid>",
     "data": {
       "timestamp": "2026-09-26T11:00:10Z",
       "phase": "build",
       "stream": "stdout",
       "message": "Successfully built image"
     }
   }
   ```
3. **Status Change Event:**
   ```json
   {
     "type": "status_change",
     "channel": "deployment:<uuid>",
     "data": {
       "deployment_id": "<uuid>",
       "project_id": "<uuid>",
       "previous_status": "building",
       "new_status": "starting",
       "timestamp": "2026-09-26T11:00:12Z"
     }
   }
   ```
4. **Subscription Rejection:**
   ```json
   {
     "type": "error",
     "code": "UNAUTHORIZED",
     "message": "access denied to this resource"
   }
   ```

---

## 9. Future / Planned Endpoints (Not Implemented in MVP)

The following endpoints were discussed in design architecture but are **not present** in the current MVP codebase:

- `POST /api/auth/github/connect` — GitHub OAuth authorization flow.
- `GET /api/auth/github/repositories` — List authenticated user's GitHub repositories.
- `POST /api/webhooks/github` — Automated push deployment receiver.
- `GET /api/metrics` — OpenTelemetry / Prometheus platform metrics.
- `GET /api/projects/{id}/deployments/{deploymentId}/events` — Event-replay log stream.
