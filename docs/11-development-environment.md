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

## 2. Two Distinct Development Environments

To understand ForgeLAB development today, developers must distinguish between two separate runtime environments:

```text
┌────────────────────────────────────────────────────────────────────────┐
│ ENVIRONMENT A: Host-Run Backend (Recommended for Local Repositories)   │
│                                                                        │
│   Docker Compose (Infrastructure Only):                                │
│   • PostgreSQL (5432)                                                  │
│   • Redis (6379)                                                       │
│                                                                        │
│   Host System (Developer Machine):                                     │
│   • Go Backend (8080) running directly on host                         │
│   • Direct access to host filesystem repositories (e.g. C:\dev\app)    │
│   • Direct access to Docker socket (local daemon / Desktop pipe)       │
│   • Next.js Frontend (3000) running via "npm run dev" or Compose       │
└────────────────────────────────────────────────────────────────────────┘

┌────────────────────────────────────────────────────────────────────────┐
│ ENVIRONMENT B: Current Dockerized Backend (Compose Stack)              │
│                                                                        │
│   Docker Compose (Full Stack in Containers):                           │
│   • PostgreSQL (5432)                                                  │
│   • Redis (6379)                                                       │
│   • forgelab-backend (8080) inside Linux container                     │
│   • forgelab-frontend (3000) inside Linux container                    │
│                                                                        │
│   Filesystem Boundary Reality:                                         │
│   • docker-compose.yml mounts ONLY /var/run/docker.sock and            │
│     forgelab_builds:/app/data/builds                                   │
│   • Arbitrary host filesystem repositories are NOT mounted             │
│   • Host paths (e.g. C:\dev\testapp) do NOT exist in the container     │
└────────────────────────────────────────────────────────────────────────┘
```

### Environment A — Host-Run Backend (Current Working Path for Local Repositories)
Because ForgeLAB MVP uses host filesystem paths (`repository_path`), running the Go backend directly on the host machine is the natural execution environment:
- The Go process can read any local repository directory on the developer's filesystem.
- `PathValidator` can inspect, canonicalize, and validate host directories.
- Docker builds operate against Docker Desktop / local Docker daemon via socket.

**Startup sequence for Environment A:**
```bash
# 1. Start database and Redis containers
docker compose up -d postgres redis

# 2. Run migrations from host
cd backend
go run cmd/migrate/main.go up

# 3. Start backend control plane on host
go run cmd/server/main.go

# 4. In a separate terminal, start frontend on host
cd ../frontend
npm install
npm run dev
```

### Environment B — Current Dockerized Backend (Containerized Stack)
Running `docker compose up --build` starts all four services as containers.
- **Important Limitation:** The current `docker-compose.yml` does **not** mount arbitrary host repository directories into the `forgelab-backend` container.
- If a user inputs a host path (such as `C:\dev\testapp` or `/home/user/projects/testapp`), the backend container’s `PathValidator` fails because that directory does not exist within the container’s isolated filesystem.
- To use Environment B for local repository deployments, a specific host directory would need to be mounted into the container (a convention not currently defined in `docker-compose.yml`).
- Furthermore, container-to-container Next.js rewrites (`http://localhost:8080/api/:path*`) require reconciliation (see Section 6 and [docs/14-known-limitations.md](14-known-limitations.md)).

---

## 3. Host Port Allocations & Authoritative Range

| Service / Capability | Port / Range | Protocol | Notes |
| :--- | :--- | :--- | :--- |
| **Frontend UI** | `3000` | HTTP | Browser entry point (`http://localhost:3000`) |
| **Backend REST & WS** | `8080` | HTTP/WS | API server and WebSocket endpoint |
| **PostgreSQL Database** | `5432` | TCP | Relational database access |
| **Redis Queue & PubSub**| `6379` | TCP | Background job coordination |
| **Deployed User Applications** | **`10000–60000`** | HTTP/TCP | **Authoritative dynamic port range** |

### Authoritative Dynamic Port Range
> **Notice:** Older architectural discussions referenced an initial range of `8000–9000`. The **authoritative implementation** (`backend/internal/network/port_manager.go:L19-L23` and `backend/cmd/server/main.go:L98`) allocates host ports from:
> ```text
> 10000 to 60000
> ```
> Each newly deployed container is allocated an open host port in this range and bound to `0.0.0.0:<port>`.

---

## 4. Environment Variables & Compose Configuration Reality

### Configuration Mechanism Distinction
Developers must distinguish between how configuration is loaded in different environments:

1. **Host-Run Backend (Environment A):**
   - The Go backend reads environment variables from the host process environment.
   - You can copy `.env.example` to `.env` and export the variables or rely on fallback defaults defined in `backend/internal/config/config.go`.
   - `FORGELAB_ALLOWED_SOURCE_ROOTS` is actively read and enforced if set in the host environment.
2. **Docker Compose Backend (Environment B):**
   - The backend service in `docker-compose.yml` uses an explicit, inline `environment:` block with static values (`SERVER_PORT: 8080`, `DATABASE_URL: ...`, `REDIS_URL: ...`).
   - `docker-compose.yml` does **not** specify `env_file: .env`.
   - `FORGELAB_ALLOWED_SOURCE_ROOTS` is **not** currently injected into the `forgelab-backend` Compose service. Setting it in `.env` has no effect on the Compose backend container unless added to `docker-compose.yml`.

### Complete Environment Variable Reference

| Variable | Default Value | Description |
| :--- | :--- | :--- |
| `SERVER_HOST` | `0.0.0.0` | Bind address for Go API server |
| `SERVER_PORT` | `8080` | Bind port for Go API server |
| `DATABASE_URL` | `postgres://forgelab:forgelab_dev_password@localhost:5432/forgelab?sslmode=disable` | PostgreSQL connection DSN |
| `REDIS_URL` | `redis://localhost:6379/0` | Redis connection URL |
| `JWT_SECRET` | *(Required)* | Secret key for signing HS256 tokens |
| `JWT_ACCESS_TOKEN_EXPIRY` | `15` | Access token lifespan in minutes |
| `JWT_REFRESH_TOKEN_EXPIRY`| `7` | Refresh token lifespan in days |
| `FORGELAB_ENCRYPTION_KEY` | *(Required)* | 32-byte Base64 key for AES-256-GCM secret encryption |
| `FORGELAB_WORK_DIR` | `./data/builds` | Working directory where source snapshots are staged |
| `FORGELAB_ALLOWED_SOURCE_ROOTS` | `""` (Empty) | Comma-separated allowed host roots for repository imports (Host backend) |
| `DOCKER_HOST` | `""` (Auto-detect) | Docker Engine socket path override |
| `LOG_LEVEL` | `debug` | Logging level (`debug`, `info`, `warn`, `error`) |
| `LOG_FORMAT` | `text` | Log formatter (`text` or `json`) |
| `NEXT_PUBLIC_API_URL` | `http://localhost:8080/api` | API URL consumed by the Next.js frontend |

---

## 5. Local Source Path Security (`FORGELAB_ALLOWED_SOURCE_ROOTS`)

ForgeLAB builds applications from host repository directories. When running the backend on the host, `internal/security/PathValidator` enforces defense-in-depth:

### What `FORGELAB_ALLOWED_SOURCE_ROOTS` Controls
- Defines a whitelist of directories on the host filesystem from which ForgeLAB is permitted to import source repositories.
- **Format:** Comma-separated list of absolute filesystem paths.
  - **Windows Example:** `C:\Users\developer\Projects,D:\Dev`
  - **Linux / macOS Example:** `/home/developer/projects,/opt/code`

### Behavior When Empty
- If `FORGELAB_ALLOWED_SOURCE_ROOTS` is unset or empty, boundary checking against specific root prefixes is bypassed.
- **However, defense-in-depth remains active:**
  1. The path must exist and must be a directory.
  2. Symlinks are fully evaluated and canonicalized (`filepath.EvalSymlinks`) to block directory traversal escapes.
  3. **Dangerous system directories are permanently forbidden**, including:
     - Linux: `/etc`, `/var`, `/usr`, `/sys`, `/proc`, `/dev`, `/boot`, `/bin`, `/sbin`
     - Windows: `C:\Windows`, `C:\Program Files`, `C:\Program Files (x86)`, `C:\System Volume Information`

### Why Local Source is Snapshotted Before Building
When a deployment executes, ForgeLAB **copies the entire repository** from the host source path to an isolated build directory:
```text
Host Path (e.g. C:\dev\repo)  ──► Copy ──►  ./data/builds/<deployment-id>/  ──► Docker ImageBuild
```
**Rationale:**
1. **Build Isolation:** Changes made on the host during a build do not corrupt or invalidate the image build context.
2. **Reproducibility:** The build operates against an immutable point-in-time snapshot.
3. **Container Sandbox:** Containers never mount host source directly; source is copied into the image layer at build time.

---

## 6. Docker Engine Integration & Host Socket

The Go backend requires direct access to Docker Engine to manage image builds and container lifecycles:

- **Socket Mount in Compose:** `/var/run/docker.sock:/var/run/docker.sock` in `docker-compose.yml`.
- **Host Execution:** On Windows with Docker Desktop, the Go backend communicates via the named pipe `npipe:////./pipe/docker_engine` or `DOCKER_HOST`. On Linux/macOS, it connects to `unix:///var/run/docker.sock`.
- **Trust Boundary Warning:** Because the control plane has Docker socket access, anyone with access to execute arbitrary commands inside the backend effectively controls the host Docker daemon. Keep the backend environment secure.

### Container Restart Policy vs. Platform Self-Healing
- **Docker Daemon Restart (`unless-stopped`):** Deployed application containers are created with `RestartPolicy: { Name: "unless-stopped" }`. The Docker daemon itself automatically restarts crashed containers.
- **ForgeLAB Self-Healing (NOT IMPLEMENTED):** ForgeLAB does **not** implement continuous background runtime health monitoring, crash-loop detection, or automated application rollback after promotion.

---

## 7. Database Initialization & Schema Migrations

ForgeLAB uses `golang-migrate` for SQL schema versioning. Migrations live in `backend/migrations/`:
- `000001_initial_schema.up.sql` (Creates users, projects, deployments, env_vars, logs, refresh_tokens)
- `000002_add_active_deployment_unique_index.up.sql` (Enforces at most one active deployment per project)

The backend image builds two separate binaries: `/app/forgelab-server` and `/app/forgelab-migrate`. The migration runner is `forgelab-migrate`.

### Applying Migrations Inside Docker Compose
With the Compose stack running, execute migrations using the dedicated `forgelab-migrate` binary:
```bash
docker compose exec backend /app/forgelab-migrate up
```

### Applying Migrations from Host (Go CLI)
If running Go directly on the host machine:
```bash
cd backend
go run cmd/migrate/main.go up
```

### Database Reset Procedure
To reset the development database to a clean state:
```bash
# Option A: Rollback migrations and re-apply via container
docker compose exec backend /app/forgelab-migrate down
docker compose exec backend /app/forgelab-migrate up

# Option B: Rollback migrations and re-apply via host Go
cd backend
go run cmd/migrate/main.go down
go run cmd/migrate/main.go up

# Option C: Complete volume wipe (Destructive)
docker compose down -v
docker compose up -d postgres redis
docker compose exec backend /app/forgelab-migrate up
```

---

## 8. Common Failure Modes & Troubleshooting

| Symptom | Probable Cause | Resolution |
| :--- | :--- | :--- |
| **`docker client initialization warning`** | Docker daemon not running or socket not accessible | Ensure Docker Desktop is running and socket permissions are valid |
| **`repository path does not exist` in Compose backend** | Running in Environment B without host directory mounts | Run backend on host (Environment A) so local filesystem is accessible |
| **`port already in use`** | Host port 5432, 6379, 8080, or 3000 occupied | Terminate conflicting process or update port mapping in `.env` / compose |
| **`repository path is outside allowed root`** | Path violates `FORGELAB_ALLOWED_SOURCE_ROOTS` | Add the target directory to host environment or clear the variable for dev |
| **`no available host ports in range 10000-60000`** | System firewall or port exhaustion | Verify host networking allows local TCP binds on ports > 10000 |
| **Frontend API calls fail in Compose** | Next.js rewrite `localhost:8080` fails in container | Run frontend on host or access backend directly on published port |
| **WebSocket disconnects immediately** | Invalid JWT token or clock skew | Check browser localStorage token and verify backend JWT_SECRET matches |
