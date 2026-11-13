# ForgeLAB — Self-Hosted Application Deployment Platform

ForgeLAB is a single control-plane application deployment platform built in Go. It manages the entire container deployment lifecycle: direct local-directory deployment without browser upload, optional archive upload fallback, authorized GitHub repository import, heuristic runtime framework detection, automatic multi-stage container builds, Dockerfile execution, dynamic host and internal port mapping, health/readiness check gating, live WebSocket log streaming, AES-256-GCM secret and OAuth token encryption, and rollback safety.

---

## Architecture Overview

- **Backend Control Plane**: Go (REST API via `chi` + WebSockets via `gorilla/websocket` with Redis Pub/Sub)
- **Database**: PostgreSQL 16 (Durable records for users, identities, projects, services, service deployments, secrets, github integrations, repository imports, custom domains, metrics history, audit logs, and logs)
- **Service-First Architecture**: Projects compose multiple autonomous services with independent deployment pipelines (`POST /api/projects/{id}/services/{serviceId}/deploy`), per-service deployment modes (`standard` or opt-in `blue_green` zero-downtime cutover via `TrafficRouter` at `/proxy/services/{serviceId}`), rollbacks, and log streams
- **Work Queue & Lease Ownership**: Redis 7 (`LPUSH` / `BRPOP` queue with cryptographically unique execution lease tokens, Lua atomic renewal/recovery, heartbeat cancellation check, automatic orphaned release reconciler, and O(1) state indexing)
- **Runtime Engine & Sandboxing**: Docker Engine SDK with strict trust boundary enforcement (no-new-privileges, `CapDrop: ["ALL"]`, strict security allowlist `allowedContainerCapabilities` permitting only `CHOWN`, `SETGID`, `SETUID` while normalizing `CAP_` prefixes and rejecting disallowed capabilities fail-closed with `ErrCapabilityNotAllowed`, non-root execution profiles, rejection of `/var/run/docker.sock` and sensitive mounts, rejection of `host` and `container:` network namespace sharing, enforced per-service CPU/RAM/PID resource limits, per-owner cumulative resource/storage quotas, and truthful rejection of unsupported `ephemeral_storage_mb` disk quotas)
- **Control-Plane Network Isolation**: The privileged ForgeLAB backend container is never connected to tenant Docker networks; preview routing resolves strictly via published container host ports (`HostPort`) with management port shielding (:8080, :4142, :3000, :5432, :6379)
- **Custom Domains & Caddy HTTPS Ingress**: Integrated `caddy:2-alpine` reverse proxy in Compose (`infra/caddy/Caddyfile`, persistent `forgelab_caddy_data` and `forgelab_caddy_config` volumes, `CADDY_ADMIN_URL=http://caddy:2019`). Domains require DNS TXT verification (`_forgelab-verify.<domain>`) and are reported `https_active: true` (`tls_status: "active"`) only after confirmed Caddy Admin API route provisioning—truthfully reporting `proxy_unconfigured` otherwise
- **Guest Mode Security**: Disabled by default in `.env.example` and production, while available via explicit configuration (`FORGELAB_ALLOW_GUEST_MODE=true`) or local dev defaults with isolated guest browser sessions
- **Proxy Trust & Dynamic CORS**: Untrusted clients have spoofed forwarding headers stripped at the boundary; dynamic preview host registry validates active tunnel hosts without broad wildcard origins
- **Gateway & Tunnel Reliability**: Production gateway requires verified TLS (`GATEWAY_TLS_SKIP_VERIFY=true` rejected; automatic plaintext fallback eliminated). Cloudflare Quick Tunnels feature graceful termination (`cmd.Cancel` with SIGINT & bounded WaitDelay), startup-state race elimination, and startup stale share reconciliation
- **Authentication, Token Lifespans & WebSockets**: Extended active token (default 48h, > 1 day) and refresh token (default 14d, > 1 week) defaults supporting duration strings and minutes/days, prolonged 24h session inactivity limits, bidirectional session updates during active WebSocket communication, cross-tab refresh mutex, and transaction commit grace cache
- **Health, Self-Healing, Auto-Rollback & Diagnostics**: Lightweight `/health` liveness probe, deep dependency `/ready` probe, `/health/queue`, `/health/logs`, configurable background self-heal container restarts (`FORGELAB_SELF_HEAL_*`), crash-loop detection (`FORGELAB_CRASH_LOOP_*`), automatic rollback after sustained health-check failures or crash loops (`FORGELAB_AUTO_ROLLBACK_*`) preserving and verifying the prior healthy container (`VerifyPreviousHealthyContainer` / `RestorePreviousHealthyContainer`), and actionable system/project diagnostics (`GET /api/diagnostics`, `GET /api/projects/{id}/diagnostics`)
- **Monitoring, Metrics History & Operational Audit Trail**: Backend-sampled and TTL-cached live metrics (`FORGELAB_METRICS_CACHE_TTL`), periodic background metrics history persisted to PostgreSQL with configurable retention (`FORGELAB_METRICS_SAMPLE_INTERVAL`, `FORGELAB_METRICS_RETENTION_DAYS`), and secret-redacted `audit_logs` covering deployments, rollbacks, secrets, domains, volumes, and imports
- **Rate Limiting**: Configurable fixed-window counter throttling (`ratelimit`) backed by Redis/in-memory for authentication, uploads, deployments, and WebSockets returning HTTP 429
- **Deployment Lifecycle & Persistent Volumes**: Immediate cancellation abort during heartbeats, atomic superseding of obsolete queued builds, strict non-regression invariant for healthy containers, and project-scoped persistent volume inspection, Docker disk usage visibility, and `.tar.gz` backup/safe restore (`409 ErrVolumeInUse` if mounted)
- **Source Ingestion & Push-to-Deploy**: Local Agent CLI (`cmd/agent`) with loopback `127.0.0.1:4142` binding, Windows NTFS junction/8.3 short-path canonicalization, and interactive Session 0 guard; archive upload fallback (`/api/sources/upload`) with restricted permissions and 1GB per-owner quota; restart-recoverable GitHub repository imports (`RecoverInterruptedImports`, durable state, cancel/retry); and HMAC-SHA256 verified push-to-deploy webhooks (`POST /api/webhooks/github`)
- **Build Strategy**: Direct build context streaming with `.dockerignore` pruning, TAR symlink-escape enforcement (`ErrSymlinkEscape`), bounded 64KB log lines, automatic heuristic multi-stage container build generation & custom Dockerfile build strategy
- **Host Port Range**: Dynamic port allocation in the range **`10000–60000`** with dynamic container internal port mapping
- **Frontend**: Next.js 16.4.6 + React 19 + TypeScript + Tailwind CSS with hardened Content Security Policy (`next.config.ts`)
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

You can also launch all 6 services (`postgres`, `redis`, `migrate`, `backend`, `frontend`, and `caddy`) using Docker Compose:
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

#### Containerized Stack Highlights:
1. **Configured Host Source Root:** In `docker-compose.yml`, the backend container mounts `${FORGELAB_HOST_SOURCE_ROOT:-./}:/host-projects:ro` as a read-only volume. When running ForgeLAB in Docker, configure `FORGELAB_HOST_SOURCE_ROOT` to your host projects directory (e.g. `C:\Users\username\Projects` or `/home/user/projects`). The backend automatically translates host paths to the container mount safely.
2. **Host Local Agent Option:** Start `.\start-agent.ps1` (Windows) or `./start-agent.sh` (Linux/macOS) on `http://127.0.0.1:4142`; Compose configures `FORGELAB_AGENT_URL=http://host.docker.internal:4142` automatically.
3. **Integrated Caddy Reverse Proxy (`caddy:2-alpine`):** Binds `${CADDY_HTTP_PORT_BINDING:-80}:80` and `${CADDY_HTTPS_PORT_BINDING:-443}:443` with baseline config at [infra/caddy/Caddyfile](infra/caddy/Caddyfile), persistent volumes `forgelab_caddy_data` (`/data`) and `forgelab_caddy_config` (`/config`), and dynamic route provisioning via `CADDY_ADMIN_URL=http://caddy:2019`.
4. **Path Security & Streaming:** ForgeLAB enforces `FORGELAB_ALLOWED_SOURCE_ROOTS` and streams Docker build contexts from the filesystem with `.dockerignore` pruning and TAR symlink-escape enforcement, eliminating browser uploads and disk duplication.

For active development with host-native source trees, use **Workflow A**.

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
├── .github/workflows/       # GitHub Actions CI & CodeQL workflows
├── infra/caddy/             # Baseline Caddyfile for reverse proxy & automatic TLS
├── backend/                 # Go control-plane application
│   ├── cmd/server/          # API server & embedded deployment worker entrypoint
│   ├── cmd/agent/           # Local agent CLI for streaming local host source trees
│   ├── cmd/gateway/         # Standalone public gateway daemon for public preview sharing
│   ├── cmd/migrate/         # SQL migration CLI tool
│   ├── Dockerfile           # Multi-stage Go backend Dockerfile
│   ├── internal/            # Core business logic & services
│   │   ├── agent/           # Local agent HTTP server, folder picker, session manager
│   │   ├── analyzer/        # Build & configuration analyzer
│   │   ├── auth/            # JWT authentication & claims
│   │   ├── config/          # Environment configuration loader & validation
│   │   ├── crypto/          # AES-256-GCM encryption for secrets & tokens
│   │   ├── database/        # PostgreSQL connection pool (pgx)
│   │   ├── detector/        # Heuristic framework & runtime detector
│   │   ├── discovery/       # Multi-service topology, compose & fingerprint engine
│   │   ├── docker/          # Docker SDK engine, capability allowlist, recovery, volumes, metrics & diagnostics
│   │   ├── dockerignore/    # .dockerignore parser and rule matcher
│   │   ├── envparser/       # Repository .env file parser
│   │   ├── gateway/         # Public gateway proxy, TLS ingress & stream multiplexing
│   │   ├── handlers/        # REST HTTP request handlers
│   │   ├── logging/         # Log persistence & secret redactor
│   │   ├── middleware/      # Auth, guest isolation, CORS, trusted proxy & security headers
│   │   ├── models/          # Data models & state machine transitions
│   │   ├── network/         # Dynamic host port allocator (10000-60000)
│   │   ├── proxy/           # Blue-green TrafficRouter & Caddy Admin API syncer
│   │   ├── queue/           # Redis worker queue with lease ownership safety
│   │   ├── ratelimit/       # Fixed-window rate limiting with in-memory fallback
│   │   ├── security/        # Windows/Unix path canonicalization, junction/reparse & root boundary validator
│   │   ├── services/        # User, Project, Service, Deployment, Domain, Webhook, Import, Metrics, Audit & Secret services
│   │   ├── tararchive/      # Streaming TAR build context & symlink escape enforcement
│   │   ├── tunnel/          # Cloudflare & gateway outbound tunnels + share router
│   │   └── websocket/       # WebSocket Hub, single-use tickets & Redis PubSub
│   └── migrations/          # PostgreSQL schema migrations (000001 to 000027)
├── frontend/                # Next.js 16.4.6 + React 19 + TypeScript web application
│   ├── src/app/             # Pages & internal Next API proxy (App Router)
│   ├── src/components/      # UI, auth, dashboard & marketing components
│   ├── src/features/        # Domain modules (deployments, services, domains, volumes, metrics, topology, webhooks)
│   ├── src/lib/             # Typed API clients, session manager & WebSocket hooks
│   ├── test/                # Frontend Node/tsx unit & security header tests
│   ├── next.config.ts       # Tightened CSP & HTTP security headers configuration
│   └── Dockerfile           # Next.js container Dockerfile
├── docker-compose.yml       # Local stack (postgres, redis, migrate, backend, frontend, caddy)
├── start-agent.ps1          # Windows interactive desktop Local Agent launcher
├── start-agent.sh           # Linux/macOS Local Agent launcher
├── INSTRUCTION.md           # Persistent agent operational memory & workflow
└── .env.example             # Environment variable template
```

---

## Unit Testing Artifacts

Automated unit and regression tests validate isolated algorithmic and integration logic across the backend and frontend:

```bash
cd backend
SKIP_DOCKER_LIVE_TEST=1 go test -timeout 300s ./...

cd frontend
npm test
```

The test suites cover:
- **Path Canonicalization & TAR Security:** Traversal defense, Windows NTFS junctions / 8.3 short paths (`CanonicalizeExistingPath`, `\??\` normalization), UNC rejection (`internal/security`), and TAR symlink-escape enforcement (`ErrSymlinkEscape` in `internal/tararchive`).
- **Container Trust Boundaries:** Capability drop `ALL` and strict allowlist enforcement (`CHOWN`, `SETGID`, `SETUID`, `ErrCapabilityNotAllowed`), socket/sensitive mount rejection, and resource caps (`internal/docker`).
- **Custom Domains & Caddy Sync:** DNS TXT verification, dynamic Caddy route generation, and truthful `proxy_unconfigured` status handling (`internal/services`, `internal/proxy`, `internal/handlers`).
- **Recovery, Self-Healing & Blue-Green Cutover:** Sustained health failure auto-rollback, crash-loop detection, verified preserved-container restoration, and `/proxy/services/{serviceId}` slot routing (`internal/docker`, `internal/proxy`).
- **GitHub Import Recovery & Webhooks:** Startup `RecoverInterruptedImports`, durable cancel/retry state, HMAC-SHA256 webhook verification, and `X-GitHub-Delivery` deduplication (`internal/services`, `internal/handlers`).
- **Volumes, Metrics, Audit Logs & Diagnostics:** Volume validation & archive safety bounds, TTL-cached runtime metrics & historical retention, secret-redacted `audit_logs`, and diagnostic aggregation (`internal/docker`, `internal/services`, `internal/handlers`).
- **JWT, Crypto, Logs & WebSockets:** Claims & token rotation (`internal/auth`), AES-256-GCM encryption (`internal/crypto`), log redaction (`internal/logging`), state transitions (`internal/models`), and channel/ticket isolation (`internal/websocket`).
- **Frontend Regression & CSP Tests:** Trusted-proxy header stripping, hardened CSP & HSTS headers, WebSocket tickets, log filters, session sync, and local agent checks (`frontend/test/`).

*Note: Automated tests are supportive engineering artifacts. Live host-environment validation remains governed by [docs/12-manual-verification.md](docs/12-manual-verification.md), where physical test records remain `NOT RECORDED` until executed.*

---

## Engineering Documentation Index

The `docs/` directory represents ForgeLAB's persistent engineering memory:

- [docs/00-current-state.md](docs/00-current-state.md) — **Start here:** Implementation audit baseline, documentation status, and implementation matrix
- [docs/01-product-vision.md](docs/01-product-vision.md) — Product scope, principles, and non-goals
- [docs/02-functional-requirements.md](docs/02-functional-requirements.md) — Implemented vs. future functional requirements
- [docs/03-technology-decisions.md](docs/03-technology-decisions.md) — Tech stack rationale and rejected alternatives
- [docs/04-architecture-decisions.md](docs/04-architecture-decisions.md) — Architecture, state machine, and data models
- [docs/05-security-trust-boundaries.md](docs/05-security-trust-boundaries.md) — Security model, Docker socket trust, CSP, and secrets
- [docs/06-websocket-contract.md](docs/06-websocket-contract.md) — Realtime WebSocket specifications & single-use ticket auth
- [docs/07-implementation-roadmap.md](docs/07-implementation-roadmap.md) — Implementation roadmap & verification tracking
- [docs/08-open-questions-future.md](docs/08-open-questions-future.md) — Resolved platform architecture & deferred scope
- [docs/09-api-contract.md](docs/09-api-contract.md) — Complete REST & WebSocket API contract
- [docs/10-frontend-architecture.md](docs/10-frontend-architecture.md) — Next.js UI structure and state architecture
- [docs/11-development-environment.md](docs/11-development-environment.md) — Development setup, ports, environment variables, and troubleshooting
- [docs/12-manual-verification.md](docs/12-manual-verification.md) — Authoritative physical test procedures
- [docs/13-decisions.md](docs/13-decisions.md) — Architecture Decision Records (DEC-001 to DEC-017)
- [docs/14-known-limitations.md](docs/14-known-limitations.md) — Identified limitations, investigation items, and backlog
- [docs/15-github-integration.md](docs/15-github-integration.md) — GitHub App, OAuth, recoverable imports & push-to-deploy design
