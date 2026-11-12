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
- Authenticated REST requests require a JWT access token in the `Authorization` header:
  ```http
  Authorization: Bearer <access_token>
  ```
- Browser clients authenticate via secure HttpOnly cookies (`forgelab_access_token` and `forgelab_refresh_token`).
- **Security Hardening (Query Tokens Removed):** Query parameter token authentication (`?token=<jwt>`) is strictly rejected with `401 Unauthorized` across all REST and WebSocket endpoints to prevent token leakage in proxy logs, browser histories, and referrer headers.
- **WebSocket Ticket Authentication:** WebSocket connections may authenticate via:
  1. Secure HttpOnly cookie (`forgelab_access_token`)
  2. Standard HTTP header `Authorization: Bearer <access_token>`
  3. Short-lived single-use tickets: `POST /api/ws/ticket` returns a 60-second single-use ticket consumed via `GET /api/ws?ticket=<uuid>`. Once consumed or expired, the ticket is immediately invalidated.
- Access tokens expire after 48 hours by default (> 1 day, configured via `JWT_ACCESS_TOKEN_EXPIRY`, supporting durations like `48h`, `2d`, or minutes).
- Refresh tokens expire after 14 days by default (> 1 week, configured via `JWT_REFRESH_TOKEN_EXPIRY`, supporting durations like `14d`, `30d`, or days) and use atomic single-use rotation.

### Registration-Free Guest Workspace & Isolated Guest Sessions (Local Deployments)
- When `FORGELAB_ALLOW_GUEST_MODE=true` (or in local development `isDev`), unauthenticated requests to local deployment endpoints (`/api/sources/*`, `/api/discovery/plan`, `/api/projects/*`, and `/api/ws`) use `middleware.AuthOrGuestMiddleware`.
- **Browser Isolation:** Anonymous browser visitors are issued an isolated guest UUID and given a secure HttpOnly cookie `forgelab_guest_session` and header `X-Guest-Session`. The corresponding user record (`guest-<uuid>@forgelab.local`) is automatically provisioned in PostgreSQL. This guarantees anonymous browser users cannot access one another's projects, sources, logs, or secrets.
- **Local CLI Usage:** Local non-browser requests (e.g. `curl` or automated local scripts originating on loopback without browser session headers) resolve to the safe installation-level guest identity `00000000-0000-0000-0000-000000000001` (`guest@forgelab.local`), preserving frictionless local CLI workflows.
- Account-linked GitHub integration endpoints (`/api/integrations/github`, `/connect`, `/token`, `/app/install`, `/orgs`, `/disconnect`, `/repositories`, `/detect`) and `source_type: "github"` require registered accounts and reject guest requests with `403 Forbidden`. Public repository lookup and import endpoints (`GET /api/integrations/github/repositories/{owner}/{repo}`, `/branches`, and `POST|GET /api/integrations/github/imports*` when targeting public repositories) and `source_type: "github_public"` are available to both registered and guest users.
- `GET /api/auth/mode` returns installation guest mode configuration (`guest_mode_enabled`, `allow_guest_mode`, `guest_user_id`).

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

### `GET /ready` & `GET /health/ready`
- **Authentication:** Optional (Public with diagnostics redaction; Trusted Internal / Bearer JWT for full diagnostics)
- **Authorization:** Detailed internal diagnostics (`queue`, `build_queue`, `log_collectors`) require trusted internal access (loopback or trusted private proxies) or valid authentication. External unauthenticated callers receive a redacted response omitting internal diagnostics to prevent information leakage.
- **Purpose:** Full dependency readiness probe checking PostgreSQL, Redis, and Docker Engine.
- **Success Response (Public / External Unauthenticated):** `200 OK`
  ```json
  {
    "status": "ready",
    "timestamp": "2026-10-18T14:30:00Z",
    "dependencies": {
      "postgres": { "status": "up", "latency_ms": 2 },
      "redis": { "status": "up", "latency_ms": 1 },
      "docker": { "status": "up", "latency_ms": 4 }
    }
  }
  ```
- **Success Response (Trusted Internal / Authenticated):** `200 OK`
  ```json
  {
    "status": "ready",
    "timestamp": "2026-10-18T14:30:00Z",
    "dependencies": {
      "postgres": { "status": "up", "latency_ms": 2 },
      "redis": { "status": "up", "latency_ms": 1 },
      "docker": { "status": "up", "latency_ms": 4 }
    },
    "queue": {
      "pending_jobs": 0,
      "processing_jobs": 0,
      "dead_letter_jobs": 0,
      "total_recovered_jobs": 0
    },
    "log_collectors": {
      "active_collectors": 2,
      "total_lines_collected": 1284,
      "total_collection_errors": 0,
      "total_persistence_errors": 0,
      "collectors": [
        {
          "id": "e93ab817-6ff5-408a-b864-16a7f9a1cb01",
          "container_id": "c1a2b3c4d5e6",
          "deployment_id": "78331ea6-cf0b-4616-942e-8d80b895d895",
          "service_deployment_id": "00aead97-d237-4833-b6ef-81d77d30d240",
          "service_name": "backend",
          "started_at": "2026-10-18T14:28:00Z",
          "last_activity": "2026-10-18T14:29:55Z",
          "lines_collected": 850,
          "retries": 0,
          "collection_errors": 0,
          "persistence_errors": 0
        }
      ]
    }
  }
  ```

### `GET /health/queue`
- **Authentication:** Required for external callers (Bearer JWT or trusted internal/loopback network access).
- **Authorization:** External unauthenticated requests are rejected with `401 Unauthorized`.
- **Purpose:** Detailed diagnostics on Redis deployment queue, in-flight leases, and build queue semaphores.
- **Success Response:** `200 OK`

### `GET /health/logs`
- **Authentication:** Required for external callers (Bearer JWT or trusted internal/loopback network access).
- **Authorization:** External unauthenticated requests are rejected with `401 Unauthorized`.
- **Purpose:** Operational observability diagnostics for all active Docker runtime log collectors.
- **Success Response:** `200 OK`
  ```json
  {
    "active_collectors": 2,
    "total_lines_collected": 1284,
    "total_collection_errors": 0,
    "total_persistence_errors": 0,
    "collectors": [
      {
        "id": "e93ab817-6ff5-408a-b864-16a7f9a1cb01",
        "container_id": "c1a2b3c4d5e6",
        "service_deployment_id": "00aead97-d237-4833-b6ef-81d77d30d240",
        "service_name": "backend",
        "started_at": "2026-10-18T14:28:00Z",
        "last_activity": "2026-10-18T14:29:55Z",
        "lines_collected": 850,
        "retries": 0,
        "collection_errors": 0,
        "persistence_errors": 0
      }
    ]
  }
  ```

---

## 3. Authentication Endpoints (`/api/auth`)

### `GET /api/auth/mode`
- **Authentication:** None (Public)
- **Purpose:** Reports whether registration-free Guest Mode is enabled for this installation and exposes the installation fallback `guest_user_id`.
- **Success Response:** `200 OK`
  ```json
  {
    "guest_mode_enabled": true,
    "allow_guest_mode": true,
    "guest_user_id": "00000000-0000-0000-0000-000000000001"
  }
  ```

---

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
    "expires_in": 172800
  }
  ```
- **Side Effects:**
  - Sets HttpOnly cookies: `forgelab_access_token` (default 48h via `JWT_ACCESS_TOKEN_EXPIRY`) and `forgelab_refresh_token` (default 14d via `JWT_REFRESH_TOKEN_EXPIRY`). Access/refresh tokens are not returned in the JSON body.
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
    "expires_in": 172800
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
    "expires_in": 172800
  }
  ```
- **Side Effects:**
  - Atomically revokes existing refresh token (single-use replay prevention with concurrency grace handling).
  - Issues new access and refresh token pair via HttpOnly cookies.
- **Error Responses:**
  - `400 Bad Request`: Missing refresh token.
  - `401 Unauthorized`: Token expired, revoked, or non-existent.

---

### `POST /api/auth/touch`
- **Authentication:** Optional (Access cookie/header, refresh cookie/body, or Guest fallback if enabled)
- **Purpose:** Extends session activity without forcing refresh-token rotation while an access token remains valid; rotates tokens if expired and refresh token is present, or ensures an isolated guest session when `FORGELAB_ALLOW_GUEST_MODE=true`.
- **Success Response:** `200 OK`
  ```json
  {
    "status": "ok",
    "authenticated": true,
    "user_id": "c3d4e5f6-a7b8-4c1d-9e0f-1a2b3c4d5e6f"
  }
  ```
- **Error Responses:**
  - `401 Unauthorized`: No valid session and guest mode is disabled.

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

## 4. Discovery & Deployment Plan Endpoints (`/api/discovery`)

### `POST /api/discovery/plan`
Generates an authoritative repository discovery analysis and immutable `DeploymentPlan` blueprint from any supported source (GitHub, Local Directory, Local Agent, or Archive Upload).

- **Authentication:** Required (Bearer JWT or `forgelab_access_token` cookie)
- **Request Body:**
  ```json
  {
    "source_type": "github",
    "source_reference": "octocat/hello-world",
    "branch": "main",
    "root_dir": ".",
    "repository_path": "/var/projects/local-app",
    "agent_id": "00000000-0000-0000-0000-000000000000",
    "project_id": "00000000-0000-0000-0000-000000000000"
  }
  ```
  - `source_type`: Required. `"github"`, `"local_directory"`, `"local_agent"`, or `"local_upload"`.
  - `source_reference`: Required for `"github"` (`owner/repo`) or `"local_upload"` (upload source UUID) or `"local_agent"` (agent source UUID).
  - `branch`: Optional git branch for `"github"` (defaults to repository default branch). Resolves to exact 40-character commit SHA.
  - `repository_path`: Required for `"local_directory"`. Must reside in configured allowed source roots.
  - `project_id`: Optional existing project UUID used to perform environment-variable conflict checks against existing ForgeLAB secrets.

- **Success Response:** `200 OK`
  ```json
  {
    "discovery": {
      "repository_name": "hello-world",
      "topology": {
        "type": "compose",
        "compose_file_path": "docker-compose.yml",
        "networks": [
          { "name": "backend-net", "driver": "bridge" }
        ],
        "volumes": ["db_data"],
        "env_files": [".env"],
        "root_env_vars": [
          {
            "key": "DATABASE_URL",
            "value": "postgres://user:pass@db:5432/app",
            "is_secret": true,
            "scope": "runtime",
            "source_file": ".env",
            "has_conflict": false,
            "conflict_resolution": "imported",
            "active_value": "source"
          }
        ]
      },
      "primary_strategy": "compose",
      "services": [
        {
          "name": "web",
          "role": "frontend",
          "classification": "application",
          "source_path": "./web",
          "runtime": "nodejs",
          "framework": "nextjs",
          "build_strategy": "dockerfile",
          "dockerfile_path": "Dockerfile",
          "internal_port": 3000,
          "host_port": 3000,
          "public_exposed": true,
          "depends_on": ["api"]
        },
        {
          "name": "db",
          "role": "other",
          "classification": "infrastructure",
          "build_strategy": "image",
          "image": "postgres:16-alpine",
          "internal_port": 5432,
          "public_exposed": false,
          "volumes": [
            { "name": "db_data", "container_path": "/var/lib/postgresql/data" }
          ]
        }
      ],
      "total_files": 38,
      "total_bytes": 1048576,
      "dependencies": {
        "web": ["api"],
        "api": ["db"]
      }
    },
    "plan": {
      "id": "e0b04a9e-1234-5678-9abc-def012345678",
      "source_revision": "4b825dc642cb6eb9a060e54bf8d69288fbee4904",
      "source_type": "github",
      "strategy": "compose",
      "topology": "compose",
      "execution_tiers": [
        ["db"],
        ["api"],
        ["web"]
      ],
      "networks": ["forgelab-net"],
      "volumes": ["forgelab-vol-db_data"],
      "services": [...],
      "public_endpoints": [
        {
          "service_name": "web",
          "port": 3000,
          "type": "public",
          "protocol": "http",
          "address": "0.0.0.0:3000"
        }
      ],
      "internal_endpoints": [
        {
          "service_name": "api",
          "port": 8080,
          "type": "internal",
          "protocol": "http",
          "address": "api:8080"
        },
        {
          "service_name": "db",
          "port": 5432,
          "type": "internal",
          "protocol": "tcp",
          "address": "db:5432"
        }
      ],
      "created_at": "2026-10-05T12:00:00Z"
    }
  }
  ```

---

## 5. Project Management Endpoints (`/api/projects`)

### `POST /api/projects`
- **Authentication:** Required (Bearer JWT or `forgelab_access_token` cookie)
- **Request Body:**
  ```json
  {
    "name": "Production API",
    "source_type": "github",
    "source_reference": "octocat/hello-world",
    "branch": "main",
    "build_strategy": "auto",
    "build_command": "npm run build",
    "start_command": "npm start",
    "runtime_type": "node",
    "internal_port": 3000,
    "health_strategy": "auto",
    "health_check_path": "/health",
    "dockerfile_path": "Dockerfile",
    "build_context": ".",
    "deployment_strategy": "compose",
    "deployment_plan": {
      "strategy": "compose",
      "source_revision": "4b825dc642cb6eb9a060e54bf8d69288fbee4904",
      "execution_tiers": [["db"], ["web"]]
    },
    "services": [
      {
        "name": "web",
        "role": "frontend",
        "classification": "application",
        "build_strategy": "dockerfile",
        "internal_port": 3000,
        "depends_on": ["db"]
      },
      {
        "name": "db",
        "role": "other",
        "classification": "infrastructure",
        "build_strategy": "image",
        "image": "postgres:16-alpine",
        "internal_port": 5432,
        "volumes": [
          { "name": "db_data", "container_path": "/var/lib/postgresql/data" }
        ]
      }
    ]
  }
  ```
- **Validation & Business Rules:**
  - `name`: Required, non-empty. Unique per-user slug is generated.
  - `deployment_strategy`: `"compose"`, `"dockerfile"`, `"auto"`, or `"custom"`. Defaults to `"auto"`.
  - `deployment_plan`: Optional JSON snapshot generated by `/api/discovery/plan`.
  - `services`: Optional array of service definitions. Services support `classification` (`application`, `worker`, `infrastructure`, `job`), `image` (pre-built images), `depends_on` (service DAG), and `volumes` (named persistent mounts).
  - `source_type`: `"local_directory"`, `"local_upload"`, `"local_agent"`, `"github"`, or legacy `"local"`.
  - For `source_type: "local_directory"`:
    - `repository_path`: Required absolute host directory path situated within configured `FORGELAB_ALLOWED_SOURCE_ROOTS`. Validated via `PathValidator`.
  - For `source_type: "local_upload"`:
    - `source_reference`: Required UUID of uploaded source workspace from `/api/sources/upload`.
  - For `source_type: "github"`:
    - `source_reference`: Required (`owner/repo`). Verifies active GitHub integration with encrypted token.
  - For legacy `source_type: "local"`:
    - Automatically mapped to `local_directory` if `repository_path` is specified, or `local_upload` if `source_reference` is specified.
  - `build_strategy`: `"auto"` (multi-stage Docker build generation) or `"dockerfile"` (explicit Dockerfile). Defaults to `"auto"`.
  - `internal_port`: Application container listening port (e.g. 3000, 8000, 8080). Defaults to 8080 if not specified.
  - `health_strategy`: `"auto"`, `"http"`, `"tcp"`, or `"none"`. Defaults to `"auto"`.
  - Sets initial project status to `inactive`.
- **Success Response:** `201 Created`
  ```json
  {
    "id": "a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d",
    "owner_id": "c3d4e5f6-a7b8-4c1d-9e0f-1a2b3c4d5e6f",
    "name": "Production API",
    "slug": "production-api",
    "source_type": "github",
    "source_reference": "octocat/hello-world",
    "repository_path": "",
    "branch": "main",
    "dockerfile_path": "Dockerfile",
    "build_context": ".",
    "build_strategy": "auto",
    "build_command": "npm run build",
    "start_command": "npm start",
    "runtime_type": "node",
    "internal_port": 3000,
    "health_strategy": "auto",
    "health_check_path": "/health",
    "health_check_enabled": true,
    "status": "inactive",
    "current_deployment_id": null,
    "port": null,
    "created_at": "2026-09-27T10:30:00Z",
    "updated_at": "2026-09-27T10:30:00Z"
  }
  ```
- **Error Responses:**
  - `400 Bad Request`: Malformed JSON or input validation failure (e.g. empty project name).
  - `403 Forbidden`: GitHub repository permissions not granted / unauthorized.
  - `409 Conflict`: A project with a similar name already exists for the user.
  - `422 Unprocessable Entity`: Invalid or unavailable source (e.g. missing/expired local source upload).
  - `500 Internal Server Error`: Unexpected server or database failure.

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
    "message": "project deleted"
  }
  ```

---

## 6. Application Lifecycle & Topology Endpoints

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

### `GET /api/projects/{id}/topology`
- **Authentication:** Supported (Bearer JWT or Guest session)
- **Authorization:** Checks project ownership.
- **Description:** Returns the authoritative service connection topology and actionable diagnostics graph for the project. Reuses ForgeLAB's existing discovery and deployment-plan DAG architecture.
- **Success Response:** `200 OK`
  ```json
  {
    "project_id": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
    "project_name": "ecommerce-stack",
    "network_name": "forgelab-net-3fa85f64-5717-4562-b3fc-2c963f66afa6",
    "tiers": [
      ["db", "redis"],
      ["backend"],
      ["frontend"]
    ],
    "services": [
      {
        "id": "e4b3e8c9-...",
        "name": "db",
        "role": "other",
        "classification": "infrastructure",
        "status": "running",
        "internal_hostname": "db",
        "internal_port": 5432,
        "network_name": "forgelab-net-3fa85f64-...",
        "network_aliases": ["db"],
        "health_strategy": "none",
        "health_check_path": "/",
        "depends_on": [],
        "tier": 0
      },
      {
        "id": "f5c4d9e0-...",
        "name": "backend",
        "role": "backend",
        "classification": "application",
        "status": "running",
        "internal_hostname": "backend",
        "internal_port": 8080,
        "host_port": 10042,
        "network_name": "forgelab-net-3fa85f64-...",
        "network_aliases": ["backend"],
        "health_strategy": "http",
        "health_check_path": "/health",
        "depends_on": ["db", "redis"],
        "tier": 1
      }
    ],
    "diagnostics": [
      {
        "severity": "info",
        "category": "dns",
        "service_name": "backend",
        "title": "Internal Service DNS Available",
        "message": "Service reachable internally on forgelab-net-... at backend:8080",
        "action": "Use backend:8080 or BACKEND_URL for inter-service communication within the project network"
      }
    ]
  }
  ```
- **Error Responses:**
  - `401 Unauthorized`: Not authenticated and guest mode disabled.
  - `403 Forbidden`: User does not own the project.
  - `404 Not Found`: Project does not exist.

---

## 7. Public Project Sharing (`/api/projects/{id}/share`)

### `GET /api/projects/{id}/share`
Retrieves public sharing configuration, active provider health, and active share details for a project.

- **Authentication:** Required (User JWT or isolated Guest Session)
- **Rate Limit:** 60 requests/minute
- **Success Response (`200 OK`):**
  ```json
  {
    "configured": true,
    "provider": "cloudflare",
    "tunnel_state": "online",
    "install_guide": "",
    "share": {
      "id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
      "project_id": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
      "owner_id": "1fa85f64-5717-4562-b3fc-2c963f66afa1",
      "share_token": "a1b2c3d4e5f60718293a4b5c6d7e8f90",
      "provider": "cloudflare",
      "public_url": "https://random-words-1234.trycloudflare.com",
      "public_host": "random-words-1234.trycloudflare.com",
      "primary_service_id": "service-uuid-1",
      "local_port": 39421,
      "route_mappings": [
        {
          "path_prefix": "/",
          "service_id": "service-uuid-1",
          "service_name": "frontend"
        },
        {
          "path_prefix": "/api",
          "service_id": "service-uuid-2",
          "service_name": "backend"
        }
      ],
      "status": "active",
      "expires_at": "2026-10-21T18:00:00Z",
      "created_at": "2026-10-20T18:00:00Z",
      "updated_at": "2026-10-20T18:00:00Z"
    },
    "available_services": [
      {
        "id": "service-uuid-1",
        "name": "frontend",
        "role": "frontend",
        "status": "running",
        "host_port": 10041
      },
      {
        "id": "service-uuid-2",
        "name": "backend",
        "role": "backend",
        "status": "running",
        "host_port": 10042
      }
    ]
  }
  ```
- **Tunnel State Values (`tunnel_state`):**
  - `"not_shared"`: Sharing is configured and ready; no public share is currently active.
  - `"checking_prerequisites"`: Provider is verifying binary prerequisites (`cloudflared`).
  - `"starting_tunnel"`: Ephemeral router listener bound and child tunnel process starting.
  - `"connecting"`: Tunnel established; waiting for provider URL discovery and origin readiness.
  - `"online"`: Provider-issued URL validated and origin serving requests.
  - `"disconnected"`: Tunnel process or outbound stream disconnected.
  - `"unavailable"`: Provider unconfigured, binary missing, or disabled.
  - `"expired"`: Share has expired according to its `expires_at` timestamp.
  - `"revoked"`: Share was explicitly revoked.
  - `"failed"`: Tunnel process or gateway registration failed.
- **Prerequisites Missing Response (`200 OK`):**
  When `cloudflared` is not found on `$PATH` or specified binary path:
  ```json
  {
    "configured": false,
    "provider": "cloudflare",
    "tunnel_state": "unavailable",
    "install_guide": "Cloudflare Quick Tunnel requires cloudflared. Install it on your machine:\n- macOS: brew install cloudflared\n- Linux: sudo apt-get install cloudflared\n- Windows: winget install --id Cloudflare.cloudflared\nSee: https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/",
    "message": "cloudflared binary is not installed or not executable"
  }
  ```
- **Error Responses:**
  - `401 Unauthorized`: Not authenticated and guest mode disabled.
  - `403 Forbidden`: User does not own the project.
  - `404 Not Found`: Project not found.

### `POST /api/projects/{id}/share`
Creates a new public share or updates route mappings for an existing active share.
1. Creates an ephemeral local router bound to `127.0.0.1:0`.
2. Starts the supervised `cloudflared` child process (or registers routes with the self-hosted gateway).
3. Captures and validates the provider-issued URL (`https://*.trycloudflare.com`).
4. Verifies origin readiness via HTTP probe.
5. Persists the share record and transitions to `online`.

- **Authentication:** Required (User JWT or isolated Guest Session)
- **Rate Limit:** 30 requests/minute
- **Request Body:**
  ```json
  {
    "primary_service_id": "service-uuid-1",
    "expires_in_hours": 24,
    "route_mappings": [
      {
        "path_prefix": "/",
        "service_id": "service-uuid-1"
      },
      {
        "path_prefix": "/api",
        "service_id": "service-uuid-2"
      }
    ]
  }
  ```
- **Validation Rules:**
  - `primary_service_id` is required, must belong to the project, and the service must be currently `running`.
  - Every `route_mappings` entry must specify a non-empty `path_prefix` starting with `/` without path traversal (`..`).
  - Target service IDs must belong to the project.
  - An automatic `/` mapping to `primary_service_id` is added if omitted.
- **Success Response (`201 Created`):**
  ```json
  {
    "message": "Public share created successfully",
    "share": {
      "id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
      "project_id": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
      "owner_id": "1fa85f64-5717-4562-b3fc-2c963f66afa1",
      "share_token": "a1b2c3d4e5f60718293a4b5c6d7e8f90",
      "provider": "cloudflare",
      "public_url": "https://random-words-1234.trycloudflare.com",
      "public_host": "random-words-1234.trycloudflare.com",
      "primary_service_id": "service-uuid-1",
      "local_port": 39421,
      "status": "active"
    }
  }
  ```
- **Error Responses:**
  - `400 Bad Request`: Invalid payload, unserviceable prefix, or target service not running.
  - `403 Forbidden`: Project or service not owned by user.
  - `404 Not Found`: Project or target service does not exist.
  - `503 Service Unavailable`: Sharing provider missing binary, unconfigured, or tunnel process failed to start.

### `DELETE /api/projects/{id}/share`
Immediately revokes the active public share for the project, terminates any running child tunnel processes, closes the local ephemeral router, and deregisters gateway routes.

- **Authentication:** Required (User JWT or isolated Guest Session)
- **Success Response (`200 OK`):**
  ```json
  {
    "message": "Public share revoked successfully"
  }
  ```
- **Error Responses:**
  - `401 Unauthorized`: Not authenticated.
  - `403 Forbidden`: User does not own the project.
  - `404 Not Found`: No active share found for the project.

---

## 8. Environment Variables & Secrets (`/api/projects/{id}/env`)

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
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Parameters:** `id` (Project UUID), `key` (Variable name, URL-encoded).
- **Query Parameters:** `service_id` (optional Service UUID to scope deletion to a single service).
- **Success Response:** `200 OK`
  ```json
  {
    "message": "environment variable deleted"
  }
  ```

---

### `POST /api/projects/{id}/env/import`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Purpose:** Re-imports detected `.env` variables from the project's configured source workspace into encrypted PostgreSQL storage without duplicating manually overridden keys.
- **Success Response:** `200 OK`
  ```json
  {
    "imported_count": 4,
    "message": "Successfully imported 4 environment variable(s)"
  }
  ```

---

## 9. Deployment Endpoints

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
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Authorization:** Checks user ownership of parent project.
- **Success Response:** `200 OK` (Single Deployment object).
- **Error Responses:**
  - `404 Not Found`: Deployment or project not found.

---

### `POST /api/projects/{id}/deployments/{deploymentId}/cancel`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Authorization:** Checks user ownership of parent project.
- **Purpose:** Cancels a queued or in-flight pre-running release deployment and aborts any active Docker build context.
- **Success Response:** `200 OK`
  ```json
  {
    "message": "deployment cancelled successfully"
  }
  ```
- **Error Responses:**
  - `400 Bad Request`: Deployment is already in a terminal state (`running`/`failed`/`stopped`/etc.).
  - `404 Not Found`: Deployment or project not found.
  - `409 Conflict`: Cannot cancel a deployment that is already `running`.

---

### `GET /api/projects/{id}/deployments/{deploymentId}/services`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Authorization:** Checks user ownership of parent project.
- **Success Response:** `200 OK` (Array of `ServiceDeployment` records belonging to the parent release deployment).

---

### `GET /api/projects/{id}/logs`
- **Authentication:** Required (Bearer JWT)
- **Authorization:** Checks user ownership of parent project.
- **Purpose:** Fetches historical build and runtime logs aggregated across all project services (frontend, backend, databases, workers, and independent services).
- **Query Parameters:**
  - `service_id` (optional): Filter to a specific service UUID.
  - `phase` (optional): Filter phase category: `"all"` (all phases), `"deployment"` (`source`, `build`, `startup`, `health`), `"production"` (`runtime` stdout/stderr), or exact phase name.
  - `cursor` / `after_id` (optional): Int64 log ID cursor. Returns logs with `id > cursor`.
  - `before_id` (optional): Int64 log ID cursor. Returns logs with `id < before_id`.
  - `limit` (optional): Integer (default `500`, max `2000`).
- **Ordering Guarantee:** Returns the most recent matching logs in ascending chronological order (`ORDER BY id ASC`), ensuring latest production logs are never truncated by initial build output.
- **Success Response:** `200 OK`
  ```json
  [
    {
      "id": 101,
      "deployment_id": "d1e2f3a4-b5c6-7d8e-9f0a-1b2c3d4e5f6a",
      "service_id": "b1c2d3e4-f5a6-7b8c-9d0e-1f2a3b4c5d6e",
      "timestamp": "2026-09-26T11:00:02Z",
      "phase": "source",
      "stream": "system",
      "message": "Acquiring source snapshot for project 'Production API'..."
    },
    {
      "id": 102,
      "deployment_id": "d1e2f3a4-b5c6-7d8e-9f0a-1b2c3d4e5f6a",
      "service_id": "b1c2d3e4-f5a6-7b8c-9d0e-1f2a3b4c5d6e",
      "timestamp": "2026-09-26T11:00:05Z",
      "phase": "runtime",
      "stream": "stdout",
      "message": "Server listening on :8080"
    }
  ]
  ```

---

### `GET /api/projects/{id}/deployments/{deploymentId}/logs`
- **Authentication:** Required (Bearer JWT)
- **Authorization:** Checks user ownership of parent project.
- **Purpose:** Fetches historical build and runtime logs for an orchestration release deployment.
- **Query Parameters:**
  - `service_id` (optional): Filter to a specific service UUID.
  - `phase` (optional): `"all"`, `"deployment"`, `"production"`, or specific phase.
  - `cursor` / `after_id` (optional): Int64 log ID cursor.
  - `before_id` (optional): Int64 log ID cursor.
  - `limit` (optional): Integer (default `500`, max `2000`).
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
---

## 10. Service Lifecycle, Resource Limits & Blue-Green Proxy

### `GET /api/projects/{id}/services`
- **Authentication:** Required (Bearer JWT)
- **Authorization:** Checks project ownership.
- **Success Response:** `200 OK` (Array of Service objects).
  ```json
  [
    {
      "id": "b1c2d3e4-f5a6-7b8c-9d0e-1f2a3b4c5d6e",
      "project_id": "a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d",
      "name": "frontend",
      "role": "frontend",
      "source_path": "./frontend",
      "runtime_type": "nextjs",
      "framework": "nextjs",
      "package_manager": "npm",
      "build_strategy": "auto",
      "internal_port": 3000,
      "host_port": 10005,
      "public_exposed": true,
      "health_strategy": "http",
      "health_check_path": "/",
      "health_check_enabled": true,
      "status": "running",
      "container_id": "78a9b0c1d2e3",
      "image_tag": "forgelab/a1b2c3d4/frontend:1",
      "current_service_deployment_id": "e1f2a3b4-5c6d-7e8f-9a0b-1c2d3e4f5a6b",
      "preview_url": "http://localhost:10005",
      "created_at": "2026-09-30T10:00:00Z",
      "updated_at": "2026-09-30T10:05:00Z"
    }
  ]
  ```

---

### `POST /api/projects/{id}/services/{serviceId}/deploy`
- **Authentication:** Required (Bearer JWT)
- **Authorization:** Checks project ownership and service association.
- **Concurrency Rule:** Enforces lock per `service_id` (`uq_active_service_deployment`). A frontend deployment does NOT block a backend deployment.
- **Side Effects:**
  - Creates a new `ServiceDeployment` record with incremented `deploy_number` for that service.
  - Leaves `deployment_id` as `NULL` (service-only deployment).
  - Pushes job with type `service_deployment` to Redis queue.
  - Updates service status to `queued`.
- **Success Response:** `201 Created` (Returns newly created ServiceDeployment object).
  ```json
  {
    "id": "e1f2a3b4-5c6d-7e8f-9a0b-1c2d3e4f5a6b",
    "service_id": "b1c2d3e4-f5a6-7b8c-9d0e-1f2a3b4c5d6e",
    "service_name": "frontend",
    "deploy_number": 2,
    "status": "queued",
    "image_tag": "forgelab/a1b2c3d4/frontend:2",
    "internal_port": 3000,
    "build_strategy": "auto",
    "build_command": "npm run build",
    "start_command": "npm start",
    "runtime_type": "nextjs",
    "created_at": "2026-09-30T11:00:00Z"
  }
  ```
- **Error Responses:**
  - `409 Conflict`: A deployment is already in progress for this specific service.

---

### `POST /api/projects/{id}/services/{serviceId}/rollback`
- **Authentication:** Required (Bearer JWT)
- **Authorization:** Checks project ownership and service association.
- **Execution Mode:** `reuse_image` (strictly reuses prior known-good Docker image).
- **Side Effects & Invariants:**
  - Finds the most recent successful prior deployment for this service strictly preceding the current deployment.
  - Validates that the prior deployment contains an immutable image tag or image digest.
  - **Fail-Closed Guarantee:** If the prior deployment lacks an image reference, or if the Docker daemon cannot verify the immutable image tag/digest, the rollback **fails closed** (`400 Bad Request` or runtime `failed` status) and does NOT silently rebuild from current source.
  - Creates a new `ServiceDeployment` record with `execution_mode: "reuse_image"`, `image_tag`, and snapshotted configuration from the prior known-good release.
  - Enqueues the service deployment job in Redis. If queue enqueue fails, the record is immediately marked `failed` to prevent dangling queued states.
- **Success Response:** `201 Created` (Returns created rollback ServiceDeployment object).
- **Error Responses:**
  - `400 Bad Request`: No previous successful deployment found, or prior deployment has no immutable image to reuse.
  - `409 Conflict`: A deployment is already in progress for this service.
  - `500 Internal Server Error`: Enqueue failure (record marked failed).

---

### `GET /api/projects/{id}/services/{serviceId}/deployments`
- **Authentication:** Required (Bearer JWT)
- **Success Response:** `200 OK` (Array of ServiceDeployment records ordered by `deploy_number DESC`).

---

### `GET /api/projects/{id}/services/{serviceId}/deployments/{deploymentId}`
- **Authentication:** Required (Bearer JWT)
- **Success Response:** `200 OK` (Single ServiceDeployment object).

---

### `GET /api/projects/{id}/services/{serviceId}/deployments/{deploymentId}/logs`
- **Authentication:** Required (Bearer JWT)
- **Query Parameters:**
  - `phase` (optional): `"all"`, `"deployment"`, `"production"`, or specific phase.
  - `cursor` / `after_id` (optional): Int64 log ID cursor.
  - `before_id` (optional): Int64 log ID cursor.
  - `limit` (optional): Integer (default `500`, max `2000`).
- **Success Response:** `200 OK` (Array of DeploymentLog entries scoped to the specific service deployment, sorted chronologically).

---

### `POST /api/projects/{id}/services/{serviceId}/stop`
- **Authentication:** Required (Bearer JWT)
- **Side Effects:** Stops container for this service; recalculates composite project status.
- **Success Response:** `200 OK` (`{"status": "stopped"}`).

---

### `POST /api/projects/{id}/services/{serviceId}/start`
- **Authentication:** Required (Bearer JWT)
- **Side Effects:** Starts container for this service; recalculates composite project status.
- **Success Response:** `200 OK` (`{"status": "running"}`).

---

### `POST /api/projects/{id}/services/{serviceId}/restart`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Side Effects:** Restarts container for this service.
- **Success Response:** `200 OK` (`{"status": "running"}`).

---

### `POST /api/projects/{id}/services/{serviceId}/deployments/{deploymentId}/cancel`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Authorization:** Checks project ownership and service association.
- **Purpose:** Cancels a queued or pre-running independent service deployment and aborts any active Docker build context.
- **Success Response:** `200 OK`
  ```json
  {
    "message": "service deployment cancelled successfully"
  }
  ```
- **Error Responses:**
  - `400 Bad Request`: Service deployment already in a terminal state, or service does not belong to project.
  - `404 Not Found`: Project, service, or deployment not found.
  - `409 Conflict`: Service deployment is already running.

---

### `PATCH /api/projects/{id}/services/{serviceId}/resources`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Authorization:** Checks project ownership and service association.
- **Request Body:**
  ```json
  {
    "cpu_millicores": 500,
    "memory_mb": 512,
    "pids_limit": 256
  }
  ```
- **Validation Rules:**
  - `cpu_millicores`: `100` to `16000` (Guests capped at `1000` millicores / 1 core).
  - `memory_mb`: `64` to `32768` (Guests capped at `1024` MB / 1 GB).
  - `pids_limit`: `16` to `4096`.
  - `ephemeral_storage_mb`: Rejected with `400 Bad Request` on unsupported storage drivers when `> 0`.
- **Success Response:** `200 OK` (Returns updated `Service` object).

---

### `PATCH /api/projects/{id}/services/{serviceId}/deployment-mode`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Authorization:** Checks project ownership and service association.
- **Request Body:**
  ```json
  {
    "deployment_mode": "blue_green"
  }
  ```
- **Validation Rules:**
  - `deployment_mode`: Required. Must be `"standard"` or `"blue_green"`.
- **Success Response:** `200 OK` (Returns updated `Service` object).

---

### `ALL /proxy/services/{serviceId}` & `/proxy/services/{serviceId}/*`
- **Authentication:** None (Service Traffic Proxy Ingress; mounted directly on root router)
- **Purpose:** Routes live HTTP requests through `TrafficRouter` to the active healthy container slot (`blue` or `green`) when `blue_green` zero-downtime deployment mode is in use. Strips the `/proxy/services/{serviceId}` path prefix before forwarding upstream and emits `X-ForgeLAB-Slot: blue|green`.
- **Error Responses:**
  - `400 Bad Request`: Invalid `serviceId` UUID.
  - `502 Bad Gateway` / `503 Service Unavailable`: No active healthy slot route registered for service.

---

## 11. Custom Domains & Caddy HTTPS Lifecycle (`/api/projects/{id}/domains`)

### `GET /api/projects/{id}/domains`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Authorization:** Checks project ownership.
- **Purpose:** Lists custom domains bound to a project with DNS verification instructions and effective HTTPS status.
- **Success Response:** `200 OK` (`[]CustomDomain`; `https_active` is `true` only when `verified == true` and `tls_status == "active"`).
  ```json
  [
    {
      "id": "5c6d7e8f-1111-2222-3333-444455556666",
      "project_id": "a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d",
      "service_id": "b1c2d3e4-f5a6-7b8c-9d0e-1f2a3b4c5d6e",
      "domain": "app.example.com",
      "verification_token": "fl-verify-...",
      "verified": true,
      "tls_status": "active",
      "https_active": true,
      "target_port": 10005,
      "verification_record_name": "_forgelab-verify.app.example.com",
      "verification_record_value": "fl-verify-...",
      "cname_instruction": "CNAME app.example.com -> ingress.example.com",
      "last_checked_at": "2026-11-12T10:00:00Z",
      "created_at": "2026-11-12T09:50:00Z",
      "updated_at": "2026-11-12T10:00:00Z"
    }
  ]
  ```

---

### `POST /api/projects/{id}/domains`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Request Body:**
  ```json
  {
    "domain": "app.example.com",
    "service_id": "b1c2d3e4-f5a6-7b8c-9d0e-1f2a3b4c5d6e"
  }
  ```
- **Success Response:** `201 Created` (Returns created `CustomDomain` with `verified: false`, `tls_status: "pending"`, `https_active: false`, and DNS TXT/CNAME instructions).
- **Error Responses:**
  - `400 Bad Request`: Invalid domain format or service mismatch.
  - `404 Not Found`: Project or target service not found.
  - `409 Conflict`: Domain is already registered.

---

### `POST /api/projects/{id}/domains/{domainId}/verify`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Purpose:** Performs live DNS TXT verification (`_forgelab-verify.<domain>`) and CNAME/A routing checks (for non-local `FORGELAB_INGRESS_HOST` setups), then pushes the domain route into Caddy via `CADDY_ADMIN_URL` when configured.
- **Success Response (DNS Valid):** `200 OK`
  - If Caddy Admin API is configured and route provisioning succeeds: returns `CustomDomain` with `verified: true`, `tls_status: "active"`, `https_active: true`.
  - If DNS passes but `CADDY_ADMIN_URL` is unset: returns `CustomDomain` with `verified: true`, `tls_status: "proxy_unconfigured"`, `https_active: false` (never reports HTTPS as active without real Caddy provisioning).
- **Error Responses:**
  - `404 Not Found`: Project or custom domain not found.
  - `422 Unprocessable Entity`: DNS verification failed (`{"error": "...", "domain": {...}}`).
  - `502 Bad Gateway`: DNS passed, but Caddy Admin API route synchronization returned an error (`{"error": "...", "domain": {...}}`).

---

### `DELETE /api/projects/{id}/domains/{domainId}`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Side Effects:** Removes Caddy route configuration (if Caddy Admin API is configured) and deletes the domain binding.
- **Success Response:** `200 OK` (`{"message": "custom domain removed"}`).

---

## 12. Persistent Volumes & Disk Usage (`/api/projects/{id}/volumes`)

### `GET /api/projects/{id}/volumes`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Authorization:** Checks project ownership.
- **Purpose:** Inspects project-scoped Docker named volumes, mount references (`service_id`, `service_name`, `container_id`, `running`, `target`, `read_only`), active `in_use` state, and Docker daemon disk usage summary (`containers`, `images`, `volumes`, `build_cache`, reclaimable bytes, storage warning).
- **Success Response:** `200 OK` (`ProjectVolumesResponse`).
- **Error Responses:**
  - `503 Service Unavailable`: Docker Engine unavailable.

---

### `POST /api/projects/{id}/volumes/{volumeName}/backup`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Parameters:** `volumeName` (Validated 1–128 char alphanumeric/`._-` identifier; path traversal rejected).
- **Purpose:** Archives the project volume contents from a read-only Alpine helper container (`CapDrop: ["ALL"]`, `NetworkMode: "none"`) into a `.tar.gz` download stream.
- **Success Response:** `200 OK` (`Content-Type: application/gzip`, `Content-Disposition: attachment; filename="..."`).
- **Error Responses:**
  - `400 Bad Request`: Invalid volume name.
  - `404 Not Found`: Project or volume not found.
  - `503 Service Unavailable`: Docker Engine unavailable.

---

### `POST /api/projects/{id}/volumes/{volumeName}/restore`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Request Body:** Raw `.tar.gz` binary stream OR `multipart/form-data` with field `file` or `archive`.
- **Security & Resource Limits:**
  - Rejects restores while any container mounts the volume in a running state (`409 Conflict` / `ErrVolumeInUse`).
  - Enforces strict archive bounds: 100 MB compressed max, 512 MB uncompressed max, and 50,000 files max (`413 Payload Too Large` / `ErrVolumeArchiveTooLarge`).
  - Rejects path traversal (`..`), absolute paths, device nodes (`TypeChar`, `TypeBlock`, `TypeFifo`), and escaping symlinks/hardlinks, and strips `setuid`/`setgid` mode bits (`400 Bad Request` / `ErrInvalidVolumeArchive`).
- **Success Response:** `200 OK`
  ```json
  {
    "volume_name": "db_data",
    "docker_name": "forgelab-vol-a1b2c3d4-db_data",
    "files_restored": 128,
    "bytes_restored": 4194304,
    "archive_bytes": 1048576,
    "message": "Volume restored successfully"
  }
  ```

---

## 13. Push-to-Deploy Webhooks (`/api/projects/{id}/webhook` & `/api/webhooks/github`)

### `GET /api/projects/{id}/webhook`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Authorization:** Checks project ownership.
- **Success Response:** `200 OK` (`WebhookConfigView`):
  ```json
  {
    "auto_deploy": true,
    "auto_deploy_branch": "main",
    "has_webhook_secret": true,
    "webhook_url": "http://localhost:8080/api/webhooks/github?project_id=a1b2c3d4-..."
  }
  ```
- **Security Rule:** `webhook_secret` is never returned on `GET`.

---

### `PATCH /api/projects/{id}/webhook`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Request Body:**
  ```json
  {
    "auto_deploy": true,
    "auto_deploy_branch": "main",
    "regenerate_secret": true
  }
  ```
- **Success Response:** `200 OK` (`WebhookConfigView`). When a secret is generated for the first time or rotated via `regenerate_secret: true`, `webhook_secret` is included **once** in this response and encrypted at rest via AES-256-GCM.

---

### `POST /api/webhooks/github`
- **Authentication:** Public Receiver authenticated via GitHub `X-Hub-Signature-256` HMAC-SHA256 verification.
- **Query Parameters:** `project_id` (optional UUID hint).
- **Headers:** `X-GitHub-Event` (`push` or `ping`), `X-GitHub-Delivery` (replay-protection delivery UUID), `X-Hub-Signature-256` (`sha256=<hex>`).
- **Body Cap:** `1 MiB` (`413 Payload Too Large` if exceeded).
- **Success Responses:**
  - `202 Accepted` (when `push` matches enabled project/branch and enqueues a new deployment):
    ```json
    {
      "ok": true,
      "triggered": true,
      "event": "push",
      "project_id": "a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d",
      "deployment_id": "d1e2f3a4-b5c6-7d8e-9f0a-1b2c3d4e5f6a",
      "commit_sha": "7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b",
      "branch": "main"
    }
    ```
  - `200 OK`: `ping` event, non-matching branch/deleted ref/auto-deploy disabled (`"ignored": true`), or duplicate `X-GitHub-Delivery` (`"duplicate": true`).
- **Error Responses:**
  - `401 Unauthorized`: Invalid/missing HMAC signature or webhook secret unavailable.
  - `409 Conflict`: Active deployment already running for target project.
  - `503 Service Unavailable`: Deployment queue unavailable.

---

## 14. Runtime Metrics, History, Audit Logs & Diagnostics

### `GET /api/projects/{id}/metrics`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Query Parameters:**
  - `refresh` / `force` (`true` to bypass the backend TTL cache `FORGELAB_METRICS_CACHE_TTL` and force a fresh Docker stats sample; concurrent refreshes are deduplicated).
- **Success Response:** `200 OK` (`ProjectRuntimeMetrics`, containing `cached`, `cache_age_seconds`, `docker_available`, aggregated `total_cpu_percent`, `total_memory_used_bytes`, `total_network_rx_bytes`, `total_network_tx_bytes`, and per-service live telemetry).

---

### `GET /api/projects/{id}/metrics/history`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Query Parameters:**
  - `service_id` (optional UUID filter scoped to the project)
  - `hours` (integer `1` to `720`, default `1`)
  - `from` / `to` (optional RFC 3339 timestamps overriding `hours`)
  - `limit` (positive integer, default `240`)
- **Success Response:** `200 OK` (`ProjectMetricsHistoryResponse`, returning `retention_days`, `sample_interval_seconds`, per-service `summaries`, and historical `samples` recorded by the background sampler).

---

### `GET /api/audit-logs` & `GET /api/projects/{id}/audit-logs`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Query Parameters:**
  - `project_id` (only on `/api/audit-logs`)
  - `action` (e.g. `project.delete`, `deployment.rollback`, `domain.verify`, `volume.restore`)
  - `resource_type` (e.g. `project`, `service`, `deployment`, `env_var`, `domain`, `volume`, `repository_import`)
  - `limit` (default `50`, max `200`), `offset` (default `0`)
- **Success Response:** `200 OK` (`{"logs": [...], "limit": 50, "offset": 0}`). Sensitive metadata keys (`value`, `secret`, `password`, `token`, `api_key`, etc.) are recursively scrubbed to `"[REDACTED]"`.

---

### `GET /api/diagnostics` & `GET /api/projects/{id}/diagnostics`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Purpose:** Aggregates actionable operational diagnostics across Docker connectivity & daemon reconciliation state, recent release/service deployment failures, host/container resource exhaustion & Docker disk usage, and stalled/failed repository imports.
- **Success Response:** `200 OK` (`DiagnosticsReport`, returning `scope: "system"|"project"`, `overall_status: "healthy"|"degraded"|"critical"`, `docker`, `deployment_failures`, `resources`, `imports`, and remediable `issues`).

---

## 15. Source Management & Local Agent Endpoints (`/api/sources` & `/api/agent`)

### `POST /api/sources/local/validate`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Purpose:** Validates an approved host directory beneath `FORGELAB_ALLOWED_SOURCE_ROOTS`, performs canonical symlink checking, and runs heuristic runtime detection. Supports synchronous (`<= 2s`) or asynchronous (`"async": true`, returning `202 Accepted` with `session_id`) scanning.
- **Request Body:**
  ```json
  {
    "repository_path": "C:\\Users\\username\\Projects\\my-app",
    "async": false
  }
  ```
- **Success Response:** `200 OK` (or `202 Accepted` when async/long scan) returning `session_id`, `status` (`ready` | `scanning` | `failed`), `valid`, `repository_path`, `project_name`, `files_count`, `total_bytes`, `runtime`, `framework`, `build_strategy`, `suggested_port`, `health_strategy`, `health_check_path`.

---

### `GET /api/sources/local/validate/{sessionId}`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session)
- **Purpose:** Polls scan progress (`status`, `files_scanned`, `total_bytes`, `result`, `error`) for an asynchronous local validation session.
- **Success Response:** `200 OK`.

---

### `POST /api/sources/upload`
- **Authentication:** Required (Bearer JWT or `forgelab_access_token` cookie)
- **Content-Type:** `multipart/form-data`
- **Supported Formats:**
  1. Multiple individual files via `files` field (retaining relative paths from browser directory picker).
  2. Single compressed archive via `archive` field (`.zip`, `.tar.gz`, or `.tgz`).
- **Security & Validation:**
  - Enforces Zip Slip and path traversal protection (`cleanRel` checks).
  - Maximum uncompressed size: 100MB.
  - Automatically skips build artifacts and VCS directories (`node_modules`, `.git`, `.next`, `dist`, `__pycache__`, etc.).
- **Automatic Heuristic Detection:**
  - Inspects unpacked source files using `internal/detector`.
  - Determines `runtime`, `framework`, suggested build/start commands, suggested port, and health check endpoint.
- **Success Response:** `201 Created`
  ```json
  {
    "source_id": "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d",
    "status": "processing",
    "phase": "finalizing",
    "files_count": 42,
    "processed_files": 0,
    "total_bytes": 1420500,
    "processed_bytes": 0,
    "detection": null
  }
  ```
- **Streaming & Lifecycle Architecture:**
  - Directory uploads use Go's sequential `r.MultipartReader()` API directly into `.uploads/<source_id>/` without buffering or writing temporary files to disk.
  - Enforces `http.MaxBytesReader` 105MB request limit and 100MB uncompressed source limit.
  - Upload atomically moves from `.uploads/<source_id>/` to `data/sources/<source_id>/` upon successful stream completion.
  - The endpoint returns `201 Created` immediately after atomic persistence, decoupling network transfer from heuristic detection.
  - Background processing analyzes source runtime/framework and transitions status to `ready` or `failed`.
- **Error Responses:**
  - `400 Bad Request`: Empty upload or invalid archive.
  - `413 Payload Too Large`: Source exceeds 100MB uncompressed limit.
  - `422 Unprocessable Entity`: Path traversal or corrupt archive.

---

### `GET /api/sources/{id}`
- **Authentication:** Required (Bearer JWT or `forgelab_access_token` cookie)
- **Parameters:** `id` (UUID of source workspace)
- **Ownership:** Strictly restricted to the authenticated user that uploaded the source workspace.
- **Success Response:** `200 OK`
  ```json
  {
    "source_id": "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d",
    "status": "ready",
    "phase": "ready",
    "files_count": 42,
    "processed_files": 42,
    "total_bytes": 1420500,
    "processed_bytes": 1420500,
    "runtime": "nodejs",
    "framework": "nextjs",
    "detection": {
      "runtime": "nodejs",
      "framework": "nextjs",
      "build_strategy": "auto",
      "suggested_port": 3000,
      "build_command": "npm run build",
      "start_command": "npm start",
      "health_check_path": "/",
      "health_strategy": "auto",
      "detected_files": ["package.json", "next.config.js"]
    },
    "error": null
  }
  ```
- **Lifecycle Status Values:**
  - `status`: `uploading` | `processing` | `ready` | `failed` | `cancelled`
  - `phase`: `uploading` | `finalizing` | `detecting` | `ready` | `failed` | `cancelled`
- **Error Responses:**
  - `400 Bad Request`: Invalid source UUID format.
  - `401 Unauthorized`: Unauthenticated.
  - `403 Forbidden`: Authenticated user does not own this source workspace.
  - `404 Not Found`: Source workspace not found.

---

### `DELETE /api/sources/{id}`
- **Authentication:** Required (Bearer JWT or `forgelab_access_token` cookie)
- **Parameters:** `id` (UUID of source workspace)
- **Success Response:** `200 OK`
  ```json
  {
    "message": "source workspace deleted successfully"
  }
  ```

---

### `POST /api/sources/agent/session`
- **Authentication:** Required (Bearer JWT, cookie, or Guest session; requires existing user record)
- **Purpose:** Issues a signed short-lived Local Agent session token (`24h` TTL) bound to the authenticated user and optional `agent_id`.
- **Request Body:**
  ```json
  {
    "agent_id": "laptop-macos"
  }
  ```
- **Success Response:** `201 Created`
  ```json
  {
    "session_id": "1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d",
    "token": "sess_tok_991823ab",
    "agent_id": "laptop-macos",
    "expires_at": "2026-10-02T12:00:00Z"
  }
  ```

---

### `POST /api/sources/agent/register`
- **Authentication:** Required (Bearer JWT or `forgelab_access_token` cookie)
- **Purpose:** Registers an active local agent session as an authenticated source.
- **Request Body:**
  ```json
  {
    "agent_id": "laptop-macos",
    "token": "sess_tok_991823ab",
    "source_reference": "2b682ad9-91c8-45a8-a007-8ebf77ce3969",
    "fingerprint": "a94a8fe5ccb19ba61c4c0873d391e987982fbbd3",
    "metadata": {
      "folder_name": "my-express-app",
      "services_count": 1
    }
  }
  ```
- **Security & Storage Invariants:**
  - The backend verifies the session token against the agent session manager or validator.
  - The verified token is encrypted using AES-256-GCM and stored in `sources.encrypted_session_token`.
  - The plain session token is strictly removed from metadata, logs, WebSocket events, and HTTP response bodies.
  - The encrypted credential is only accessible to the source owner during deployment execution.
- **Success Response:** `201 Created`
  ```json
  {
    "id": "2b682ad9-91c8-45a8-a007-8ebf77ce3969",
    "owner_id": "c3d4e5f6-a7b8-4c1d-9e0f-1a2b3c4d5e6f",
    "source_type": "local_agent",
    "source_reference": "2b682ad9-91c8-45a8-a007-8ebf77ce3969",
    "agent_id": "laptop-macos",
    "fingerprint": "a94a8fe5ccb19ba61c4c0873d391e987982fbbd3",
    "metadata": {
      "folder_name": "my-express-app",
      "services_count": 1
    },
    "created_at": "2026-10-01T12:00:00Z",
    "updated_at": "2026-10-01T12:00:00Z"
  }
  ```
- **Error Responses:**
  - `400 Bad Request`: Missing agent ID, source reference, or session token.
  - `401 Unauthorized`: Unauthenticated user or agent rejected the session token.

---

### `POST /api/sources/agent/session/validate`
- **Authentication:** None / Internal Agent Auth
- **Purpose:** Allows local agent instances to validate session tokens against the central control plane.
- **Request Body:**
  ```json
  {
    "token": "sess_tok_991823ab",
    "agent_id": "laptop-macos"
  }
  ```
- **Success Response:** `200 OK`
  ```json
  {
    "valid": true,
    "session_id": "1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d",
    "user_id": "c3d4e5f6-a7b8-4c1d-9e0f-1a2b3c4d5e6f",
    "agent_id": "laptop-macos",
    "expires_at": "2026-10-01T14:00:00Z"
  }
  ```

---

### `POST /api/sources/agent/session/revoke`
- **Authentication:** Supported (Bearer JWT or Guest session)
- **Purpose:** Revokes an active agent session immediately by token, session ID, or bound source workspace ID.
- **Request Body:**
  ```json
  {
    "token": "sess_tok_991823ab",
    "session_id": "1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d",
    "source_id": "2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e"
  }
  ```
- **Success Response:** `200 OK`
  ```json
  {
    "revoked": true,
    "message": "agent session revoked"
  }
  ```

---

### Local Agent Daemon: `GET /api/agent/status`
- **Daemon URL:** `http://127.0.0.1:4142` (Loopback default)
- **Authentication:** Optional (Scrubbed response when unauthenticated; full `active_sessions` and identity details when authenticated or matching `X-Expected-Agent-ID`)
- **CORS / Request Headers:** Unauthenticated GET requests omit `Content-Type` so browsers do not issue unnecessary CORS preflight requests.
- **Purpose:** Returns local agent process health independently of Docker Engine availability. Docker availability is probed asynchronously by a background monitor and exposed from cache without blocking `/api/agent/status`.
- **Success Response:** `200 OK`
  ```json
  {
    "status": "online",
    "version": "1.0.0",
    "commit": "dev",
    "os": "windows",
    "arch": "amd64",
    "docker_available": false,
    "docker_status": "unavailable",
    "docker_error": "Docker daemon is unreachable (start Docker Desktop or the Docker service)",
    "docker_checked_at": "2026-11-10T09:00:00Z",
    "docker": {
      "available": false,
      "status": "unavailable",
      "error": "Docker daemon is unreachable (start Docker Desktop or the Docker service)",
      "last_checked_at": "2026-11-10T09:00:00Z"
    },
    "agent_id": "",
    "agent_id_verified": false,
    "active_sessions": 0
  }
  ```
- **Notes:** `docker_status` is `"checking"` immediately after startup until the first background ping completes, then transitions to `"available"` or `"unavailable"`.

---

### Local Agent Daemon: `POST /api/agent/select-folder`
- **Daemon URL:** `http://127.0.0.1:4142`
- **Authentication:** Required (`Authorization: Bearer <session_token>` or `X-Agent-Session-Token: <session_token>`)
- **Purpose:** Launches the host OS native directory picker (Windows STA PowerShell `FolderBrowserDialog` owned by an invisible interactive WinForms window, macOS `osascript`, Linux `zenity`/`kdialog`) from the logged-in user's interactive desktop session and registers the selected directory session.
- **Request Body:**
  ```json
  {
    "title": "Select Project Folder for ForgeLAB"
  }
  ```
- **Success Response (Folder Selected):** `200 OK` returning `AgentSourceSession` metadata (never exposing raw `CanonicalPath`).
- **Success Response (User Cancelled Dialog):** `200 OK`
  ```json
  {
    "cancelled": true
  }
  ```
- **Error Responses:**
  - `400 Bad Request`: Selected directory failed `PathValidator` checks.
  - `401 Unauthorized`: Missing or invalid session token.
  - `408 Request Timeout`: Dialog timed out waiting for user input (`reason: "timeout"`).
  - `409 Conflict`: Another folder picker dialog is already open (`reason: "busy"`).
  - `503 Service Unavailable`: Agent is running in a non-interactive desktop session / Session 0 service (`reason: "non_interactive_session"`).

---

### Local Agent Daemon: `POST /api/agent/select-path`
- **Daemon URL:** `http://127.0.0.1:4142`
- **Authentication:** Required (`Authorization: Bearer <session_token>` or `X-Agent-Session-Token: <session_token>`)
- **Purpose:** Validates an explicit local directory path via `PathValidator` without opening a GUI dialog (used by CLI/automation and tests) and registers the `AgentSourceSession`.
- **Request Body:**
  ```json
  {
    "path": "C:\\Users\\username\\Projects\\my-app"
  }
  ```
- **Success Response:** `200 OK` returning `AgentSourceSession` metadata (never exposing raw `CanonicalPath`).

---

### Local Agent Daemon: `POST /api/agent/verify-identity`
- **Daemon URL:** `http://127.0.0.1:4142` (or `host.docker.internal:4142`)
- **Authentication:** None (Preflight identity verification probe)
- **Purpose:** Allows the ForgeLAB control plane to verify candidate agent endpoint identity before transmitting any bearer credentials or session tokens.
- **Request Body:**
  ```json
  {
    "expected_agent_id": "laptop-macos"
  }
  ```
- **Success Response:** `200 OK`
  ```json
  {
    "verified": true,
    "agent_id": "laptop-macos",
    "agent_name": "My MacBook Pro"
  }
  ```
- **Failure Response:** `200 OK` with `verified: false` or `404 Not Found`.

---

### Local Agent Daemon: `GET /api/agent/sources/{id}/stream-context`
- **Daemon URL:** `http://localhost:4142` (Configurable via `FORGELAB_AGENT_URL`)
- **Authentication:** Required via HTTP header:
  - `Authorization: Bearer <session_token>` OR
  - `X-Agent-Session-Token: <session_token>`
  - *Query parameter tokens (`?token=...`) are explicitly rejected to prevent secret exposure in URL logs.*
- **Query Parameters:**
  - `service_path`: Relative subdirectory within the registered source (default: `.`)
  - `runtime`: Detected runtime (e.g. `nodejs`, `python`, `go`)
  - `port`: Suggested container port
  - `start_cmd`: Optional custom startup command
- **Purpose:** Streams a `.tar` archive of the service directory directly into the Docker build context pipe, pruning files according to `.dockerignore`.
- **Success Response:** `200 OK` (`Content-Type: application/x-tar`, raw streamed tar archive).
- **Error Responses:**
  - `401 Unauthorized`: Missing or invalid session token.
  - `403 Forbidden`: Token does not match the bound source session.
  - `404 Not Found`: Session expired or agent was restarted.

---

## 16. GitHub Integration Endpoints (`/api/integrations/github`)

### `GET /api/integrations/github`
- **Authentication:** Required (Registered account only; Guest sessions rejected with `403 Forbidden`)
- **Purpose:** Checks GitHub connection status, authentication type (`app`, `oauth`, `pat`), connected username, active installations, and reconnection guidance.
- **Success Response:** `200 OK`
  ```json
  {
    "connected": true,
    "username": "octocat",
    "scopes": ["repo", "read:user"],
    "auth_type": "app",
    "app_configured": true,
    "app_slug": "forgelab-ci",
    "installations": [
      {
        "id": 881122,
        "account_login": "my-org",
        "account_type": "Organization",
        "account_avatar_url": "https://avatars.githubusercontent.com/u/123",
        "repository_selection": "all"
      }
    ],
    "needs_reauth": false,
    "reconnect_guidance": ""
  }
  ```
- **When Not Connected:** `200 OK` with `{"connected": false, "app_configured": true, "app_slug": "..."}`.

---

### `GET /api/integrations/github/app/install`
- **Authentication:** Required (Registered account only; Guest sessions rejected with `403 Forbidden`)
- **Purpose:** Generates a secure installation URL for the configured GitHub App.
- **Success Response:** `200 OK`
  ```json
  {
    "url": "https://github.com/apps/forgelab-ci/installations/new?state=..."
  }
  ```

---

### `GET /api/integrations/github/app/callback`
- **Authentication:** Public callback with `installation_id` query parameter from GitHub App installation flow.
- **Purpose:** Verifies server-side installation ownership via GitHub API, persists installation in `github_app_installations`, and redirects to frontend dashboard.
- **Response:** `302 Found` redirecting to `${FRONTEND_URL}/dashboard?github_app_installed=true`.

---

### `GET /api/integrations/github/imports`
- **Authentication:** Supported (Registered or Guest session; returns imports owned by caller)
- **Query Parameters:** `limit` (integer, default `20`).
- **Purpose:** Lists recent repository import jobs (including jobs recovered after backend restarts).
- **Success Response:** `200 OK`
  ```json
  {
    "imports": [ ... ]
  }
  ```

---

### `POST /api/integrations/github/imports`
- **Authentication:** Supported (Registered or Guest; public repos accessible to all)
- **Request Body:**
  ```json
  {
    "repository": "owner/repo",
    "branch": "main",
    "root_dir": "apps/web"
  }
  ```
- **Purpose:** Initiates asynchronous, PostgreSQL-durable import pipeline (`RecoverInterruptedImports` resumes/reconciles in-flight jobs across backend restarts): verifies access, resolves commit SHA, downloads tarball, safely extracts snapshot to `data/sources/imports/<id>`, executes discovery engine, and generates deployment plan.
- **Success Response:** `201 Created`
  ```json
  {
    "id": "e8101b22-b189-49b5-ae04-fc30562371b8",
    "owner_id": "...",
    "source_type": "github",
    "repository_full_name": "owner/repo",
    "is_private": false,
    "branch": "main",
    "commit_sha": "",
    "root_dir": "apps/web",
    "status": "pending",
    "progress": 5,
    "stage": "Initializing import",
    "created_at": "2026-10-18T17:00:00Z"
  }
  ```

---

### `GET /api/integrations/github/imports/{id}`
- **Authentication:** Supported (Registered or Guest)
- **Purpose:** Polls the status, progress, stage, error, discovery result, and deployment plan of an active or completed import.
- **Success Response:** `200 OK`
  ```json
  {
    "id": "e8101b22-b189-49b5-ae04-fc30562371b8",
    "repository_full_name": "owner/repo",
    "is_private": false,
    "branch": "main",
    "commit_sha": "7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b",
    "root_dir": "apps/web",
    "status": "ready",
    "progress": 100,
    "stage": "ready",
    "discovery_result": { "services": [...] },
    "deployment_plan": { "source_revision": "7a8b9c...", "services": [...] },
    "project_id": null,
    "created_at": "2026-10-18T17:00:00Z",
    "expires_at": "2026-10-19T17:00:00Z"
  }
  ```

---

### `POST /api/integrations/github/imports/{id}/cancel`
- **Authentication:** Supported (Registered or Guest)
- **Purpose:** Cancels an in-flight background import, updates durable DB state, and cleans up temporary snapshot workspace. Rejects completed imports with `409 Conflict`.
- **Success Response:** `200 OK` (Returns updated `RepositoryImport` object with `status: "cancelled"`).

---

### `POST /api/integrations/github/imports/{id}/retry`
- **Authentication:** Supported (Registered or Guest)
- **Purpose:** Restarts a failed or cancelled import with fresh durable status and progress tracking (`409 Conflict` if already active or non-retryable).
- **Success Response:** `200 OK` returning updated `RepositoryImport` record.

---

### `POST /api/integrations/github/token`
- **Authentication:** Required (Registered account only; Guest sessions rejected with `403 Forbidden`)
- **Request Body:**
  ```json
  {
    "token": "ghp_... or github_pat_..."
  }
  ```
- **Purpose:** Connects repository access using a Personal Access Token (classic or fine-grained). Validates token against GitHub API and encrypts it at rest via AES-256-GCM.
- **Success Response:** `200 OK`
  ```json
  {
    "connected": true,
    "username": "octocat",
    "scopes": ["repo", "read:user"],
    "auth_type": "pat"
  }
  ```

---

### `GET|POST /api/integrations/github/connect`
- **Authentication:** Required (Registered account only; Guest sessions rejected with `403 Forbidden`)
- **Purpose:** Generates a cryptographic state parameter + nonce cookie and either redirects (`GET`) or returns GitHub's OAuth authorize URL (`POST`) requesting `repo,read:user` permissions.
- **Success Response (`POST`):** `200 OK`
  ```json
  {
    "url": "https://github.com/login/oauth/authorize?client_id=...&redirect_uri=...&scope=repo%2Cread%3Auser&state=..."
  }
  ```

---

### `GET /api/integrations/github/callback`
- **Authentication:** Public callback with `code` and `state` parameters from GitHub plus `forgelab_oauth_nonce_repo` cookie.
- **Purpose:** Validates state and nonce, exchanges code for access token, fetches GitHub username, encrypts token at rest via AES-256-GCM, and upserts into `github_integrations`.
- **Response:** `302 Found` redirecting to `${FRONTEND_URL}/dashboard?github_connected=true`.

---

### `POST /api/integrations/github/disconnect`
- **Authentication:** Required (Registered account only; Guest sessions rejected with `403 Forbidden`)
- **Purpose:** Disconnects repository access and deletes encrypted OAuth token or PAT from `github_integrations`.
- **Success Response:** `200 OK`
  ```json
  {
    "message": "github repository integration disconnected successfully"
  }
  ```

---

### `GET /api/integrations/github/orgs`
- **Authentication:** Required (Registered account only; Guest sessions rejected with `403 Forbidden`)
- **Purpose:** Lists GitHub organizations accessible to the user's connected GitHub account.
- **Success Response:** `200 OK` (`{"organizations": [...]}`).

---

### `GET /api/integrations/github/repositories`
- **Authentication:** Required (Registered account only; Guest sessions rejected with `403 Forbidden`)
- **Query Parameters:**
  - `page`: Page number (default: `1`)
  - `per_page`: Repositories per page (default: `30`, max: `100`)
  - `org`: Optional organization login to list organization-specific repositories
- **Purpose:** Returns repositories accessible under the user's active GitHub App installations or encrypted GitHub token.
- **Success Response:** `200 OK`
  ```json
  {
    "repositories": [
      {
        "id": 1296269,
        "name": "Hello-World",
        "full_name": "octocat/Hello-World",
        "owner": "octocat",
        "private": false,
        "default_branch": "main",
        "description": "My first repository on GitHub!",
        "html_url": "https://github.com/octocat/Hello-World",
        "updated_at": "2026-09-27T07:00:00Z"
      }
    ],
    "page": 1,
    "per_page": 30,
    "has_more": false
  }
  ```
- **Error Responses:**
  - `403 Forbidden`: GitHub repository access not authorized (`not_connected: true`, `needs_reauth: true`, or `sso_required: true`).

---

### `GET /api/integrations/github/repositories/{owner}/{repo}`
- **Authentication:** Supported (Registered or Guest; public repositories accessible without linked GitHub credentials)
- **Purpose:** Fetches metadata for a specific GitHub repository (`id`, `full_name`, `private`, `default_branch`, etc.).
- **Success Response:** `200 OK` (`GitHubRepo` object).

---

### `GET /api/integrations/github/repositories/{owner}/{repo}/branches`
- **Authentication:** Supported (Registered or Guest; public repos accessible without linked GitHub credentials)
- **Purpose:** Lists branches for an authorized or public repository.
- **Success Response:** `200 OK`
  ```json
  {
    "branches": [
      {
        "name": "main",
        "commit_sha": "7fd1a60b01f91b314f59955a4e4d4e80d8edf11d"
      }
    ]
  }
  ```

---

### `GET|POST /api/integrations/github/repositories/{owner}/{repo}/detect`
- **Authentication:** Required (Registered account only; Guest sessions rejected with `403 Forbidden`)
- **Query / JSON Body Parameters:**
  - `branch`: Git branch to inspect (default: repository default branch)
  - `root_dir`: Subdirectory root path (default: `"."`)
- **Purpose:** Inspects files from the GitHub repository tree to detect runtimes, frameworks, services, commands, and ports before project creation.
- **Success Response:** `200 OK`
  ```json
  {
    "source": {
      "source_type": "github",
      "source_reference": "octocat/hello-world",
      "branch": "main",
      "folder_name": "hello-world",
      "root_dir": "."
    },
    "services": [...],
    "total_files": 24,
    "total_bytes": 182400,
    "runtime": "python",
    "framework": "fastapi",
    "build_strategy": "auto",
    "suggested_port": 8000,
    "health_check_path": "/health",
    "health_strategy": "auto",
    "build_command": "pip install -r requirements.txt",
    "start_command": "uvicorn main:app --host 0.0.0.0 --port 8000"
  }
  ```
- **Error Responses:**
  - `404 Not Found`: Repository not found or access denied (`not_found: true`).
  - `403 Forbidden`: Re-authorization required (`needs_reauth: true` or guest session).

---

## 17. Realtime WebSocket Protocol (`/api/ws`)

### `POST /api/ws/ticket`
- **Authentication:** Required (Bearer JWT or `forgelab_access_token` cookie; Guest mode allowed if configured)
- **Purpose:** Request a short-lived (60-second), single-use ticket to establish a WebSocket connection without exposing JWT access tokens in URLs.
- **Request Body:** None
- **Success Response:** `200 OK`
  ```json
  {
    "ticket": "9a01666e-691a-4c28-98e6-e3d8f80164eb",
    "expires_in": 60
  }
  ```
- **Error Responses:**
  - `401 Unauthorized`: Missing or invalid authentication.

---

### Connection Establishment (`GET /api/ws`):
- **Endpoint:** `GET /api/ws` or `GET /api/ws?ticket=<ticket_uuid>`
- **Upgrade Header:** `Upgrade: websocket`, `Connection: Upgrade`
- **Supported Authentication Mechanisms:**
  1. `forgelab_access_token` HttpOnly cookie sent automatically by the browser.
  2. `Authorization: Bearer <access_token>` request header.
  3. `ticket=<uuid>` query parameter using a ticket minted via `POST /api/ws/ticket`.
- **Security & Origin Policy:**
  - `?token=<jwt>` query parameter is **strictly rejected with 401 Unauthorized**.
  - Enforces strict origin checking against configured frontend URLs (`CORS_ALLOWED_ORIGINS` and `FRONTEND_URL`) or same-origin host matching.
  - Tickets are single-use and expire within 60 seconds. Attempting to reuse or pass an expired ticket returns `401 Unauthorized`.
  - Verifies JWT signature and claims before socket upgrade.

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

## 18. Future / Planned Endpoints (Not Yet Implemented)

The following endpoints were discussed in design architecture and remain **unimplemented** in the current codebase:

- `GET /api/metrics` — OpenTelemetry / Prometheus control-plane export endpoint (per-project live and historical metrics are implemented at `GET /api/projects/{id}/metrics` and `GET /api/projects/{id}/metrics/history`).
- `GET /api/projects/{id}/deployments/{deploymentId}/events` — Dedicated event-replay SSE/JSON stream outside the WebSocket/log endpoints.


