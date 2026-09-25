# ForgeLab

A self-hosted application deployment platform.

ForgeLab manages your application lifecycle: building, deploying, running, monitoring, logging, restarting, and rolling back — all from a web interface and API.

## Architecture

- **Backend / Control Plane:** Go
- **Database:** PostgreSQL
- **Queue / Pub-Sub:** Redis
- **Application Runtime:** Docker
- **Frontend:** Next.js + TypeScript

## Prerequisites

- [Go 1.21+](https://go.dev/dl/)
- [Docker](https://docs.docker.com/get-docker/)
- [Node.js 18+](https://nodejs.org/)
- [Docker Compose](https://docs.docker.com/compose/)

## Quick Start

### 1. Start infrastructure

```bash
docker compose up -d
```

This starts PostgreSQL and Redis.

### 2. Run database migrations

```bash
cd backend
go run cmd/migrate/main.go up
```

### 3. Start the backend

```bash
cd backend
go run cmd/server/main.go
```

The API server starts on `http://localhost:8080`.

### 4. Start the frontend (later)

```bash
cd frontend
npm install
npm run dev
```

The dashboard starts on `http://localhost:3000`.

## Project Structure

```
ForgeLAB/
├── docs/                    # Engineering documentation
│   ├── 01-product-vision.md
│   ├── 02-functional-requirements.md
│   ├── 03-technology-decisions.md
│   ├── 04-architecture-decisions.md
│   ├── 05-security-trust-boundaries.md
│   ├── 06-websocket-contract.md
│   ├── 07-implementation-roadmap.md
│   └── 08-open-questions-future.md
├── backend/                 # Go control plane
│   ├── cmd/                 # Entry points
│   │   ├── server/          # API server
│   │   └── migrate/         # Database migrations
│   ├── internal/            # Private application code
│   │   ├── config/          # Configuration
│   │   ├── database/        # Database connection
│   │   ├── middleware/       # HTTP middleware
│   │   ├── models/          # Data models
│   │   ├── handlers/        # HTTP handlers
│   │   ├── services/        # Business logic
│   │   └── auth/            # Authentication
│   └── migrations/          # SQL migration files
├── frontend/                # Next.js dashboard (future)
├── docker-compose.yml       # Local dev infrastructure
├── .env.example             # Environment variable template
└── INSTRUCTION.md           # Original project specification
```

## Documentation

See the [docs/](docs/) directory for comprehensive engineering documentation.

## License

Private — not open source.
