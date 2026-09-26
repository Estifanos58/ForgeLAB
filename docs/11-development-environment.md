# ForgeLAB — Development Environment & Operational Guide

**Status:** Current Reference Specification  
**Default Host OS:** Windows / Linux / macOS with Docker Desktop  

---

## Related Documents

- [INSTRUCTION.md](../INSTRUCTION.md) — Operational memory & mandatory agent workflow
- [docs/00-current-state.md](00-current-state.md) — Current state snapshot & matrix
- [docs/05-security-trust-boundaries.md](05-security-trust-boundaries.md) — Docker socket trust model & path boundaries
- [docs/09-api-contract.md](09-api-contract.md) — API endpoints & configuration parameters
- [docs/12-manual-verification.md](12-manual-verification.md) — Manual verification execution guide
- [docs/14-known-limitations.md](14-known-limitations.md) — Known development stack limitations & issues

---

## 1. Required Host Tools

| Tool | Minimum Version | Codebase Configuration | Purpose |
| :--- | :--- | :--- | :--- |
| **Docker Engine & Compose** | Docker 24+, Compose v2+ | Specified in `docker-compose.yml` | Multi-container infrastructure & application container runtime |
| **Go** | Go 1.21+ | Specified in `backend/go.mod: go 1.21` | Backend compilation, migration tool, and host-run execution |
| **Node.js** | Node.js 18+ (20+ recommended) | Specified in `frontend/package.json` | Frontend Next.js 14 web development |

---

## 2. Containerized Development Environment (Standard)

The primary development workflow runs completely containerized via Docker Compose:

```bash
docker compose up --build
```

The containerized stack includes:
- **`postgres`**: PostgreSQL 16 database with persistent volume `forgelab_pgdata` and healthcheck (`pg_isready`).
- **`redis`**: Redis 7 queue and pub/sub engine with persistent volume `forgelab_redisdata` and healthcheck (`redis-cli ping`).
- **`migrate`**: Runs `/app/forgelab-migrate up` once PostgreSQL is healthy, applies all migrations (including `000003_add_auth_identities`), and exits cleanly (`restart: "no"`).
- **`backend`**: Starts only after `migrate` completes successfully and `redis` is healthy. Runs the Go control plane, REST API, WebSocket hub, and embedded background deployment worker. Mounted with `/var/run/docker.sock` for Docker SDK access.
- **`frontend`**: Production multi-stage Next.js 16.3.6 container running on `http://localhost:3000`. Proxies API calls internally to `http://backend:8080`.

```text
postgres healthy ──► migrate (runs migrations & exits)
                            │
                            ▼
redis healthy ──────► backend healthy (control plane + worker + docker.sock)
                            │
                            ▼
                      frontend (Next.js 16.3.6 App Router on :3000)
```

### Local Repository Deployment & Container Filesystem Boundary
When deploying local repositories (`source_type: "local"`):
- The backend's `PathValidator` checks directory paths on the filesystem visible to the backend process.
- Inside the backend container, the container filesystem is isolated; arbitrary host paths (such as `C:\dev\my-app`) are not mounted unless explicitly specified in `docker-compose.yml` volumes.
- For host repository path testing, developers can bind-mount their source projects into the backend container or test via git/archive sources once repository integration is enabled.

---

## 3. Host Port Allocations & Authoritative Range

| Service / Capability | Port / Range | Protocol | Notes |
| :--- | :--- | :--- | :--- |
| **Frontend UI** | `3000` | HTTP | Browser entry point (`http://localhost:3000`) |
| **Backend REST & WS** | `8080` | HTTP/WS | API server and WebSocket endpoint (`http://localhost:8080`) |
| **PostgreSQL Database** | `5432` | TCP | Relational database access |
| **Redis Queue & PubSub**| `6379` | TCP | Background job coordination |
| **Deployed User Applications** | **`10000–60000`** | HTTP/TCP | **Authoritative dynamic port range** |

---

## 4. Environment Variables Reference

Configuration is managed via `.env` with fallback defaults in `.env.example`:

| Variable | Default Value | Description |
| :--- | :--- | :--- |
| `SERVER_HOST` | `0.0.0.0` | Bind address for Go API server |
| `SERVER_PORT` | `8080` | Bind port for Go API server |
| `DATABASE_URL` | `postgres://forgelab:forgelab_dev_password@postgres:5432/forgelab?sslmode=disable` | PostgreSQL connection DSN |
| `REDIS_URL` | `redis://redis:6379/0` | Redis connection URL |
| `JWT_SECRET` | `forgelab-dev-jwt-secret-change-in-production` | Secret key for signing HS256 tokens |
| `JWT_ACCESS_TOKEN_EXPIRY` | `15` | Access token lifespan in minutes |
| `JWT_REFRESH_TOKEN_EXPIRY`| `7` | Refresh token lifespan in days |
| `FORGELAB_ENCRYPTION_KEY` | `dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q=` | 32-byte Base64 key for AES-256-GCM secret encryption |
| `FORGELAB_WORK_DIR` | `/app/data/builds` | Working directory where source snapshots are staged |
| `FORGELAB_ALLOWED_SOURCE_ROOTS` | `""` | Comma-separated allowed host roots for repository imports |
| `FRONTEND_URL` | `http://localhost:3000` | Frontend public URL for OAuth redirect landing |
| `CORS_ALLOWED_ORIGINS` | `http://localhost:3000` | Permitted browser origins for CORS headers |
| `COOKIE_SECURE` | `false` | Set to `true` in production to enforce HTTPS cookies |
| `GOOGLE_CLIENT_ID` | `""` | Google OAuth 2.0 Web Client ID |
| `GOOGLE_CLIENT_SECRET` | `""` | Google OAuth 2.0 Web Client Secret |
| `GOOGLE_REDIRECT_URL` | `http://localhost:3000/api/auth/google/callback` | Callback URL registered with Google Cloud Console |
| `GITHUB_CLIENT_ID` | `""` | GitHub OAuth App Client ID |
| `GITHUB_CLIENT_SECRET` | `""` | GitHub OAuth App Client Secret |
| `GITHUB_REDIRECT_URL` | `http://localhost:3000/api/auth/github/callback` | Callback URL registered with GitHub Developer Settings |
| `LOG_LEVEL` | `debug` | Logging level (`debug`, `info`, `warn`, `error`) |

---

## 5. Database Initialization & Automatic Migrations

ForgeLAB uses `golang-migrate` for SQL schema versioning. Migrations live in `backend/migrations/`:
- `000001_initial_schema.up.sql` (Creates users, projects, deployments, env_vars, logs, refresh_tokens)
- `000002_add_active_deployment_unique_index.up.sql` (Enforces at most one active deployment per project)
- `000003_add_auth_identities.up.sql` (Adds `auth_identities` table, foreign keys, and makes `password_hash` nullable)

### Automated Execution in Docker Compose
The `migrate` service in `docker-compose.yml` automatically runs on `docker compose up`:
```yaml
migrate:
  build:
    context: ./backend
    dockerfile: Dockerfile
  command: ["./forgelab-migrate", "up"]
  depends_on:
    postgres:
      condition: service_healthy
```
This guarantees that all migrations are applied cleanly before the backend process begins serving traffic.

---

## 6. Common Failure Modes & Troubleshooting

| Symptom | Probable Cause | Resolution |
| :--- | :--- | :--- |
| **`Google OAuth is not configured`** | Placeholder or empty `GOOGLE_CLIENT_ID` | Expected behavior when credentials are not yet supplied; add real credentials to `.env` |
| **`GitHub OAuth is not configured`** | Placeholder or empty `GITHUB_CLIENT_ID` | Expected behavior when credentials are not yet supplied; add real credentials to `.env` |
| **`repository path does not exist` in Compose backend** | Running inside container without host directory mounts | Host paths are outside container boundaries; mount target path or test via future git import |
| **`port already in use`** | Host port 5432, 6379, 8080, or 3000 occupied | Terminate conflicting process or update port mapping in `.env` |
| **`backend fails healthcheck`** | PostgreSQL or Redis not accessible | Check `docker compose logs backend` for connection errors |
| **WebSocket disconnects** | Expired or missing session cookies | Re-authenticate via `/login` or check browser cookie permissions |
