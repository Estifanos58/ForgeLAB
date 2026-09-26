# ForgeLab — Technology Decisions

**Status:** Current Reference Specification  
**Architecture:** Single Control Plane Application  

---

## Related Documents

- [INSTRUCTION.md](../INSTRUCTION.md) — Operational memory & mandatory agent workflow
- [docs/00-current-state.md](00-current-state.md) — Current state snapshot & matrix
- [docs/04-architecture-decisions.md](04-architecture-decisions.md) — System architecture & data models
- [docs/11-development-environment.md](11-development-environment.md) — Host tool requirements & setup
- [docs/13-decisions.md](13-decisions.md) — Architecture Decision Records (DEC-001 to DEC-003)

---

## Chosen Technology Stack

| Layer | Technology | Rationale |
|-------|-----------|-----------|
| Backend / Control Plane | **Go** | Deliberate learning choice. Do NOT replace with NestJS/Spring Boot. |
| API Style | **REST** | Standard, well-understood, CLI-friendly |
| Realtime Communication | **WebSocket** | Live logs, deployment progress, status updates |
| Database | **PostgreSQL** | Durable state for projects, deployments, users, auth identities |
| Queue / Background Work | **Redis** | Deployment work coordination, pub/sub for realtime, single-use OAuth state storage |
| Application Runtime | **Docker** | Container build and execution environment |
| Authentication Providers | **Email/Password, Google OAuth, GitHub OAuth** | Multi-provider auth with backend-owned session issuing (HttpOnly cookies) |
| Reverse Proxy | **Caddy** | Future routing/HTTPS automation (not MVP) |
| Frontend | **Next.js 16.3.6 Active LTS + React 19 + TypeScript** | Modern App Router, proxy.ts conventions, Tailwind CSS, landing page and dashboard |
| Observability | **OpenTelemetry** | Future metrics/tracing direction (not MVP) |

## Important Technology Constraints

1. **Go is non-negotiable** for the control plane. It is a deliberate learning dimension.
2. **No cloud dependencies** for the development version. Must run on a local machine.
3. **No Kubernetes** initially. The system is a single control-plane application.
4. **No Kafka** — Redis covers the queue/pub-sub needs.
5. **No service mesh** — unnecessary for single-node MVP.
6. **Verify library/API behavior** against current authoritative documentation before implementation.

## Go Libraries (Selected After Research)

| Purpose | Library | Notes |
|---------|---------|-------|
| HTTP Router | `github.com/go-chi/chi/v5` | Lightweight, idiomatic Go router |
| PostgreSQL Driver | `github.com/jackc/pgx/v5` | High-performance native Go PostgreSQL driver |
| Migrations | `github.com/golang-migrate/migrate/v4` | Database schema migrations |
| WebSocket | `github.com/gorilla/websocket` | Production-grade WebSocket for Go |
| Docker SDK | `github.com/docker/docker/client` | Official Docker Engine SDK for Go |
| JWT | `github.com/golang-jwt/jwt/v5` | JWT authentication tokens |
| Password Hashing | `golang.org/x/crypto/bcrypt` | Standard password hashing |
| UUID | `github.com/google/uuid` | UUID generation for durable identifiers |
| Logging | `log/slog` | Go 1.21+ structured logging (stdlib) |
| Configuration | `github.com/caarlos0/env/v11` | Environment-based config parsing |
| Redis | `github.com/redis/go-redis/v9` | Redis client for Go |
| Encryption | `crypto/aes` + `crypto/cipher` (stdlib) | AES-GCM for secret encryption at rest |

## Rejected Alternatives

| Alternative | Reason Rejected |
|-------------|-----------------|
| NestJS / Spring Boot for backend | Developer already knows these; Go is the learning goal |
| Kubernetes for orchestration | Over-engineering for MVP; distributed arch is earned by requirements |
| Kafka for messaging | Redis covers queue/pub-sub needs at this scale |
| AWS/cloud infrastructure | Must be $0 cost and self-hosted |
| gRPC for API | REST is simpler, CLI-friendly, more appropriate for this product |
| Gin / Echo / Fiber for routing | chi is lighter weight, closer to stdlib patterns |
| GORM for database | pgx is more performant and idiomatic; raw SQL is more educational |
