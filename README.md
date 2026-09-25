# ForgeLAB — Self-Hosted Application Deployment Platform

ForgeLAB is a single control-plane application deployment platform built in Go. It manages the entire container deployment lifecycle: repository snapshotting, Docker builds, container orchestration, HTTP health checking, live WebSocket log streaming, secret encryption, and zero-downtime rollback safety.

---

## Architecture Overview

- **Backend Control Plane**: Go (REST API + WebSockets)
- **Database**: PostgreSQL 16
- **Work Queue & Pub/Sub**: Redis 7
- **Runtime Engine**: Docker SDK
- **Frontend**: Next.js 14 + TypeScript (Dark mode operational UI)
- **Deployment Safety Invariant**: Failed releases never terminate the previous running deployment.

---

## Prerequisites

- [Docker Desktop](https://www.docker.com/products/docker-desktop/) (Engine + Compose)
- [Go 1.21+](https://go.dev/dl/) (for local CLI test execution)
- [Node.js 18+](https://nodejs.org/) (for frontend development)

---

## Quick Start (Docker-First Development)

The standard developer experience runs entirely through Docker Compose.

### 1. Copy Environment Configuration

```bash
cp .env.example .env
```

### 2. Start the Stack

```bash
docker compose up --build
```

This starts:
- **PostgreSQL**: `localhost:5432`
- **Redis**: `localhost:6379`
- **Backend API**: `localhost:8080`
- **Frontend UI**: `localhost:3000`

### 3. Run Database Migrations

In a separate terminal window:

```bash
docker compose exec backend /app/server -migrate
```

*(Or locally via Go)*:
```bash
cd backend
go run cmd/migrate/main.go up
```

### 4. Access ForgeLAB

Open [http://localhost:3000](http://localhost:3000) in your browser:
1. Register a new user account.
2. Log in to access the Dashboard.
3. Import a local repository containing a `Dockerfile`.
4. Trigger a deployment and watch live build/runtime logs stream over WebSockets!

---

## Automated Test Suite

Run unit and isolation tests across all Go packages:

```bash
cd backend
go test -v ./internal/...
```

The test suite validates:
- **Authentication & JWT**: Claims validation, issuer verification, atomic refresh-token rotation.
- **Path Security**: Canonicalization, path traversal blocking, and system directory defense (`PathValidator`).
- **Secret Security**: AES-256-GCM encryption with unique nonces and log redaction (`LogRedactor`).
- **State Machine**: Enforced deployment state transitions (`ValidateStateTransition`).
- **WebSocket Isolation**: Server-side authorization and strict channel isolation (`project:<uuid>`, `deployment:<uuid>`).

---

## Repository Structure

```
ForgeLAB/
├── docs/                    # Architecture & engineering specifications
│   ├── 01-product-vision.md
│   ├── 02-functional-requirements.md
│   ├── 03-technology-decisions.md
│   ├── 04-architecture-decisions.md
│   ├── 05-security-trust-boundaries.md
│   ├── 06-websocket-contract.md
│   ├── 07-implementation-roadmap.md
│   └── 08-open-questions-future.md
├── backend/                 # Go control-plane application
│   ├── cmd/server/          # API & WebSocket server entrypoint
│   ├── cmd/migrate/         # SQL migration runner
│   ├── internal/            # Core business logic & services
│   │   ├── auth/            # JWT & security identity
│   │   ├── crypto/          # AES-256-GCM encryption
│   │   ├── docker/          # Docker Engine deployment pipeline
│   │   ├── handlers/        # REST HTTP handlers
│   │   ├── logging/         # Log persistence & secret redactor
│   │   ├── models/          # Data models & state machine
│   │   ├── network/         # Host port allocator
│   │   ├── queue/           # Redis worker queue
│   │   ├── security/        # Host path validator
│   │   ├── services/        # User, Project, Deployment, & Secret services
│   │   └── websocket/       # WebSocket Hub & channel pub/sub
│   └── migrations/          # PostgreSQL schema migrations
├── frontend/                # Next.js 14 + TypeScript web application
│   ├── src/app/             # Pages (Login, Register, Dashboard, Project details)
│   └── src/lib/             # API client & WebSocket log hook
├── docker-compose.yml       # Local dev container stack
├── Dockerfile.backend       # Multi-stage Go control-plane Dockerfile
└── .env.example             # Environment variable template
```

---

## Engineering Documentation

For full architecture details, refer to the [docs/](docs/) directory:
- [01-product-vision.md](docs/01-product-vision.md)
- [02-functional-requirements.md](docs/02-functional-requirements.md)
- [03-technology-decisions.md](docs/03-technology-decisions.md)
- [04-architecture-decisions.md](docs/04-architecture-decisions.md)
- [05-security-trust-boundaries.md](docs/05-security-trust-boundaries.md)
- [06-websocket-contract.md](docs/06-websocket-contract.md)
- [07-implementation-roadmap.md](docs/07-implementation-roadmap.md)
- [08-open-questions-future.md](docs/08-open-questions-future.md)
