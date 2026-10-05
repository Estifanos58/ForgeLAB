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

## 3.5 Discovery & Deployment Plan Endpoints (`/api/discovery`)

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

## 4. Project Management Endpoints (`/api/projects`)

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
---

## 8. Service Lifecycle & Independent Deployment Endpoints

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
- **Success Response:** `200 OK` (Array of DeploymentLog entries scoped to the specific service deployment).

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
- **Authentication:** Required (Bearer JWT)
- **Side Effects:** Restarts container for this service.
- **Success Response:** `200 OK` (`{"status": "running"}`).

---

## 6. Source Management Endpoints (`/api/sources`)

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

## 7. GitHub Integration Endpoints (`/api/integrations/github`)

### `GET /api/integrations/github`
- **Authentication:** Required (Bearer JWT or `forgelab_access_token` cookie)
- **Purpose:** Checks whether the authenticated user has granted repository-access OAuth permissions.
- **Success Response:** `200 OK`
  ```json
  {
    "connected": true,
    "username": "octocat",
    "scopes": ["repo", "read:user"],
    "updated_at": "2026-09-27T08:00:00Z"
  }
  ```
- **When Not Connected:** `200 OK` with `{"connected": false}`.

---

### `POST /api/integrations/github/connect`
- **Authentication:** Required (Bearer JWT or `forgelab_access_token` cookie)
- **Purpose:** Generates a cryptographic state parameter and returns GitHub's OAuth authorize URL requesting `repo,read:user` permissions.
- **Success Response:** `200 OK`
  ```json
  {
    "url": "https://github.com/login/oauth/authorize?client_id=...&redirect_uri=...&scope=repo%2Cread%3Auser&state=..."
  }
  ```

---

### `GET /api/integrations/github/callback`
- **Authentication:** Public callback with `code` and `state` parameters from GitHub.
- **Purpose:** Validates state, exchanges code for access token, fetches GitHub username, encrypts token at rest via AES-256-GCM, and upserts into `github_integrations`.
- **Response:** `302 Found` redirecting to `${FRONTEND_URL}/dashboard?github_connected=true`.

---

### `POST /api/integrations/github/disconnect`
- **Authentication:** Required (Bearer JWT or `forgelab_access_token` cookie)
- **Purpose:** Disconnects repository access and deletes encrypted OAuth token from `github_integrations`.
- **Success Response:** `200 OK`
  ```json
  {
    "message": "github repository integration disconnected successfully"
  }
  ```

---

### `GET /api/integrations/github/repositories`
- **Authentication:** Required (Bearer JWT or `forgelab_access_token` cookie)
- **Query Parameters:**
  - `page`: Page number (default: 1)
  - `per_page`: Repositories per page (default: 30, max: 100)
- **Purpose:** Returns repositories accessible under the user's encrypted GitHub token.
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
    "per_page": 30
  }
  ```
- **Error Responses:**
  - `403 Forbidden`: GitHub repository access not authorized.

---

### `GET /api/integrations/github/repositories/{owner}/{repo}/branches`
- **Authentication:** Required (Bearer JWT or `forgelab_access_token` cookie)
- **Purpose:** Lists branches for an authorized repository.
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

### `GET /api/integrations/github/repositories/{owner}/{repo}/detect`
- **Authentication:** Required (Bearer JWT or `forgelab_access_token` cookie)
- **Query Parameters:**
  - `branch`: Git branch to inspect (default: default branch)
  - `root_dir`: Subdirectory root path (default: ".")
- **Purpose:** Inspects files from the GitHub repository tree to automatically detect runtime, framework, commands, and port before project creation.
- **Success Response:** `200 OK`
  ```json
  {
    "runtime": "python",
    "framework": "fastapi",
    "build_strategy": "auto",
    "suggested_port": 8000,
    "build_command": "pip install -r requirements.txt",
    "start_command": "uvicorn main:app --host 0.0.0.0 --port 8000",
    "health_check_path": "/health",
    "health_strategy": "auto",
    "detected_files": ["requirements.txt", "main.py"]
  }
  ```

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

---

## 8. Source Management Endpoints (`/api/sources`)

### `POST /api/sources/local/validate`
- **Authentication:** Required (Bearer JWT)
- **Purpose:** Fast directory validation and heuristic runtime inspection for direct local-directory deployments. Does NOT transfer project files over HTTP.
- **Request Body:**
  ```json
  {
    "repository_path": "C:\\Users\\username\\Projects\\my-app"
  }
  ```
- **Validation Rules:**
  - `repository_path`: Required non-empty string.
  - Path must exist and resolve to a directory (not a regular file).
  - Must reside under configured `FORGELAB_ALLOWED_SOURCE_ROOTS`.
  - Evaluates canonical symlinks and rejects restricted system directories (`/etc`, `C:\Windows`, etc.).
  - In Docker Compose, translates host path beneath `FORGELAB_HOST_SOURCE_ROOT` to `/host-projects`.
  - Prunes ignored directories (`node_modules`, `.git`, `.next`, cache dirs) during structure scanning.
- **Success Response:** `200 OK`
  ```json
  {
    "valid": true,
    "repository_path": "C:\\Users\\username\\Projects\\my-app",
    "project_name": "my-app",
    "files_count": 42,
    "total_bytes": 1048576,
    "runtime": "node",
    "framework": "nextjs",
    "build_strategy": "dockerfile",
    "dockerfile_path": "Dockerfile",
    "build_context": ".",
    "build_command": "npm run build",
    "start_command": "npm start",
    "suggested_port": 3000,
    "health_strategy": "http",
    "health_check_path": "/"
  }
  ```
- **Error Responses:**
  - `400 Bad Request`: Path is empty, not a directory, or system restricted directory.
  - `403 Forbidden`: Path is outside configured `FORGELAB_ALLOWED_SOURCE_ROOTS`.
  - `404 Not Found`: Path does not exist on the filesystem.

---

### `POST /api/sources/upload`
- **Authentication:** Required (Bearer JWT)
- **Purpose:** Optional fallback archive ingestion (.zip, .tar.gz, .tgz) when direct filesystem access is unavailable.
- **Request Format:** `multipart/form-data` with `archive` file field.
- **Constraints:** Maximum 100 MB uncompressed size limit.
- **Success Response:** `201 Created`
  ```json
  {
    "source_id": "b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e",
    "status": "processing",
    "phase": "finalizing",
    "files_count": 120,
    "total_bytes": 4500000
  }
  ```

---

### `GET /api/sources/{id}`
- **Authentication:** Required (Bearer JWT)
- **Parameters:** `id` (Source workspace UUID).
- **Purpose:** Check processing status and detection results for an uploaded archive source workspace.
- **Success Response:** `200 OK`

---

### `DELETE /api/sources/{id}`
- **Authentication:** Required (Bearer JWT)
- **Parameters:** `id` (Source workspace UUID).
- **Purpose:** Delete an uploaded source workspace from disk and the `source_workspaces` database table.
- **Success Response:** `200 OK`

---

## 9. Realtime WebSocket Protocol (`/api/ws`)

### Connection Establishment:
- **Endpoint:** `GET /api/ws?token=<jwt_access_token>`
- **Upgrade Header:** `Upgrade: websocket`, `Connection: Upgrade`
- **Security:** Verifies JWT signature and claims before socket upgrade.

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

## 10. Future / Planned Endpoints (Not Implemented in MVP)

The following endpoints were discussed in design architecture but are **not present** in the current codebase:

- `POST /api/webhooks/github` — Automated push deployment receiver.
- `GET /api/metrics` — OpenTelemetry / Prometheus platform metrics.
- `GET /api/projects/{id}/deployments/{deploymentId}/events` — Event-replay log stream.


