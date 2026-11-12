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
| **Go** | Go 1.26+ | Specified in `backend/go.mod: go 1.26.0` | Backend compilation, migration tool, and host-run execution |
| **Node.js** | Node.js 20+ | Specified in `frontend/package.json` | Frontend Next.js 16.4.6 + React 19 web development |

---

## 2. Containerized Development Environment (Standard)

The primary development workflow runs completely containerized via Docker Compose:

```bash
docker compose up --build
```

The containerized stack includes:
- **`postgres`**: PostgreSQL 16 (`postgres:16-alpine`) database with persistent volume `forgelab_pgdata` and healthcheck (`pg_isready`).
- **`redis`**: Redis 7 (`redis:7-alpine`) queue and pub/sub engine with persistent volume `forgelab_redisdata` and healthcheck (`redis-cli ping`).
- **`migrate`**: Runs `./forgelab-migrate up` once PostgreSQL is healthy, applies all migrations (`000001`–`000027`), and exits cleanly (`restart: "no"`).
- **`backend`**: Starts only after `migrate` completes successfully and `redis` is healthy. Runs the Go control plane, REST API, WebSocket hub, continuous health/self-heal/auto-rollback monitor, runtime metrics sampler, and embedded background deployment worker. Mounted with `/var/run/docker.sock`, persistent volumes (`forgelab_builds`, `forgelab_sources`), and `${FORGELAB_HOST_SOURCE_ROOT:-./}:/host-projects:ro`.
- **`frontend`**: Production multi-stage Next.js 16.4.6 container running on `http://localhost:3000`. Proxies API calls internally to `http://backend:8080` and enforces tightened CSP and security headers (`frontend/next.config.ts`).
- **`caddy`**: Official Caddy 2 (`caddy:2-alpine`) reverse proxy for custom domains and automatic TLS certificates, configured via baseline [`infra/caddy/Caddyfile`](../infra/caddy/Caddyfile), exposing ports `${CADDY_HTTP_PORT_BINDING:-80}:80` and `${CADDY_HTTPS_PORT_BINDING:-443}:443` with persistent volumes `forgelab_caddy_data` (certificates) and `forgelab_caddy_config`, and dynamic Admin API sync (`http://caddy:2019`).

```text
postgres healthy ──► migrate (runs migrations & exits)
                            │
                            ▼
redis healthy ──────► backend healthy (control plane + worker + docker.sock)
                            │                    ▲
                            ▼                    │ Admin API (:2019)
                      frontend (:3000)     caddy (:80 / :443, certs in forgelab_caddy_data)
```

### Local Repository Deployment & Container Filesystem Boundary
When deploying local repositories (`source_type: "local_directory"`):
- The backend's `PathValidator` checks directory paths on the filesystem visible to the backend process.
- **Docker Compose Read-Only Host Mount:** In `docker-compose.yml`, the backend container mounts `${FORGELAB_HOST_SOURCE_ROOT:-./}:/host-projects:ro`.
- **Path Translation:** When the frontend provides a host directory path beneath `FORGELAB_HOST_SOURCE_ROOT`, `PathValidator` automatically and securely translates it to `/host-projects/...` inside the container.
- **Security Boundary:** ForgeLAB strictly verifies that the translated directory is inside configured `FORGELAB_ALLOWED_SOURCE_ROOTS` and is not a restricted system directory.
- **Direct Build Streaming:** Build context is streamed concurrently into Docker with early `.dockerignore` directory pruning. Files are never duplicated to `data/builds/<deployment-id>`.

### Host Local Agent Daemon (`forgelab-agent`)
To import host folders seamlessly from the browser using a native OS folder picker without mounting entire drives into Docker:
- **Windows Startup (`.\start-agent.ps1`):** Run `.\start-agent.ps1` from an interactive PowerShell or Windows Terminal window while signed in to your Windows desktop session.
- **Interactive Desktop Requirement:** The agent **must** run inside the logged-in interactive user's Windows session (`SessionId != 0` and `[Environment]::UserInteractive = true`). Do not run it from a headless Windows service or Session 0, where GUI dialogs cannot open.
- **Linux / macOS Startup (`./start-agent.sh`):** Launch from an active desktop terminal session (`X11`/`Wayland`/`macOS Aqua`).
- **Listener-Bound Readiness:** `forgelab-agent` binds its TCP listener (`127.0.0.1:4142` by default) synchronously via `net.Listen` **before** printing `Ready on http://127.0.0.1:4142`. If port `4142` is busy, it exits non-zero with port conflict diagnostics and does not announce readiness.
- **Docker Independence:** `/api/agent/status` reports `status: "online"` as soon as the agent binds its listener even if Docker Desktop is stopped; Docker health is monitored asynchronously in the background and exposed separately via `docker_status` (`checking`, `available`, `unavailable`). Folder picking works immediately without waiting for Docker, while Docker Desktop only needs to be running before deployment.

---

## 3. Host Port Allocations & Authoritative Range

| Service / Capability | Port / Range | Protocol | Notes |
| :--- | :--- | :--- | :--- |
| **Frontend UI** | `3000` | HTTP | Browser entry point (`http://localhost:3000`, `127.0.0.1:3000` by default) |
| **Backend REST & WS** | `8080` | HTTP/WS | API server and WebSocket endpoint (`http://localhost:8080`, `127.0.0.1:8080` by default) |
| **Local Agent Daemon (`forgelab-agent`)** | `4142` | HTTP | Host-native folder picker & context streamer (`http://127.0.0.1:4142`) |
| **Caddy Custom-Domain Ingress** | `80` / `443` | HTTP/HTTPS | Bundled `caddy` service (`CADDY_HTTP_PORT_BINDING` / `CADDY_HTTPS_PORT_BINDING`; internal Admin API `:2019`) |
| **Public Gateway (`cmd/gateway`, optional)** | `8082` / `8443` | HTTP/TLS-Yamux | Optional self-hosted sharing gateway HTTP ingress (`:8082`) and Yamux tunnel listener (`:8443`) |
| **PostgreSQL Database** | `5432` | TCP | Relational database access (`127.0.0.1:5432`) |
| **Redis Queue & PubSub**| `6379` | TCP | Background job coordination (`127.0.0.1:6379`) |
| **Deployed User Applications** | **`10000–60000`** | HTTP/TCP | **Authoritative dynamic port range** (bound to `FORGELAB_APP_HOST_IP`, default `127.0.0.1`) |

---

## 4. Environment Variables Reference

Configuration is managed via `.env` with fallback defaults documented in [`.env.example`](../.env.example), [`docker-compose.yml`](../docker-compose.yml), and [`backend/internal/config/config.go`](../backend/internal/config/config.go):

> [!NOTE]
> **`FORGELAB_LOCAL_BUILD_MODE` Default Difference:** When running the Go binary directly without setting `FORGELAB_LOCAL_BUILD_MODE`, `config.go` defaults to `snapshot` for extra build isolation. Both `.env.example` and `docker-compose.yml` explicitly set `FORGELAB_LOCAL_BUILD_MODE=direct` for zero-copy streaming builds.

| Variable | Default Value | Description |
| :--- | :--- | :--- |
| `APP_ENV` | `development` | Application environment (`development` or `production`; aliases `FORGELAB_ENV`, `ENV`; enforces strict security validation when `production`/`staging`) |
| `BACKEND_PORT_BINDING` | `127.0.0.1:8080` | Host port/IP binding for backend Docker Compose service (defaults to loopback) |
| `FRONTEND_PORT_BINDING` | `127.0.0.1:3000` | Host port/IP binding for frontend Docker Compose service (defaults to loopback) |
| `SERVER_HOST` | `0.0.0.0` | Bind address for Go API server |
| `SERVER_PORT` | `8080` | Bind port for Go API server |
| `DATABASE_URL` | `postgres://forgelab:forgelab_dev_password@localhost:5432/forgelab?sslmode=disable` | PostgreSQL connection DSN (Compose overrides host to `postgres:5432`) |
| `REDIS_URL` | `redis://localhost:6379/0` | Redis connection URL (Compose overrides host to `redis:6379/0`) |
| `JWT_SECRET` | `""` (`forgelab-dev-jwt-secret-change-in-production` in Compose/`.env.example`) | Secret key for signing HS256 JWTs (`>= 32` chars + entropy check in production) |
| `JWT_ACCESS_TOKEN_EXPIRY` | `48h` | Access token lifespan (e.g. `48h`, `2d`, or minutes) |
| `JWT_REFRESH_TOKEN_EXPIRY` | `14d` | Refresh token lifespan (e.g. `14d`, `30d`, `2w`) |
| `FORGELAB_ENCRYPTION_KEY` | `dGhpcy1pcy1hLWRldi1rZXktY2hhbmdlLWluLXByb2Q=` | 32-byte Base64 key for AES-256-GCM secret, token, and webhook secret encryption |
| `FORGELAB_WORK_DIR` | `./data/builds` (`/app/data/builds` in Compose) | Working directory where source snapshots are staged |
| `FORGELAB_SOURCES_DIR` | `./data/sources` (`/app/data/sources` in Compose) | Working directory where uploaded source archives and GitHub imports are extracted |
| `FORGELAB_ALLOWED_SOURCE_ROOTS` | `""` | Comma-separated allowed host roots for local directory imports (required in production) |
| `FORGELAB_HOST_SOURCE_ROOT` | `""` | Host projects directory mounted into backend container (`${FORGELAB_HOST_SOURCE_ROOT:-./}:/host-projects:ro`) |
| `FORGELAB_CONTAINER_SOURCE_ROOT` | `/host-projects` | Read-only container mount point for host projects |
| `FORGELAB_LOCAL_BUILD_MODE` | `snapshot` (`direct` in `.env.example`/Compose) | Build mode for local directories (`direct` or `snapshot`) |
| `FORGELAB_IN_DOCKER` | `""` (`"true"` in Compose) | Signals containerized execution for internal Docker/Caddy upstream host defaults |
| `FORGELAB_AGENT_URL` | `http://127.0.0.1:4142` (`http://host.docker.internal:4142` in Compose) | Candidate Local Agent daemon base URL |
| `FORGELAB_AGENT_SESSION_TTL` | `15m` | Expiration duration for short-lived Local Agent session tokens |
| `FORGELAB_MAX_CONCURRENT_BUILDS` | CPU-aware (`1`/`2`/`<=4`) | Global cap on concurrent Docker builds/deploys (alias `FORGELAB_MAX_DOCKER_BUILDS`) |
| `FORGELAB_QUEUE_WORKERS` | `4` | Number of concurrent Redis deployment queue worker goroutines |
| `FORGELAB_IMAGE_PRUNE_INTERVAL` | `1h` | Background Docker image & build workspace garbage-collection interval (alias `FORGELAB_PRUNE_INTERVAL`) |
| `FORGELAB_PRUNE_FILTER_LABEL` | `""` | Optional Docker label filter scoping image pruning |
| `FRONTEND_URL` | `http://localhost:3000` | Frontend public URL for OAuth redirect landing & CORS allowlist |
| `BACKEND_INTERNAL_URL` | `http://backend:8080` | Next.js internal SSR/API proxy target URL |
| `NEXT_PUBLIC_APP_URL` | `http://localhost:3000` | Public UI URL included in frontend CSP `connect-src` |
| `NEXT_PUBLIC_BACKEND_PORT` | `8080` | Optional backend loopback port included in frontend CSP `connect-src` |
| `CORS_ALLOWED_ORIGINS` | `http://localhost:3000` | Permitted browser origins for CORS headers & WebSocket origin verification |
| `COOKIE_SECURE` | `false` | Set `true` (enforced in production) for `Secure` cookies and frontend HSTS header |
| `ENABLE_HSTS` | `false` | Explicitly enable `Strict-Transport-Security` header in `next.config.ts` |
| `FORGELAB_ALLOW_GUEST_MODE` | `true` in dev / `false` in prod (`false` in Compose/`.env.example`) | Enable or disable registration-free local Guest Mode |
| `FORGELAB_PUBLIC_HOST` | `""` | Optional public hostname used for generated preview URLs (alias `PUBLIC_HOST`) |
| `FORGELAB_APP_HOST_IP` | `127.0.0.1` | Host IP where published application container ports bind (`10000–60000`) |
| `CADDY_ADMIN_URL` | `""` (`http://caddy:2019` in Compose) | Caddy Admin API URL; when empty, verified domains report `tls_status: "proxy_unconfigured"` |
| `FORGELAB_INGRESS_HOST` | `localhost` | Public hostname/IP displayed in custom-domain DNS CNAME/A instructions |
| `FORGELAB_CADDY_TARGET_HOST` | `FORGELAB_APP_HOST_IP` (`host.docker.internal` in Compose) | Upstream host dialed by Caddy when proxying custom domains to published service host ports |
| `CADDY_HTTP_PORT_BINDING` | `80` | Host port binding for Caddy HTTP ingress |
| `CADDY_HTTPS_PORT_BINDING` | `443` | Host port binding for Caddy HTTPS ingress |
| `ACME_EMAIL` | `""` | Optional Let's Encrypt / ACME account email for custom domain certificates |
| `FORGELAB_RUNTIME_HEALTH_CHECK_INTERVAL` | `30s` | Background container health, crash, and restart-loop inspection interval |
| `FORGELAB_AUTO_ROLLBACK_ENABLED` | `true` | Automatically roll back service to previous healthy release after sustained unhealthy checks or crash loops |
| `FORGELAB_AUTO_ROLLBACK_THRESHOLD` | `3` | Consecutive unhealthy Docker health-check sweeps before triggering automatic rollback |
| `FORGELAB_AUTO_ROLLBACK_COOLDOWN` | `5m` | Minimum cooldown between automatic rollback attempts per service (`0s` disables) |
| `FORGELAB_SELF_HEAL_ENABLED` | `true` | Enable bounded self-healing container restarts on unexpected crashes |
| `FORGELAB_SELF_HEAL_MAX_RESTARTS` | `3` | Maximum self-heal container restarts before failing/rolling back (`0` disables restarts) |
| `FORGELAB_CRASH_LOOP_THRESHOLD` | `3` | Number of crash/restart events within `FORGELAB_CRASH_LOOP_WINDOW` defining a crash loop |
| `FORGELAB_CRASH_LOOP_WINDOW` | `10m` | Sliding time window for crash-loop detection |
| `FORGELAB_METRICS_CACHE_TTL` | `10s` | Backend TTL cache for live runtime metrics (`0s` disables caching) |
| `FORGELAB_METRICS_SAMPLE_INTERVAL` | `15s` | Background runtime metrics sampling interval for historical graphs (`0s` disables sampler) |
| `FORGELAB_METRICS_RETENTION_DAYS` | `7` | Retention window in days for historical metrics samples (`0` disables pruning) |
| `RATE_LIMIT_ENABLED` | `true` | Enable Redis/in-memory request throttling |
| `RATE_LIMIT_AUTH_PER_MINUTE` | `30` | Per-client rate limit for `/api/auth/*` endpoints |
| `RATE_LIMIT_SOURCE_PER_MINUTE` | `60` | Per-client rate limit for `/api/sources/*` & `/api/discovery/plan` |
| `RATE_LIMIT_DEPLOY_PER_MINUTE` | `60` | Per-client rate limit for deployment/rollback endpoints |
| `RATE_LIMIT_WS_CONN_PER_MINUTE` | `120` | Per-client rate limit for WebSocket handshake/ticket requests |
| `RATE_LIMIT_WS_SUB_PER_MINUTE` | `120` | Per-client WebSocket channel subscription limit per minute |
| `RATE_LIMIT_TRUSTED_PROXIES` | `127.0.0.1,::1` (required in prod) | Comma-separated trusted proxy IPs/CIDRs for safe `X-Forwarded-For` parsing |
| `FORGELAB_SHARING_ENABLED` | `false` (`true` in Compose/`.env.example`) | Enable outbound public preview sharing |
| `FORGELAB_SHARING_PROVIDER` | `cloudflare` | Public sharing provider (`cloudflare` Quick Tunnels or `gateway` self-hosted tunnel) |
| `FORGELAB_CLOUDFLARED_PATH` | `cloudflared` | Path to `cloudflared` binary when `FORGELAB_SHARING_PROVIDER=cloudflare` |
| `GATEWAY_TUNNEL_URL` | `""` | Outbound Yamux tunnel endpoint when `FORGELAB_SHARING_PROVIDER=gateway` |
| `GATEWAY_PUBLIC_URL` | `""` | Public HTTPS base URL served by the custom gateway |
| `GATEWAY_AUTH_TOKEN` | `""` | Shared gateway authentication token (alias `GATEWAY_AUTH_SECRET`; `>= 16` bytes on gateway) |
| `GATEWAY_TLS_SKIP_VERIFY` | `false` | Dev-only flag to skip TLS verification on gateway tunnel (forbidden in production) |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` / `GOOGLE_REDIRECT_URL` | `""` / `""` / `http://localhost:3000/api/auth/google/callback` | Google OAuth 2.0 Web Client credentials and callback URL |
| `GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET` / `GITHUB_REDIRECT_URL` / `GITHUB_REPO_REDIRECT_URL` | `""` / `""` / `.../auth/github/callback` / `.../integrations/github/callback` | GitHub OAuth 2.0 credentials for Sign-In and repository fallback |
| `GITHUB_APP_ID` / `GITHUB_APP_PRIVATE_KEY` / `GITHUB_APP_PRIVATE_KEY_PATH` / `GITHUB_APP_SLUG` / `GITHUB_APP_WEBHOOK_SECRET` / `GITHUB_APP_CLIENT_ID` / `GITHUB_APP_CLIENT_SECRET` | `""` | GitHub App configuration for short-lived installation tokens & repository access |
| `LOG_LEVEL` | `debug` (`debug` forbidden in prod) | Logging verbosity (`debug`, `info`, `warn`, `error`) |
| `LOG_FORMAT` | `text` | Backend log output format (`text` or `json`) |
| `FORGELAB_LOG_RETENTION_DAYS` | `30` | Days to keep deployment & runtime logs before pruning |
| `FORGELAB_LOG_MAX_PER_DEPLOYMENT` | `10000` | Maximum stored log rows retained per deployment |
| `FORGELAB_LOG_CLEANUP_INTERVAL` | `1h` | Background interval for deployment log retention pruning |

> [!NOTE]
> **GitHub Developer Settings Callback URL:** In GitHub OAuth Apps, GitHub validates that `redirect_uri` matches the configured Authorization callback URL. When using a single OAuth App for local development, register `http://localhost:3000/api/integrations/github/callback` in GitHub Developer Settings (or use two separate apps for sign-in and repository import).

---

## 5. Database Initialization & Automatic Migrations

ForgeLAB uses `golang-migrate` for SQL schema versioning. Migrations live in [`backend/migrations/`](../backend/migrations/) (`000001` through `000027`):
- **`000001`–`000006`:** Core schema (`users`, `projects`, `deployments`, `environment_variables`, `deployment_logs`, `refresh_tokens`), single-active-deployment unique index, OAuth `auth_identities`, build strategy/health strategy columns, `github_integrations`, and isolated `sources` workspaces.
- **`000007`–`000012`:** Multi-service architecture, per-service deployments & resource limits, rollback image/commit persistence, Redis queue lease tracking, and AES-GCM encrypted agent session tokens.
- **`000013`–`000018`:** Deployment rollback persistence, `deployment_plan` JSONB + Compose network/health `depends_on` conditions, installation `GuestUserID` (`00000000-0000-0000-0000-000000000001`), and log query performance indexes.
- **`000019`–`000022`:** Durable GitHub App installations & `repository_imports` job state (`000019`), and `public_shares` with provider selection and single-active share uniqueness (`000020`–`000022`).
- **`000023`–`000027`:** Opt-in `blue_green` service deployment mode (`000023`), `custom_domains` (`000024`), push-to-deploy `auto_deploy`/`encrypted_webhook_secret`/delivery tracking (`000025`), `service_metrics_samples` time-series table (`000026`), and `audit_logs` operational audit trail (`000027`).

### Automated Execution in Docker Compose
The `migrate` service in `docker-compose.yml` automatically runs on `docker compose up`:
```yaml
migrate:
  image: forgelab-backend:latest
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
| **`repository path does not exist` in Compose backend** | Target host directory is outside `FORGELAB_HOST_SOURCE_ROOT` / `FORGELAB_ALLOWED_SOURCE_ROOTS` | Set `FORGELAB_HOST_SOURCE_ROOT` & `FORGELAB_ALLOWED_SOURCE_ROOTS` to your projects parent folder, use Local Agent (`:4142`), or import via GitHub / archive upload |
| **Domain shows `proxy_unconfigured` / `https_active: false`** | `CADDY_ADMIN_URL` is unset (e.g., host-native run without Caddy) or Caddy container is unreachable | Use bundled Compose (`CADDY_ADMIN_URL=http://caddy:2019`) or run Caddy locally with `:2019` Admin API configured |
| **`port already in use`** | Host port `80`, `443`, `3000`, `4142`, `5432`, `6379`, or `8080` occupied | Terminate conflicting process or override port bindings in `.env` (`CADDY_HTTP_PORT_BINDING`, `BACKEND_PORT_BINDING`, etc.) |
| **`backend fails healthcheck`** | PostgreSQL or Redis not accessible | Check `docker compose logs backend` for connection errors |
| **WebSocket disconnects** | Expired or missing session cookies or invalid ticket/Origin | Re-authenticate or check `CORS_ALLOWED_ORIGINS` / `FRONTEND_URL` alignment |
