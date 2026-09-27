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
| **DEC-006** | Local Source Snapshotting Prior to Docker Build | **PARTIALLY SUPERSEDED (DEC-014)** | 2026-09-25 |
| **DEC-007** | Deployment Safety Invariant (Failed Releases Never Terminate Running Releases) | **ACTIVE** | 2026-09-25 |
| **DEC-008** | GitHub Import via Explicit OAuth and Repository Selection | **ACTIVE** | 2026-09-25 |
| **DEC-009** | Physical Manual Verification as Authoritative Validation Methodology | **ACTIVE** | 2026-09-26 |
| **DEC-010** | Multi-Provider OAuth Authentication with Deferred Repository Integration | **ACTIVE** | 2026-09-26 |
| **DEC-011** | Universal Source Ingestion, GitHub Repository Integration, and Automatic Build/Runtime Strategy | **ACTIVE** | 2026-09-27 |
| **DEC-012** | Authentication Boundary Proxy, Source Workspace Ownership, and Detector Hardening | **ACTIVE** | 2026-09-27 |
| **DEC-013** | Streaming Source Ingestion, Staged Workspaces, and Asynchronous Processing Lifecycle | **ACTIVE** | 2026-09-27 |
| **DEC-014** | Direct Local-Directory Deployment, Streaming Docker Build Context, and Early `.dockerignore` Pruning | **ACTIVE** | 2026-09-27 |

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
- **Status:** **PARTIALLY SUPERSEDED BY DEC-014**
- **Decision:** Historically, ForgeLAB copied the repository source from the host path into an isolated temporary directory (`data/builds/<deployment-id>`) before initiating `docker build`.
- **Reason:**
  1. **Build Context Isolation:** Edits made by the developer on the host machine while a build is in progress will not corrupt or invalidate the image build context.
  2. **Reproducibility:** Builds are executed from an immutable point-in-time snapshot.
  3. **Security:** Avoids mounting the host filesystem directly into the Docker daemon during build execution.
- **Current Implementation:** For `local_directory` deployments, direct build mode is now the default (DEC-014), eliminating full filesystem duplication. Snapshot mode is retained as a configurable option (`FORGELAB_LOCAL_BUILD_MODE=snapshot`) and for uploaded source workspaces (`local_upload`).
- **Rule for Future Agents:** For `local_directory`, default to direct filesystem builds. Do not force an intermediate source copy into `data/builds/<deployment-id>` unless snapshot mode is explicitly enabled.

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

---

## DEC-010: Multi-Provider OAuth Authentication with Deferred Repository Integration

- **Date:** 2026-09-26
- **Status:** **ACTIVE**
- **Decision:** Introduce Google OAuth 2.0 and GitHub OAuth as first-class authentication providers for ForgeLAB accounts, while strictly keeping GitHub repository importing, repository browsing, webhooks, and automated deployments deferred.
- **Reason:**
  1. **Developer Experience:** Modern developer tools require frictionless sign-in with existing identity providers (Google and GitHub) without forcing password management.
  2. **Security & Boundary Separation:** Decouples user authentication (verifying identity via standard OpenID Connect / OAuth user profile and verified email) from repository access (which requires high-privilege `repo` scopes, token persistence, webhook management, and clone isolation).
  3. **Backend-Owned Session Authority:** Sessions are issued and maintained by the Go backend via secure HttpOnly cookies (`forgelab_access_token`, `forgelab_refresh_token`). No JWT persistence in client-side `localStorage`.
  4. **Dedicated Identity Model:** Introduces the `auth_identities` table with nullable `users.password_hash` to support deterministic account linking by verified email and provider subject without fragile email-only matching.
- **Current Implementation:** `backend/internal/services/oauth_service.go`, `backend/internal/services/user_service.go`, `backend/internal/handlers/auth_handler.go`, `backend/migrations/000003_add_auth_identities.up.sql`, `frontend/src/components/auth/oauth-buttons.tsx`.
- **Rule for Future Agents:** Do not conflate GitHub sign-in with GitHub repository import. Keep repository importing and webhook synchronization behind explicit future design milestones as specified in DEC-008.

---

## DEC-011: Universal Source Ingestion, GitHub Repository Integration, and Automatic Build/Runtime Strategy

- **Date:** 2026-09-27
- **Status:** **ACTIVE**
- **Decision:** Replaced the obsolete host-filesystem-path and mandatory-Dockerfile MVP model with a universal application deployment architecture:
  1. **Source Abstraction:** Supports `local` (isolated upload workspace snapshots) and `github` (authorized repository imports via encrypted OAuth tokens).
  2. **Clean Authentication vs. Repository Authorization:** GitHub sign-in (`read:user user:email`) and GitHub repository authorization (`repo` scope) remain separate capabilities. Repository tokens are encrypted at rest using AES-256-GCM in `github_integrations`.
  3. **Local Computer Import:** Users upload project directories or zip/tar.gz archives through the browser directly into a controlled, isolated source workspace with path traversal and zip slip protection, eliminating host-path inaccessibility in Docker Compose.
  4. **Heuristic Project Detection:** Source trees are inspected to detect runtime frameworks (Node.js, Next.js, Vite, Python/FastAPI/Flask/Django, Go, Java, Rust, Dockerfile).
  5. **Build Strategy Abstraction:** Supports `auto` (multi-stage Dockerfile generation from detected runtime) and `dockerfile` (explicit Dockerfile).
  6. **Dynamic Internal Port & Health Strategy:** Container internal ports are dynamically mapped (e.g. 3000, 8000, 8080) to dynamic host ports. Readiness checks support `auto`, `http`, `tcp`, and `none`.
  7. **Deployment Safety Invariant Preserved:** Failed releases are discarded; healthy releases remain active.
- **Current Implementation:** `backend/internal/detector/`, `backend/internal/services/source_service.go`, `backend/internal/services/github_service.go`, `backend/internal/docker/engine.go`, `backend/migrations/000004_source_and_build_abstractions.up.sql`, `frontend/src/components/dashboard/create-project-modal.tsx`.
- **Rule for Future Agents:** Do not revert to requiring host filesystem paths or mandatory Dockerfiles. Maintain source isolation and AES-256-GCM token encryption.

---

## DEC-012: Authentication Boundary Proxy, Source Workspace Ownership, and Detector Hardening

- **Date:** 2026-09-27
- **Status:** **ACTIVE**
- **Decision:** Addressed live authentication and local-upload failure modes without altering the core Go/PostgreSQL/Redis/Docker architecture:
  1. **Next.js App Router Reverse Proxy:** Implemented `frontend/src/app/api/[[...path]]/route.ts` as a reliable same-origin reverse proxy. Explicitly preserves incoming `Cookie`, `Authorization`, method, query string, request body (`duplex: 'half'`), backend `Set-Cookie` headers (via `response.headers.getSetCookie()`), `Location` redirects (`redirect: 'manual'`), and backend status codes. Client JavaScript never touches raw JWTs.
  2. **Session Verification & Controlled Refresh:** Frontend `login` and `register` verify session validity by calling `GET /api/auth/me` before updating auth state. `apiClient` implements a single controlled 401 refresh-and-retry mechanism using the refresh-token cookie, preventing infinite loops.
  3. **Persistent Source Workspace Ownership:** Created `source_workspaces` table (`migrations/000005_add_source_workspaces.up.sql`) recording `id`, `owner_id`, `workspace_path`, `files_count`, `total_bytes`, and `created_at`. Every source lookup, deletion, and project creation verifies ownership against the authenticated user.
  4. **Registered Source Deletion Route:** Registered `DELETE /api/sources/{id}` in Chi router with `AuthMiddleware` and `SourceService.DeleteSource()`. Unauthorized users receive `403 Forbidden`.
  5. **Deterministic Source Ingestion:** Strips outer browser-selected directory wrappers (e.g. `MyProject/package.json` $\to$ `package.json`) while preserving internal nested directories. Uses `io.LimitedReader(src, remaining + 1)` to prevent silent file truncation and strictly enforce the 100MB uncompressed limit. Parses raw `Content-Disposition` header to circumvent Go's `filepath.Base` stripping of relative paths.
  6. **Docker Compose Volume Persistence:** Added `forgelab_sources:/app/data/sources` named volume and `FORGELAB_SOURCES_DIR=/app/data/sources` to persist uploaded sources across container restarts, isolated from build workspaces.
  7. **Deterministic Dockerfile Generation:** Hardened automatic Dockerfiles across Node.js, Next.js, Vite/static, FastAPI, Flask, Django, Go, Maven, Gradle, and Rust. Multi-stage Java builds copy artifacts to `/app/app.jar` and Rust builds copy binaries to `/app/server`, eliminating shell glob expansion failures in exec-form `CMD`.
  8. **Readiness Semantics Hardening:** `health_strategy: "auto"` treats HTTP 2xx/3xx as healthy, explicitly rejects HTTP 5xx server errors as healthy, and falls back to TCP connectivity. Preserves the deployment safety invariant.
  9. **Consistent GitHub Detection Endpoint:** Standardized read-only repository inspection as `GET /api/integrations/github/repositories/{owner}/{repo}/detect` with `branch` and `root_dir` query parameters across backend, frontend client, and documentation.
  10. **Consistent GitHub Repository Authorization Callback URL:** Synchronized `GITHUB_REPO_REDIRECT_URL=http://localhost:3000/api/integrations/github/callback` across `.env.example`, `docker-compose.yml`, backend configuration, and service logic. Documented GitHub Developer Settings single-app vs two-app OAuth setup to prevent `The redirect_uri is not associated with this application.` errors.
  11. **Safe HTTP Server Timeouts:** Replaced aggressive global `ReadTimeout: 15s` and `WriteTimeout: 15s` on Go's `http.Server` with `ReadHeaderTimeout: 15s` and `IdleTimeout: 120s`. This eliminates mid-stream connection resets during large file uploads and prevents premature closure of persistent WebSocket streams while still guarding against Slowloris attacks.
  12. **HTTP Boundary Upload Protection & Observability:** Enforced early `Content-Length` and streaming `http.MaxBytesReader` limits (105MB max HTTP body) returning deterministic `413 Request Entity Too Large`. Added structured `slog` metrics logging upload durations, file counts, and sizes without logging file contents.
  13. **Client-Side Pre-Upload Filtering & Measurable Progress:** Implemented path-segment-based pre-upload filtering (`node_modules`, `.git`, `.next`, `dist`, `build`, `.venv`, etc.) and client-side 100MB size validation. Replaced opaque `fetch()` with `XMLHttpRequest` progress reporting to provide dynamic file counts and accurate upload percentages.
- **Current Implementation:** `frontend/src/app/api/[[...path]]/route.ts`, `frontend/src/lib/api/client.ts`, `frontend/src/lib/source-utils.ts`, `frontend/src/features/auth/auth-context.tsx`, `frontend/src/components/dashboard/create-project-modal.tsx`, `backend/internal/services/source_service.go`, `backend/internal/services/github_service.go`, `backend/internal/handlers/source_handler.go`, `backend/internal/detector/detector.go`, `backend/internal/docker/engine.go`, `backend/cmd/server/main.go`, `docker-compose.yml`.
- **Rule for Future Agents:** Do not restore global `ReadTimeout` to `http.Server`. Maintain client-side pre-filtering and `source_workspaces` ownership checks.

---

## DEC-013: Streaming Source Ingestion, Staged Workspaces, and Asynchronous Processing Lifecycle

- **Date:** 2026-09-27
- **Status:** **ACTIVE**
- **Decision:** Removed the `r.ParseMultipartForm` disk-spooling bottleneck and decoupled network upload from server-side source analysis:
  1. **Streaming Multipart Ingestion:** Replaced `r.ParseMultipartForm` in `SourceHandler.UploadSource` with sequential part consumption via `r.MultipartReader()`. Files stream directly from the incoming HTTP stream into their destination paths within an isolated temporary staging directory (`data/sources/.uploads/<source_id>/`), completely eliminating intermediate temporary file creation and reopen-copy overhead.
  2. **Controlled Upload Staging & Atomic Finalization:** Uploads write exclusively into `.uploads/<source_id>/`. Incomplete or cancelled uploads never appear as valid workspaces. Upon complete streaming success, root directory normalization runs in-place, and the directory is atomically renamed to `data/sources/<source_id>/`.
  3. **Decoupled Asynchronous Processing Lifecycle:** The upload endpoint returns `201 Created` immediately with `{ "source_id": "...", "status": "processing", "phase": "finalizing" }` once bytes are staged. Long-running heuristic detection (`internal/detector`) executes in a background goroutine, updating database lifecycle columns (`status`, `phase`, `processed_files`, `processed_bytes`, `detection_result`, `error`).
  4. **Authenticated Source Status Polling (`GET /api/sources/{id}`):** Introduced an authenticated status retrieval endpoint enforcing source ownership. Returns current processing status, phase, metrics, and detection without exposing internal filesystem paths.
  5. **Reliable Cancellation & Clean Failure Recovery:** The browser upload uses `AbortController` bound to the active `XMLHttpRequest`. Aborting cancels the request, signaling Go's `r.Context().Done()`. The streaming reader terminates immediately, purges `.uploads/<source_id>/`, and rolls back any partial database records.
  6. **Safe Authentication & Timeout Management:** Upload XHR enforces a 10-minute network timeout (`xhr.timeout = 600000`) and removes blind 401 retries that previously risked re-uploading entire multi-megabyte payloads.
  7. **Explicit Frontend State Machine & Honest Progress:** Refactored `CreateProjectModal` from boolean flags to an explicit `ImportPhase` (`idle`, `preparing`, `uploading`, `processing`, `ready`, `failed`, `cancelled`). Network progress honestly displays byte transfer (0–100%, rate, ETA). The UI transitions to server processing status and advances to application configuration only when the source workspace reaches `ready`.
- **Current Implementation:** `backend/internal/services/source_service.go`, `backend/internal/handlers/source_handler.go`, `backend/migrations/000006_extend_source_workspaces_lifecycle.up.sql`, `frontend/src/lib/api/client.ts`, `frontend/src/components/dashboard/create-project-modal.tsx`.
- **Rule for Future Agents:** Do not reintroduce `r.ParseMultipartForm` for directory uploads. Ensure all source uploads stage under `.uploads/` before atomic finalization. Preserve asynchronous status polling and ownership verification on `GET /api/sources/{id}`.

---

## DEC-014: Direct Local-Directory Deployment, Streaming Docker Build Context, and Early `.dockerignore` Pruning

- **Date:** 2026-09-27
- **Status:** **ACTIVE**
- **Decision:** Replaced the browser-side folder upload pipeline (`webkitdirectory` -> huge `FormData` -> `POST /api/sources/upload`) with direct local-directory ingestion and streaming Docker builds:
  1. **Direct Local Source Ingestion (`local_directory`):** In local development and self-hosted environments, ForgeLAB accepts and validates an existing local repository path via `POST /api/sources/local/validate` instead of transferring directory contents over HTTP. The backend inspects the directory directly for detection metadata.
  2. **Path Boundary & Host-to-Container Translation:** Security is enforced via `PathValidator` with `FORGELAB_ALLOWED_SOURCE_ROOTS`. For containerized deployments, paths are safely translated from host roots (`FORGELAB_HOST_SOURCE_ROOT`) to container mounts (`/host-projects:ro`) without exposing the entire host filesystem or trusting raw container paths from clients.
  3. **Streaming Docker Build Context (`io.Pipe`):** Eliminated the in-memory `bytes.Buffer` tar archiving bottleneck (`createTarArchive`). Build contexts are walked on the fly and written into an `io.Pipe()` via `tar.Writer`, consumed concurrently by Docker's `ImageBuild` stream. Context materialization in RAM is eliminated.
  4. **Early `.dockerignore` Directory Pruning:** Integrated standard `.dockerignore` matching (`CanSkipDir`). Ignored directories (e.g., `node_modules`, `.git`, `.next`, `dist`, `.venv`) are skipped *before* filesystem descent, preventing traversal of tens of thousands of excluded files.
  5. **Direct Build Mode (Zero Duplication):** Direct directory projects build straight from the validated directory source without intermediate copying into `data/builds/<deployment-id>`, while virtual files (such as generated `Dockerfile.forgelab`) are streamed into the TAR payload in-memory without mutating read-only source mounts.
  6. **Retained Archive Upload Fallback:** The uploaded source workflow (`local_upload` via .zip/.tar.gz) remains available as an explicit fallback for remote clients or environments without shared filesystem access.
- **Current Implementation:** `backend/internal/security/path_validator.go`, `backend/internal/docker/dockerignore.go`, `backend/internal/docker/tar_streamer.go`, `backend/internal/docker/engine.go`, `backend/internal/handlers/source_handler.go`, `backend/internal/services/project_service.go`, `frontend/src/components/dashboard/create-project-modal.tsx`.
- **Rule for Future Agents:** Never construct a Docker build context in a `bytes.Buffer`. Never force `webkitdirectory` browser uploads for local directories when direct filesystem access is available. Always prune ignored directories early during traversal.



