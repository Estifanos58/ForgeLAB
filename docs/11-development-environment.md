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

---

## 1. Required Host Tools

| Tool | Minimum Version | Verified In Codebase | Purpose |
| :--- | :--- | :--- | :--- |
| **Docker Engine & Compose** | Docker 24+, Compose v2+ | Required (`docker-compose.yml`) | Multi-container local stack & application container runtime |
| **Go** | Go 1.21+ | Required (`backend/go.mod: go 1.21`) | Backend compilation, migration tool, and unit tests |
| **Node.js** | Node.js 18+ (20+ recommended) | Required (`frontend/package.json`) | Frontend Next.js 14 web development |

---

## 2. Platform Services & Stack Topology

ForgeLAB uses Docker Compose (`docker-compose.yml`) to orchestrate its core infrastructure:

```text
┌────────────────────────────────────────────────────────────────────────┐
│                        Local Development Machine                       │
│                                                                        │
│   ┌─────────────────────┐                 ┌────────────────────────┐  │
│   │   Next.js Frontend  │                 │    Go Backend API      │  │
│   │   (Port 3000)       │─── HTTP/WS ────►│    (Port 8080)         │  │
│   └─────────────────────┘                 └───────────┬────────────┘  │
│                                                       │               │
│                  ┌────────────────────┬───────────────┴────────┐      │
│                  ▼                    ▼                        ▼      │
│         ┌────────────────┐   ┌────────────────┐   ┌────────────────┐  │
│         │   PostgreSQL   │   │     Redis      │   │ Docker Engine  │  │
│         │   (Port 5432)  │   │  (Port 6379)   │   │ (docker.sock)  │  │
│         └────────────────┘   └────────────────┘   └────────┬───────┘  │
│                                                            │          │
│                                                            ▼          │
│                                                   ┌────────────────┐  │
│                                                   │ Deployed Apps  │  │
│                                                   │(Ports 10000-   │  │
│                                                   │  60000)        │  │
│                                                   └────────────────┘  │
└────────────────────────────────────────────────────────────────────────┘
```

### Services Summary
1. **`forgelab-postgres` (`postgres:16-alpine`):**
   - Host Port: `5432`
   - Volume: `forgelab_pgdata`
   - Role: Stores relational tables (users, projects, deployments, secrets, logs).
2. **`forgelab-redis` (`redis:7-alpine`):**
   - Host Port: `6379`
   - Volume: `forgelab_redisdata`
   - Role: Queue for background deployment jobs (`LPUSH` / `BRPOP`) and Pub/Sub broker for live logs.
3. **`forgelab-backend` (Go Control Plane):**
   - Host Port: `8080`
   - Mounts: `/var/run/docker.sock` and `forgelab_builds:/app/data/builds`
   - Role: REST API, WebSocket Hub, and background Docker deployment worker.
4. **`forgelab-frontend` (Next.js 14 App):**
   - Host Port: `3000`
   - Role: Operational UI console.
5. **Docker Engine:**
   - Host Integration: Accessed via Docker socket.
   - Role: Builds container images and executes user applications.

---

## 3. Host Port Allocations & Authoritative Range

| Service / Capability | Port / Range | Protocol | Notes |
| :--- | :--- | :--- | :--- |
| **Frontend UI** | `3000` | HTTP | Browser entry point (`http://localhost:3000`) |
| **Backend REST & WS** | `8080` | HTTP/WS | API server and WebSocket endpoint |
| **PostgreSQL Database** | `5432` | TCP | Relational database access |
| **Redis Queue & PubSub**| `6379` | TCP | Background job coordination |
| **Deployed User Applications** | **`10000–60000`** | HTTP/TCP | **Authoritative dynamic port range** |

### Clarification on Dynamic Port Range
> **Notice:** Older architectural notes referenced an initial range of `8000–9000`. The **authoritative implementation** (`backend/internal/network/port_manager.go:L19-L23` and `backend/cmd/server/main.go:L98`) allocates host ports from:
> ```text
> 10000 to 60000
> ```
> Each newly deployed container is allocated an open host port in this range and bound to `0.0.0.0:<port>`.

---

## 4. Environment Variables Configuration

Copy `.env.example` to `.env` before starting the platform:

```bash
cp .env.example .env
```

### Complete Environment Variable Catalog

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
| `FORGELAB_ALLOWED_SOURCE_ROOTS` | `""` (Empty) | Comma-separated allowed host roots for repository imports |
| `DOCKER_HOST` | `""` (Auto-detect) | Docker Engine socket path override |
| `LOG_LEVEL` | `debug` | Logging level (`debug`, `info`, `warn`, `error`) |
| `LOG_FORMAT` | `text` | Log formatter (`text` or `json`) |
| `NEXT_PUBLIC_API_URL` | `http://localhost:8080/api` | API URL consumed by the Next.js frontend |

---

## 5. Local Source Path Security (`FORGELAB_ALLOWED_SOURCE_ROOTS`)

ForgeLAB builds applications from host repository directories. Because user input dictates the filesystem path, `internal/security/PathValidator` enforces strict defense-in-depth rules:

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
Host Path (C:\dev\repo)  ──► Copy ──►  /app/data/builds/<deployment-id>/  ──► Docker ImageBuild
```
**Rationale:**
1. **Build Isolation:** Changes made on the host during a build do not corrupt or invalidate the image build context.
2. **Reproducibility:** The build operates against an immutable point-in-time snapshot.
3. **Container Sandbox:** Containers never mount host source directly; source is copied into the image at build time.

---

## 6. Docker Engine Integration & Host Socket

The Go backend requires direct access to Docker Engine to manage image builds and container lifecycles:

- **Socket Mount:** `/var/run/docker.sock:/var/run/docker.sock` in `docker-compose.yml`.
- **Windows with Docker Desktop:** Docker Desktop creates the `/var/run/docker.sock` compatibility pipe automatically inside the WSL2 backend.
- **Trust Boundary Warning:** Because the control plane mounts the Docker socket, anyone with access to execute arbitrary commands inside the backend container effectively controls the host Docker daemon. Keep the backend environment secure.

---

## 7. Database Initialization & Schema Migrations

ForgeLAB uses `golang-migrate` for SQL schema versioning. Migrations live in `backend/migrations/`:
- `000001_initial_schema.up.sql` (Creates users, projects, deployments, env_vars, logs, refresh_tokens)
- `000002_add_active_deployment_unique_index.up.sql` (Enforces at most one active deployment per project)

### Applying Migrations (Docker Compose)
With the stack running, execute migrations inside the backend container:
```bash
docker compose exec backend /app/server -migrate
```

### Applying Migrations (Local Go CLI)
If running Go directly on the host machine:
```bash
cd backend
go run cmd/migrate/main.go up
```

### Database Reset Procedure
To reset the development database to a clean state:
```bash
# Option A: Rollback migrations and re-apply
cd backend
go run cmd/migrate/main.go down
go run cmd/migrate/main.go up

# Option B: Complete volume wipe (Destructive)
docker compose down -v
docker compose up --build
docker compose exec backend /app/server -migrate
```

---

## 8. Development Startup & Operational Workflow

### 1. Start the Complete Stack
```bash
docker compose up --build
```
Verify all containers achieve healthy status:
```bash
docker compose ps
```
Expected output:
- `forgelab-postgres` (healthy)
- `forgelab-redis` (healthy)
- `forgelab-backend` (healthy)
- `forgelab-frontend` (running)

### 2. Tail Backend & Worker Logs
```bash
docker compose logs -f backend
```

### 3. Access the Web Application
Open your browser to:
```text
http://localhost:3000
```
Register an account, create a project pointing to a local host directory with a `Dockerfile`, and proceed to the manual verification steps in [docs/12-manual-verification.md](12-manual-verification.md).

---

## 9. Common Failure Modes & Troubleshooting

| Symptom | Probable Cause | Resolution |
| :--- | :--- | :--- |
| **`docker client initialization warning`** | Docker daemon not running or socket not mounted | Ensure Docker Desktop is running and socket permissions are valid |
| **`port already in use`** | Host port 5432, 6379, 8080, or 3000 occupied | Terminate conflicting process or update port mapping in `.env` / compose |
| **`repository path is outside allowed root`** | Path violates `FORGELAB_ALLOWED_SOURCE_ROOTS` | Add the target directory to `.env` or clear the variable for unrestricted dev |
| **`no available host ports in range 10000-60000`** | System firewall or port exhaustion | Verify host networking allows local TCP binds on ports > 10000 |
| **WebSocket disconnects immediately** | Invalid JWT token or clock skew | Check browser localStorage token and verify backend JWT_SECRET matches |
