# ForgeLAB — Agent Operational Memory & Engineering Instructions

You are an AI coding agent operating inside Antigravity, working on the **ForgeLAB** repository (`Estifanos58/ForgeLAB`).

Treat this document as the project's **persistent engineering memory**. It establishes the mandatory developer workflow, architectural invariants, documentation truth rules, and implementation constraints.

---

## 1. Mandatory Agent Workflow

Every agent working on this codebase must adhere strictly to the following lifecycle.

### Before Changing Any Code:
1. **Read `INSTRUCTION.md`** (this document).
2. **Read `docs/00-current-state.md`** to know the current branch, commit, implementation status, and verification state.
3. **Read the relevant functional requirements** in [docs/02-functional-requirements.md](docs/02-functional-requirements.md).
4. **Read the relevant architecture, security, API, or frontend document** (e.g., [docs/04-architecture-decisions.md](docs/04-architecture-decisions.md), [docs/05-security-trust-boundaries.md](docs/05-security-trust-boundaries.md), [docs/09-api-contract.md](docs/09-api-contract.md), [docs/10-frontend-architecture.md](docs/10-frontend-architecture.md), [docs/11-development-environment.md](docs/11-development-environment.md)).
5. **Determine whether the requested task is:**
   - Current implementation bug fix
   - Current implementation refactor
   - New implementation for an MVP capability
   - Future platform functionality (must **not** be started prematurely)
   - Documentation update
   - Unresolved architectural question
6. **Do not implement future functionality** simply because an architecture document or roadmap mentions it.
7. **Do not replace an established architectural decision** without recording an updated Architecture Decision Record in [docs/13-decisions.md](docs/13-decisions.md).

### After Implementation:
1. **Re-check current implementation** against the modified files using static analysis and compilation checks.
2. **Update `docs/00-current-state.md`** if the project state, commit, or capabilities matrix changed.
3. **Update the relevant architectural / contract documents** (e.g., [docs/09-api-contract.md](docs/09-api-contract.md), [docs/06-websocket-contract.md](docs/06-websocket-contract.md)).
4. **Do not claim physical verification.** Automated tests, mocks, or compilation success are **not** physical verification.
5. **Provide a detailed manual verification procedure** in [docs/12-manual-verification.md](docs/12-manual-verification.md) for the human project owner.

---

## 2. Core Documentation & Verification Principles

### Documentation Truth Rule
```text
Code tells us what is currently implemented.

Documentation must distinguish:
- CURRENT BEHAVIOR (what the code actually does today)
- HISTORICAL DESIGN (earlier proposals or replaced approaches)
- FUTURE DESIGN (planned, agreed, but not yet implemented)
- UNRESOLVED IDEAS (open questions and alternatives)
- VERIFIED BEHAVIOR (manually validated by the project owner)

Never collapse these categories into one.
```

Whenever documentation and code disagree:
1. Determine what the code actually does now.
2. Document the current implementation honestly.
3. Classify older behavior as historical/proposed/future when appropriate.
4. Do not silently change code to make the documentation true.

### Physical Verification Rule
The project's authoritative validation methodology is:
```text
IMPLEMENT  ──►  PHYSICAL MANUAL VERIFICATION  ──►  RECORD RESULT  ──►  PROCEED
```

It is **NOT**:
```text
IMPLEMENT  ──►  WRITE AUTOMATED TESTS  ──►  CALL FEATURE COMPLETE
```

- **The human user is the person who performs physical verification.**
- Agents may inspect code, build binaries, run static analysis, review logs, and construct verification procedures.
- Agents must **never** mark a feature as `PHYSICALLY VERIFIED` merely because unit tests pass, a container compiles, or a mock succeeds.
- Existing automated tests in `backend/internal/...` are useful existing artifacts, but they are **non-authoritative**.

---

## 3. Product Vision & Context

ForgeLAB is a **self-hosted application deployment platform**.

A developer gives ForgeLAB a project repository, and ForgeLAB manages the resulting application's lifecycle: building, deploying, running, monitoring, logging, restarting, and rolling back.

### Why ForgeLAB Exists
- The developer already has extensive experience with TypeScript/NestJS, Spring Boot, PostgreSQL, Redis, Kafka, Docker, WebSockets, and microservices.
- **Go for the control plane is non-negotiable.** It is a deliberate learning dimension. Do not replace Go with NestJS or Spring Boot.
- The project is designed to teach when additional infrastructure is justified. Do not introduce microservices or Kubernetes prematurely.

### Non-Negotiable Constraints
1. **Self-hosted and local-first.** The core project runs on local infrastructure with $0 development cost.
2. **Single control-plane architecture for MVP.** Backend Go binary + PostgreSQL 16 + Redis 7 + Docker Engine + Next.js 14 frontend.
3. **No premature distributed systems.** Worker separation, multi-node scheduling, and service meshes are earned by real operational needs.

---

## 4. Key Architectural Invariants

### Invariant 1: Deployment Safety Invariant
```text
A is running  ──►  B is deployed  ──►  B fails health check  ──►  A remains running
```
- A failed deployment must **never** terminate or replace the previous running deployment.
- `projects.current_deployment_id` updates **only** after the new deployment passes its HTTP health check gate.

### Invariant 2: Rollback Semantics
```text
Current deployment A is active
        ↓
Rollback is requested
        ↓
New deployment record C is created using previous known-good deployment B's image tag
        ↓
Deployment C follows normal deployment pipeline (container creation, port allocation, health check)
        ↓
Deployment C is promoted to current ONLY after passing health check
```

### Invariant 3: Source Snapshotting Before Build
- ForgeLAB copies local repository source into an isolated build directory (`data/builds/<deployment-id>`) before building.
- Build context is isolated from concurrent host edits; build runs from a durable point-in-time snapshot.

### Invariant 4: Server-Side Authorization & Channel Isolation
- Projects and deployments belong to a specific `owner_id` (User UUID).
- Server validates ownership on every REST request and WebSocket subscription.
- Events published to `deployment:<uuid-A>` must **never** be delivered to subscribers of `deployment:<uuid-B>`.

### Invariant 5: Identifier Standard (UUID)
- All durable entities (`users`, `projects`, `deployments`, `environment_variables`) use **UUIDv4**.
- ULID proposals from past discussions remain deferred. Do not migrate identifiers to ULID without an explicit architectural change.

---

## 5. Scope Boundaries (Current MVP vs. Future)

### Current Implemented MVP Scope:
- User registration, login, JWT token auth, atomic refresh token rotation
- Project CRUD, ownership enforcement, host path security validation (`PathValidator`)
- Host source snapshotting, Docker SDK image builds, dynamic host port allocation (`10000–60000`)
- Deployment state transitions (`QUEUED` → `CLONING` → `BUILDING` → `STARTING` → `HEALTH_CHECKING` → `RUNNING` / `FAILED`)
- Deployment-time HTTP health check gating (10 attempts, 2-second interval)
- Application lifecycle controls: Stop, Start, Restart
- Rollback creating a new release from prior known-good image
- AES-256-GCM encrypted environment variables & streaming log secret redactor
- Scoped WebSocket log and status streaming (`deployment:<uuid>`)
- Next.js 14 frontend: Dashboard, project settings, secrets manager, and live terminal viewer

### Explicitly Deferred Future Scope (Do NOT Implement):
- GitHub OAuth integration & repository browser (see [docs/15-github-integration.md](docs/15-github-integration.md))
- Automated GitHub webhook deployments (`git push` triggers)
- Caddy reverse proxy integration, custom domains, automated Let's Encrypt TLS
- Continuous runtime health monitoring & automated self-healing crash recovery
- Zero-downtime blue/green or rolling proxy updates
- Container resource quotas (CPU/RAM caps in Docker HostConfig)
- Multi-user organizations & Role-Based Access Control (RBAC)
- ForgeLAB CLI (`forge deploy`, `forge logs`)
- Multi-node distributed workers
- WebSocket event replay service from database
