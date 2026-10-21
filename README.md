# ForgeLAB — Self-Hosted Application Deployment Platform

ForgeLAB is a single control-plane application deployment platform built in Go. It manages the entire container deployment lifecycle: direct local-directory deployment without browser upload, optional archive upload fallback, authorized GitHub repository import, heuristic runtime framework detection, automatic multi-stage container builds, Dockerfile execution, dynamic host and internal port mapping, health/readiness check gating, live WebSocket log streaming, AES-256-GCM secret and OAuth token encryption, and rollback safety.

---

## Architecture Overview

- **Backend Control Plane**: Go (REST API via `chi` + WebSockets via `gorilla/websocket` with Redis Pub/Sub)
- **Database**: PostgreSQL 16 (Durable records for users, identities, projects, services, service deployments, secrets, github integrations, and logs)
- **Service-First Architecture**: Projects compose multiple autonomous services with independent deployment pipelines (`POST /api/projects/{id}/services/{serviceId}/deploy`), rollbacks, and log streams
- **Work Queue & Lease Ownership**: Redis 7 (`LPUSH` / `BRPOP` queue with cryptographically unique execution lease tokens, Lua atomic renewal/recovery, heartbeat cancellation check, automatic orphaned release reconciler, and O(1) state indexing)
- **Runtime Engine & Sandboxing**: Docker Engine SDK with strict trust boundary enforcement (no-new-privileges, capability drop `ALL`, rejection of `/var/run/docker.sock` and sensitive mounts, rejection of `host` and `container:` network namespace sharing, enforced CPU/RAM/PID resource limits, and truthful rejection of unsupported `ephemeral_storage_mb` disk quotas)
- **Health, Readiness & Diagnostics**: Lightweight `/health` liveness probe, deep dependency `/ready` check (probing PostgreSQL, Redis, and Docker Engine concurrently with 0 secret leakage), and `/health/queue` queue & abandoned-job recovery diagnostics
- **Rate Limiting**: Configurable sliding-window throttling (`ratelimit`) backed by Redis/in-memory for authentication, uploads, deployments, and WebSockets returning HTTP 429
- **Deployment Lifecycle**: Immediate cancellation abort during heartbeats, atomic superseding of obsolete queued builds, and strict non-regression invariant for healthy containers
- **Source Ingestion**: Local Agent CLI (`cmd/agent`) with loopback `127.0.0.1` default binding and unauthenticated status protection, optional archive upload fallback (`/api/sources/upload`) with restricted `0700`/`0600` permissions, & authorized GitHub repository import (`/api/integrations/github`) with hardened decompression quotas (100MB compressed/uncompressed, 10k files, 50MB/file, 3m timeout) and atomic cache creation
- **Build Strategy**: Direct build context streaming (`io.Pipe()`) without in-memory archive buffering, bounded 64KB log lines, automatic heuristic multi-stage container build generation & custom Dockerfile build strategy
- **Host Port Range**: Dynamic port allocation in the range **`10000–60000`** with dynamic container internal port mapping
- **Frontend**: Next.js 16.3.6 LTS + React 19 + TypeScript + Tailwind CSS (Operational developer console)
- **Secure Public Sharing**: Authenticated outbound Yamux multiplexed tunnel connecting to a standalone public gateway (`cmd/gateway`) for public HTTPS links without inbound port forwarding
- **Deployment Safety Invariant**: Failed releases never terminate or replace the previous running deployment.

---

## Current Status & Verification Methodology

- **Current Implementation State:** The local-deployment MVP vertical slice is **IMPLEMENTED**.
- **Authoritative Verification Methodology:** ForgeLAB enforces **physical manual verification** by the human project owner in the live environment as the sole authoritative completion criterion.
- **Automated Tests:** Existing unit tests in `backend/internal/...` validate cryptographic, path security, and state machine algorithms, but they are **non-authoritative** artifacts. Features remain unverified until manually tested per [docs/12-manual-verification.md](docs/12-manual-verification.md).

---

## Prerequisites

- [Docker Desktop](https://www.docker.com/products/docker-desktop/) (Docker Engine 24+ & Compose v2+)
- [Go 1.26+](https://go.dev/dl/) (required for compiling the backend, running migrations, and running the host backend in Workflow A)
- [Node.js 20+](https://nodejs.org/) (required for running the host frontend Next.js 16 console in Workflow A)

---

## Development Setup & Workflows

ForgeLAB provides two setups depending on your workflow. Because ForgeLAB deploys projects from absolute directory paths on the host filesystem, the Go backend requires direct visibility into your local source tree. Full details are documented in [docs/11-development-environment.md](docs/11-development-environment.md).

### Workflow A: Local-Source Development (Host-Run Backend) — Recommended

In this workflow, backing services (PostgreSQL, Redis) run in Docker Compose while the Go backend and frontend run directly on the host. This ensures that arbitrary local host repository paths (e.g. `C:\dev\my-app` or `/home/user/my-app`) are naturally accessible to ForgeLAB's `PathValidator` and snapshot routines.

#### 1. Start Backing Infrastructure
Start PostgreSQL and Redis in the background:
```bash
docker compose up -d postgres redis
```

#### 2. Configure Environment & Run Migrations
Copy the configuration template:
```bash
cp .env.example .env
```
Execute schema migrations against local PostgreSQL:
```bash
cd backend
go run cmd/migrate/main.go up
```

#### 3. Run the Go Backend
```bash
cd backend
go run cmd/server/main.go
```
The backend API is now running at `http://localhost:8080`.

#### 4. Run the Next.js Frontend
In a separate terminal:
```bash
cd frontend
npm install
npm run dev
```
Open [http://localhost:3000](http://localhost:3000) in your browser:
1. Register a new user account and log in.
2. Create a project specifying an absolute repository path on your host containing a `Dockerfile`.
3. Trigger a deployment and inspect live build and runtime logs over WebSockets.
4. Access the deployed application on its allocated dynamic host port (`10000–60000`).

---

### Workflow B: Container Infrastructure Setup (Docker Compose)

You can also launch all services using Docker Compose:
```bash
docker compose up --build
```

To run database migrations using Docker Compose:
```bash
docker compose run --rm migrate up
```
Or execute directly inside a running backend container:
```bash
docker compose exec backend /app/forgelab-migrate up
```

#### Containerized Local-Source Support:
1. **Configured Host Source Root:** In `docker-compose.yml`, the backend container mounts `${FORGELAB_HOST_SOURCE_ROOT:-./}:/host-projects:ro` as a read-only volume. When running ForgeLAB in Docker, configure `FORGELAB_HOST_SOURCE_ROOT` to your host projects directory (e.g. `C:\Users\username\Projects` or `/home/user/projects`). The backend automatically translates host paths to the container mount safely.
2. **Path Security:** ForgeLAB enforces `FORGELAB_ALLOWED_SOURCE_ROOTS` so that only directories within permitted roots can be validated or deployed.
3. **No File Uploads Needed:** Direct local-directory deployment streams the Docker build context directly from the filesystem with `.dockerignore` pruning, eliminating browser uploads and disk duplication.

For active development with local-source deployments, use **Workflow A**.

---

## Secure Public Project Sharing

ForgeLAB enables users deploying applications on their own computers to share secure, public HTTPS links with anyone worldwide—without purchasing a domain, configuring DNS, opening firewall ports, or having a public IP address.

ForgeLAB supports two sharing providers:
1. **Cloudflare Quick Tunnels (`cloudflare`, default):** Domainless, zero-configuration sharing providing an instant `https://<random>.trycloudflare.com` HTTPS URL directly to the running application.
2. **Self-Hosted Public Gateway (`gateway`, optional):** Advanced provider for organizations with their own domain, public server, wildcard DNS, and TLS certificates.

---

### Provider 1: Cloudflare Quick Tunnels (Default / Domainless)

Cloudflare Quick Tunnels use Cloudflare's free, anonymous tunnel service (`trycloudflare.com`). No domain or Cloudflare account is required.

```text
Visitor's Browser (Anywhere on Internet)
       │
       ▼ (HTTPS :443)
┌──────────────────────────────────────────────┐
│  Cloudflare Edge (trycloudflare.com)         │  (Cloudflare Global Anycast)
└──────────────────────┬───────────────────────┘
                       │
                       │ Outbound Tunnel Connection (QUIC/HTTP2)
                       │ (Initiated from local computer by cloudflared)
                       ▼
┌──────────────────────────────────────────────┐
│  cloudflared Child Process                   │  (Managed by ForgeLAB)
└──────────────────────┬───────────────────────┘
                       │ Loopback HTTP (:random_port)
                       ▼
┌──────────────────────────────────────────────┐
│  Share-Scoped Router (backend/internal/tunnel)│ (Bound to 127.0.0.1:0)
│  - Longest-prefix route matching (/api -> backend, / -> frontend)
│  - Strict path traversal defense (rejects '..' and '%2e%2e')
│  - Blocks ForgeLAB management (:8080) and infra ports (:5432, :6379, :4142)
│  - Bidirectional WebSocket proxying
└──────────────┬───────────────────────────────┘
               │ Local Reverse Proxy (127.0.0.1)
        ┌──────┴──────┐
        ▼             ▼
┌──────────────┐ ┌──────────────┐
│ Frontend App │ │ Backend API  │  (Docker containers on 127.0.0.1:host_port)
└──────────────┘ └──────────────┘
```

#### Prerequisites
Install `cloudflared` on the host running ForgeLAB:
- **macOS:** `brew install cloudflared`
- **Linux (Debian/Ubuntu):**
  ```bash
  curl -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg | sudo tee /usr/share/keyrings/cloudflare-main.gpg >/dev/null
  echo 'deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared any main' | sudo tee /etc/apt/sources.list.d/cloudflared.list
  sudo apt-get update && sudo apt-get install -y cloudflared
  ```
- **Linux (Arch):** `sudo pacman -S cloudflared`
- **Windows:** `winget install --id Cloudflare.cloudflared`

#### Configuration (`.env`)
```env
FORGELAB_SHARING_ENABLED=true
FORGELAB_SHARING_PROVIDER=cloudflare
FORGELAB_CLOUDFLARED_PATH=cloudflared
FORGELAB_APP_HOST_IP=127.0.0.1
```

#### Quick Tunnel Characteristics & Limitations
- **Ephemeral URLs:** Every time a share is revoked and recreated, Cloudflare assigns a new random `*.trycloudflare.com` hostname.
- **Development & Testing:** Intended for previews, staging demonstrations, and pair debugging, not long-term production hosting.
- **Protocols Supported:** HTTP/1.1, HTTP/2, and WebSockets (full duplex).
- **Unsupported:** Server-Sent Events (SSE) responses are buffered by Cloudflare Quick Tunnels and may not stream in real-time.
- **Config Conflicts:** If you already have a `~/.cloudflared/config.yml` configured for named tunnels, Quick Tunnels may report a conflict. ForgeLAB detects this and reports actionable guidance.

---

### Provider 2: Self-Hosted Public Gateway (Optional / Advanced)

For deployments with private infrastructure and custom domains (e.g. `*.preview.yourdomain.com`).

```text
Visitor's Browser
       │
       ▼ (HTTPS :443)
┌──────────────────────────────────────────────┐
│  Public Gateway (cmd/gateway)                │  (Public Server / VPS)
│  - Wildcard TLS (*.preview.yourdomain.com)   │
│  - Authenticated Yamux multiplexer           │
└──────────────────────┬───────────────────────┘
                       │ Outbound Yamux Tunnel (TLS :8443)
                       ▼
┌──────────────────────────────────────────────┐
│  ForgeLAB Host (cmd/server)                  │  (User's Local Computer)
│  - TunnelManager maintains connection        │
│  - Bounded 5s registration acknowledgments   │
│  - Isolated share routing & port shielding   │
└──────────────────────────────────────────────┘
```

#### Gateway Server Setup
```bash
cd backend
GATEWAY_HTTPS_ADDR=":443" \
GATEWAY_TUNNEL_PORT=8443 \
GATEWAY_TLS_CERT=/etc/letsencrypt/live/yourdomain.com/fullchain.pem \
GATEWAY_TLS_KEY=/etc/letsencrypt/live/yourdomain.com/privkey.pem \
GATEWAY_BASE_DOMAIN=preview.yourdomain.com \
GATEWAY_AUTH_SECRET="your-strong-cryptographic-secret" \
go run cmd/gateway/main.go
```

#### Local ForgeLAB Configuration (`.env`)
```env
FORGELAB_SHARING_ENABLED=true
FORGELAB_SHARING_PROVIDER=gateway
GATEWAY_TUNNEL_URL=gateway.preview.yourdomain.com:8443
GATEWAY_PUBLIC_URL=https://preview.yourdomain.com
GATEWAY_AUTH_TOKEN=your-strong-cryptographic-secret
FORGELAB_APP_HOST_IP=127.0.0.1
```

If the gateway is unconfigured or offline, ForgeLAB truthfully reports sharing as `unavailable` with actionable troubleshooting messages rather than simulating a connection.

---

## Repository Structure

```text
ForgeLAB/
├── docs/                    # Authoritative engineering documentation
│   ├── 00-current-state.md
│   ├── 01-product-vision.md
│   ├── 02-functional-requirements.md
│   ├── 03-technology-decisions.md
│   ├── 04-architecture-decisions.md
│   ├── 05-security-trust-boundaries.md
│   ├── 06-websocket-contract.md
│   ├── 07-implementation-roadmap.md
│   ├── 08-open-questions-future.md
│   ├── 09-api-contract.md
│   ├── 10-frontend-architecture.md
│   ├── 11-development-environment.md
│   ├── 12-manual-verification.md
│   ├── 13-decisions.md
│   ├── 14-known-limitations.md
│   └── 15-github-integration.md
├── .github/workflows/       # GitHub Actions CI (gofmt, vet, test, npm build/lint, smoke, migration)
├── backend/                 # Go control-plane application
│   ├── cmd/server/          # API server & embedded deployment worker entrypoint
│   ├── cmd/agent/           # Local agent CLI for streaming local host source trees
│   ├── cmd/gateway/         # Standalone public gateway daemon for public preview sharing
│   ├── cmd/migrate/         # SQL migration CLI tool
│   ├── Dockerfile           # Multi-stage Go backend Dockerfile
│   ├── internal/            # Core business logic & services
│   │   ├── auth/            # JWT authentication & claims
│   │   ├── config/          # Environment configuration loader
│   │   ├── crypto/          # AES-256-GCM encryption for secrets
│   │   ├── database/        # PostgreSQL connection pool (pgx)
│   │   ├── docker/          # Docker Engine build, container runner & trust boundary
│   │   ├── gateway/         # Public gateway proxy, TLS ingress & stream multiplexing
│   │   ├── handlers/        # REST HTTP request handlers (including /health & /ready)
│   │   ├── logging/         # Log persistence & secret redactor
│   │   ├── middleware/      # Auth, CORS, logger, & JSON middleware
│   │   ├── models/          # Data models & state machine transitions
│   │   ├── network/         # Dynamic host port allocator (10000-60000)
│   │   ├── queue/           # Redis worker queue with lease ownership safety
│   │   ├── ratelimit/       # Sliding-window rate limiting with in-memory fallback
│   │   ├── security/        # Host path validator & boundary defense
│   │   ├── services/        # User, Project, Service, Deployment, Share & Secret services
│   │   ├── tunnel/          # Outbound Yamux tunnel client, local router & upstream resolver
│   │   └── websocket/       # WebSocket Hub, rate limiter, & Redis PubSub
│   └── migrations/          # PostgreSQL schema migrations
├── frontend/                # Next.js 16.3.6 LTS + React 19 + TypeScript web application
│   ├── src/app/             # Pages (Login, Register, Dashboard, Project & Service views)
│   ├── src/lib/             # Typed API client & WebSocket hook
│   └── Dockerfile           # Next.js container Dockerfile
├── docker-compose.yml       # Local dev container stack
├── INSTRUCTION.md           # Persistent agent operational memory & workflow
└── .env.example             # Environment variable template
```

---

## Unit Testing Artifacts

Automated unit tests validate isolated algorithmic logic across the backend:

```bash
cd backend
go test -v ./internal/...
```

The unit test suite covers:
- **JWT & Claims:** Expiration, claims validation, and token generation (`internal/auth`).
- **Path Security:** Traversal defense, symlink evaluation, and restricted system directory rejection (`internal/security`).
- **Secret Encryption:** AES-256-GCM encryption with unique nonces (`internal/crypto`).
- **Log Redaction:** Plaintext secret masking in log streams (`internal/logging`).
- **State Machine:** Valid and invalid deployment state transitions (`internal/models`).
- **WebSocket Isolation:** Channel authorization and isolation (`internal/websocket`).

*Note: These tests are supportive engineering artifacts. End-to-end platform validation requires manual execution per [docs/12-manual-verification.md](docs/12-manual-verification.md).*

---

## Engineering Documentation Index

The `docs/` directory represents ForgeLAB's persistent engineering memory:

- [docs/00-current-state.md](docs/00-current-state.md) — **Start here:** Implementation audit baseline, documentation status, and implementation matrix
- [docs/01-product-vision.md](docs/01-product-vision.md) — Product scope, principles, and non-goals
- [docs/02-functional-requirements.md](docs/02-functional-requirements.md) — MVP vs. future functional requirements
- [docs/03-technology-decisions.md](docs/03-technology-decisions.md) — Tech stack rationale and rejected alternatives
- [docs/04-architecture-decisions.md](docs/04-architecture-decisions.md) — Architecture, state machine, and data models
- [docs/05-security-trust-boundaries.md](docs/05-security-trust-boundaries.md) — Security model, Docker socket trust, and secrets
- [docs/06-websocket-contract.md](docs/06-websocket-contract.md) — Realtime WebSocket specifications & duplicate delivery note
- [docs/07-implementation-roadmap.md](docs/07-implementation-roadmap.md) — Implementation roadmap & verification tracking
- [docs/08-open-questions-future.md](docs/08-open-questions-future.md) — Resolved MVP architecture & deferred scope
- [docs/09-api-contract.md](docs/09-api-contract.md) — Complete REST & WebSocket API contract
- [docs/10-frontend-architecture.md](docs/10-frontend-architecture.md) — Next.js UI structure and state architecture
- [docs/11-development-environment.md](docs/11-development-environment.md) — Development setup, ports, and troubleshooting
- [docs/12-manual-verification.md](docs/12-manual-verification.md) — Authoritative physical test procedures
- [docs/13-decisions.md](docs/13-decisions.md) — Architecture Decision Records (DEC-001 to DEC-009)
- [docs/14-known-limitations.md](docs/14-known-limitations.md) — Identified limitations, investigation items, and backlog
- [docs/15-github-integration.md](docs/15-github-integration.md) — Future GitHub OAuth & repository selection design
