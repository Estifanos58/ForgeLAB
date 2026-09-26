# ForgeLAB — Architecture Decision Records (ADR)

**Status:** Authoritative Architectural Record  
**Purpose:** Durable engineering memory to prevent future agents from reversing established project decisions.  

---

## Related Documents

- [INSTRUCTION.md](../INSTRUCTION.md) — Operational memory & mandatory agent workflow
- [docs/00-current-state.md](00-current-state.md) — Current state snapshot & matrix
- [docs/03-technology-decisions.md](03-technology-decisions.md) — Technology stack selections
- [docs/04-architecture-decisions.md](04-architecture-decisions.md) — Core architectural patterns
- [docs/12-manual-verification.md](12-manual-verification.md) — Physical test procedures
- [docs/14-known-limitations.md](14-known-limitations.md) — Known limitations and investigation items

---

## Decision Index

| ID | Title | Status | Date |
| :--- | :--- | :--- | :--- |
| **DEC-001** | Go as the Control-Plane Language | **ACTIVE** | 2026-09-25 |
| **DEC-002** | Single Control-Plane Architecture (No Premature Microservices) | **ACTIVE** | 2026-09-25 |
| **DEC-003** | Redis for Queue and Pub/Sub (No Kafka) | **ACTIVE** | 2026-09-25 |
| **DEC-004** | UUIDv4 Identifiers Standard (ULID Migration Deferred) | **ACTIVE** | 2026-09-25 |
| **DEC-005** | Deployment-Scoped WebSocket Isolation | **ACTIVE** | 2026-09-25 |
| **DEC-006** | Local Source Snapshotting Prior to Docker Build | **ACTIVE** | 2026-09-25 |
| **DEC-007** | Deployment Safety Invariant (Failed Releases Never Terminate Running Releases) | **ACTIVE** | 2026-09-25 |
| **DEC-008** | GitHub Import via Explicit OAuth and Repository Selection | **ACTIVE** | 2026-09-25 |
| **DEC-009** | Physical Manual Verification as Authoritative Validation Methodology | **ACTIVE** | 2026-09-26 |

---

## DEC-001: Go as the Control-Plane Language

- **Date:** 2026-09-25
- **Status:** **ACTIVE**
- **Decision:** Build the entire backend control plane, API handlers, Docker integration, and background worker in Go.
- **Reason:**
  1. The developer already possesses deep professional experience with TypeScript/NestJS and Spring Boot.
  2. Go represents a deliberate new learning dimension for the developer in systems programming, concurrency (goroutines/channels), networking, and container orchestration.
  3. Go produces a single, low-memory, fast-starting static binary well-suited for a self-hosted platform.
- **Current Implementation:** `backend/cmd/server/main.go`, `backend/internal/...`
- **Rule for Future Agents:** Do **not** replace Go with NestJS, Node.js, Spring Boot, or Python simply because it might feel faster to write. Go is a primary non-negotiable project constraint.

---

## DEC-002: Single Control-Plane Architecture

- **Date:** 2026-09-25
- **Status:** **ACTIVE**
- **Decision:** Operate ForgeLAB as a single control-plane process containing the REST API server, WebSocket Hub, and an embedded goroutine deployment worker.
- **Reason:**
  1. Avoids premature microservice complexity, service meshes, RPC serialization overhead, and distributed tracing costs.
  2. Deployment platform development should teach when distributed architecture is earned by concrete requirements (concurrency, worker isolation, failure domains), rather than assuming microservices are automatically superior.
- **Current Implementation:** `backend/cmd/server/main.go` runs the HTTP server and starts `go deployQueue.StartWorker(...)` in the same binary.
- **Rule for Future Agents:** Do **not** split the backend into separate microservice repositories or separate worker binaries during the MVP phase.

---

## DEC-003: Redis for Queue and Pub/Sub

- **Date:** 2026-09-25
- **Status:** **ACTIVE**
- **Decision:** Use Redis 7 as the unified coordination engine for background deployment jobs (`LPUSH` / `BRPOP`) and realtime WebSocket log distribution (Redis Pub/Sub).
- **Reason:**
  1. Kafka or RabbitMQ would introduce excessive operational overhead and resource consumption for a local-first, self-hosted deployment platform.
  2. Redis provides both lightweight list-based queues and fast in-memory Pub/Sub channels in a single container.
- **Current Implementation:** `backend/internal/queue/deploy_queue.go` and `backend/internal/websocket/hub.go`.
- **Rule for Future Agents:** Do not introduce Kafka, RabbitMQ, or NATS unless multi-cluster enterprise queuing is explicitly required.

---

## DEC-004: UUIDv4 Standard Identifiers (ULID Proposal Deferred)

- **Date:** 2026-09-25
- **Status:** **ACTIVE**
- **Decision:** Standardize on **UUIDv4** across all database primary keys, foreign keys, Go structs, REST API paths, and WebSocket channels.
- **Reason:**
  1. Historical design discussions discussed ULID (Universally Unique Lexicographically Sortable Identifier) for sequential ordering.
  2. However, UUIDv4 is natively supported in PostgreSQL via `pgcrypto` (`gen_random_uuid()`), natively typed in Go (`github.com/google/uuid`), and already implemented uniformly across all tables, models, handlers, and frontend code.
  3. Deployments already have sequential ordering via `deploy_number INTEGER`.
  4. Migrating to ULID would introduce widespread churn across SQL schema, indexes, and clients without delivering significant functional benefit for the MVP.
- **Current Implementation:** `backend/internal/models/models.go`, `backend/migrations/000001_initial_schema.up.sql`.
- **Rule for Future Agents:** Do **not** migrate UUIDs to ULIDs simply because older conversation logs mentioned ULIDs. UUIDv4 is the active standard.

---

## DEC-005: Deployment-Scoped WebSocket Isolation

- **Date:** 2026-09-25
- **Status:** **ACTIVE**
- **Decision:** WebSocket connections are authenticated, and subscriptions are scoped to specific resources using durable UUIDs (`deployment:<uuid>`). The server authorizes ownership before granting subscriptions.
- **Reason:**
  1. Prevents multi-tenant data leaks: User B must never observe build outputs, runtime logs, or environment variables belonging to User A.
  2. Scoping traffic to individual deployments prevents browser DOM overload and ensures clients only receive logs for the release currently on screen.
- **Current Implementation:** `backend/internal/websocket/hub.go:handleSubscribe`.
- **Rule for Future Agents:** Never broadcast deployment logs to generic global or unauthenticated channels.

---

## DEC-006: Local Source Snapshotting Prior to Docker Build

- **Date:** 2026-09-25
- **Status:** **ACTIVE**
- **Decision:** ForgeLAB copies the repository source from the host path into an isolated temporary directory (`data/builds/<deployment-id>`) before initiating `docker build`.
- **Reason:**
  1. **Build Context Isolation:** Edits made by the developer on the host machine while a build is in progress will not corrupt or invalidate the image build context.
  2. **Reproducibility:** Builds are executed from an immutable point-in-time snapshot.
  3. **Security:** Avoids mounting the host filesystem directly into the Docker daemon during build execution.
- **Current Implementation:** `backend/internal/docker/engine.go:144-152` (`copyDirectory`).
- **Rule for Future Agents:** Do not replace the snapshot copy mechanism with direct host bind-mount builds unless an explicit caching/performance ADR is established.

---

## DEC-007: Deployment Safety Invariant

- **Date:** 2026-09-25
- **Status:** **ACTIVE**
- **Decision:** A deployment that fails at any phase (source acquisition, build, container creation, or HTTP health checking) must **never** terminate, replace, or disrupt the currently running deployment.
- **Reason:**
  1. Foundational reliability invariant: A broken release attempt should leave production traffic running on the last known-good release.
  2. `projects.current_deployment_id` is updated **only** after the new container passes its HTTP health check gate.
- **Current Implementation:** `backend/internal/docker/engine.go:312-326`.
- **Rule for Future Agents:** Never stop an old container or promote a new deployment ID before the health check gate succeeds.

---

## DEC-008: GitHub Import via Explicit OAuth and Repository Selection

- **Date:** 2026-09-25
- **Status:** **ACTIVE**
- **Decision:** Future GitHub import must use explicit OAuth authorization, repository listing, and user selection. Blind cloning of arbitrary GitHub URLs without authentication is rejected.
- **Reason:**
  1. Prevents SSRF attacks and internal network reconnaissance.
  2. Enables access to private repositories.
  3. Provides an authorized identity token necessary for registering automated push webhooks.
- **Current Implementation:** Fully documented in [docs/15-github-integration.md](15-github-integration.md). (Implementation deferred to post-MVP).
- **Rule for Future Agents:** Do not weaken this decision into "clone any arbitrary URL entered in a text box."

---

## DEC-009: Physical Manual Verification as Authoritative Validation Methodology

- **Date:** 2026-09-26
- **Status:** **ACTIVE**
- **Decision:** Physical manual execution by the project owner in the live development environment is the authoritative mechanism for determining feature completion.
- **Reason:**
  1. Automated tests and mocks do not validate Docker socket protocol communication, dynamic host port binding, PostgreSQL transactional concurrency, Redis list popping, browser WebSocket frame parsing, or end-to-end CSS/DOM rendering.
  2. Eliminates false confidence where "all unit tests pass" but the application fails to deploy a container in reality.
- **Current Implementation:** [docs/12-manual-verification.md](12-manual-verification.md).
- **Rule for Future Agents:** Agents must not claim a feature is physically verified based on compilation, automated test passes, or static analysis.
