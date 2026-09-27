# ForgeLAB — Frontend Architecture & UI Specification

**Status:** Current Implementation Documentation  
**Framework:** Next.js 16.3.6 Active LTS (App Router) + React 19 + TypeScript  
**Styling:** Tailwind CSS + Lucide Icons (Dark Mode theme)  
**Location:** `frontend/`  

---

## Related Documents

- [INSTRUCTION.md](../INSTRUCTION.md) — Operational memory & architectural constraints
- [docs/00-current-state.md](00-current-state.md) — Current state snapshot & matrix
- [docs/06-websocket-contract.md](06-websocket-contract.md) — WebSocket message formats & isolation
- [docs/09-api-contract.md](09-api-contract.md) — Backend REST and WebSocket contracts
- [docs/11-development-environment.md](11-development-environment.md) — Docker container networking & environment variables
- [docs/12-manual-verification.md](12-manual-verification.md) — Verification procedures for frontend pages
- [docs/13-decisions.md](13-decisions.md) — Architecture Decision Records (including DEC-010)

---

## 1. Architecture Overview & Design Goals

The ForgeLAB frontend was rebuilt from the ground up on **Next.js 16.3.6 Active LTS** using the App Router, React 19, Tailwind CSS, and the Next.js 16 `proxy.ts` convention (which replaces the legacy `middleware.ts`).

The frontend serves two primary purposes:
1. **Developer SaaS Landing Page:** A polished, production-grade landing page (`/`) showcasing ForgeLAB's architecture, Docker build engine, real-time WebSocket log streaming, health-check gating, and rollback capabilities.
2. **Operational Console:** An authenticated dashboard (`/dashboard`) and project console (`/projects/[id]`) providing complete application lifecycle management, secret configuration, deployment history, and live terminal streaming.

```text
┌────────────────────────────────────────────────────────────────────────┐
│                   Next.js 16.3.6 (App Router)                          │
│                                                                        │
│   ┌────────────────────────────────────────────────────────────────┐   │
│   │               proxy.ts (Route Protection & Session Check)      │   │
│   └────────────────────────────────────────────────────────────────┘   │
│                                │                                       │
│          ┌─────────────────────┼─────────────────────┐                 │
│          ▼                     ▼                     ▼                 │
│   ┌───────────────┐   ┌─────────────────┐   ┌─────────────────┐        │
│   │  (marketing)  │   │     (auth)      │   │      (app)      │        │
│   │       /       │   │ /login /register│   │/dashboard       │        │
│   │  Landing Page │   │ (Google, GitHub,│   │/projects/:id    │        │
│   │               │   │  Password)      │   │                 │        │
│   └───────────────┘   └─────────────────┘   └────────┬────────┘        │
│                                                      │                 │
│                                      ┌───────────────┴────────┐        │
│                                      ▼                        ▼        │
│                              ┌───────────────┐        ┌─────────────┐  │
│                              │  api/client   │        │useDeployment│  │
│                              │ (Credentials: │        │     WS      │  │
│                              │   'include')  │        │ (WebSocket) │  │
│                              └───────┬───────┘        └──────┬──────┘  │
└──────────────────────────────────────┼───────────────────────┼─────────┘
                                       │ /api/* (Proxied)      │ WS (Host)
                                       ▼                       ▼
                        ┌──────────────────────────────────────────────┐
                        │          ForgeLAB Go Backend (8080)          │
                        │       (Auth, API, Workers, WebSocket)        │
                        └──────────────────────────────────────────────┘
```

---

## 2. Directory & Page Structure

```text
frontend/
├── src/
│   ├── app/
│   │   ├── (marketing)/
│   │   │   └── page.tsx              # Developer SaaS landing page
│   │   ├── (auth)/
│   │   │   ├── login/
│   │   │   │   └── page.tsx          # Login page (Google, GitHub, password)
│   │   │   └── register/
│   │   │       └── page.tsx          # Registration page
│   │   ├── (app)/
│   │   │   ├── dashboard/
│   │   │   │   └── page.tsx          # Authenticated project dashboard
│   │   │   └── projects/[id]/
│   │   │       └── page.tsx          # Operational project & deployment console
│   │   ├── globals.css               # Design tokens, typography & base styling
│   │   └── layout.tsx                # Root layout with AuthProvider & metadata
│   ├── components/
│   │   ├── ui/                       # Button, Badge, Card, Input, Modal, Alert, Spinner
│   │   ├── layout/                   # Navbar, AppHeader, Footer
│   │   ├── auth/                     # LoginForm, RegisterForm, OAuthButtons
│   │   ├── marketing/                # Hero, Workflow, Features, Architecture, CTA
│   │   └── dashboard/                # ProjectStats, ProjectCard, CreateProjectModal
│   ├── features/
│   │   ├── auth/                     # AuthContext, useAuth hook (session state)
│   │   └── deployments/              # LifecycleControls, TerminalViewer, History, EnvManager
│   ├── lib/
│   │   ├── api/                      # Typed API client (types.ts, client.ts)
│   │   └── websocket/                # Scoped WebSocket streaming hook
│   └── proxy.ts                      # Next.js 16 proxy convention for route protection
├── next.config.ts                    # Backend reverse-proxy rewrite configuration
├── tailwind.config.ts                # Tailwind design system configuration
├── eslint.config.mjs                 # Flat ESLint 9 configuration with @next/eslint-plugin-next
├── Dockerfile                        # Multi-stage production build (Node 22 Alpine)
├── package.json
└── tsconfig.json
```

---

## 3. Backend-Owned Authentication & Session Management

### No Client-Side Token Storage
- Neither access tokens nor refresh tokens are stored in `localStorage`, `sessionStorage`, cookies readable by JavaScript, or client-side URL parameters.
- All tokens are issued by the Go backend as **HttpOnly, SameSite, Secure (configurable)** cookies:
  - `forgelab_access_token` (15-minute expiry)
  - `forgelab_refresh_token` (7-day expiry)
- Every API call from the browser sends `credentials: 'include'`. The browser automatically attaches cookies to all requests proxied to the backend.

### Next.js 16 `proxy.ts` Route Protection
Next.js 16 introduces `proxy.ts` as the standard route interceptor replacing `middleware.ts`.
- `export function proxy(request: NextRequest)` intercepts navigation.
- If an unauthenticated user attempts to visit `/dashboard` or `/projects/*` without a `forgelab_access_token` or `forgelab_refresh_token` cookie, they are redirected to `/login?redirect=...`.
- If an authenticated user navigates to `/login` or `/register`, they are redirected directly to `/dashboard`.

### Multi-Provider Authentication
The UI provides unified authentication across:
1. **Google OAuth 2.0:** Server-side authorization code flow (`/api/auth/google`).
2. **GitHub OAuth:** Server-side web OAuth flow (`/api/auth/github`) requesting minimum `read:user user:email` scopes.
3. **Email / Password:** bcrypt-authenticated email and password credentials.

---

## 4. Container Networking & Reverse Proxy

When running in Docker Compose:
- The browser accesses ForgeLAB at `http://localhost:3000`.
- The Next.js server proxies `/api/:path*` internally to `BACKEND_INTERNAL_URL` (default: `http://backend:8080/api/:path*`) via `next.config.ts` rewrites:
  ```typescript
  async rewrites() {
    return [
      {
        source: '/api/:path*',
        destination: `${process.env.BACKEND_INTERNAL_URL || 'http://backend:8080'}/api/:path*`,
      },
    ];
  }
  ```
- This ensures that browser requests never need to resolve internal Docker service names such as `http://backend:8080`.

---

## 5. State Synchronization & Realtime Pipeline

- **REST is Authoritative:** PostgreSQL via REST endpoints (`/api/projects`, `/api/deployments`) is the authoritative source for project state, deployment state machine transitions, and persistent build logs.
- **WebSocket is Ephemeral:** Realtime log lines and state-change notifications stream over WebSocket channel `deployment:<uuid>`.
- **Event-Driven Refresh:** When a `status_change` frame arrives via WebSocket, the console automatically re-syncs project configuration, container state, and deployment history via REST.

---

## 6. Resolved Architectural Debt

With this release:
1. **Eliminated `localStorage` Token Risk:** Raw JWTs are no longer stored in client storage. Sessions rely entirely on HttpOnly cookies.
2. **Containerized Multi-Stage Production Build:** Upgraded from `next dev` to a production multi-stage Alpine build with `next build` and `next start`.
3. **Docker Networking Alignment:** Internal Docker communication now points to `http://backend:8080` instead of loopback `http://localhost:8080`.

---

## 7. Visual Design System & UI Specifications

The frontend was rewritten from the ground up to establish a serious, minimal developer infrastructure aesthetic inspired by the principles behind Vercel/Geist:

### Monochromatic-First Color System
- **Page Background:** Near-black (`#09090b`)
- **Elevated Surfaces:** Flat dark neutral grays (`#111113`, `#18181b`)
- **Borders:** Thin 1px neutral borders (`#27272a`) as the primary boundary separator
- **Primary Text:** Crisp near-white (`#fafafa`)
- **Muted Text:** Neutral grays (`#a1a1aa`, `#71717a`)
- **Primary Action Contrast:** High-contrast solid white buttons (`text-neutral-950`) without decorative glowing gradients

### Typography Hierarchy
- **UI Typography:** Geist Sans (`geist/font/sans`) for layout, headings, and product interface
- **Technical Typography:** Geist Mono (`geist/font/mono`) for repository paths, branch names, dynamic ports, deployment/container IDs, terminal logs, and environment secrets
- **Hierarchy:** Page titles (20–28px), Section headings (14–18px), Card/table titles (13–14px), Body (13–14px), Metadata/Terminal logs (11–12px)

### Component Primitives
- **Button (`ui/button.tsx`):** Restrained variants (`primary`, `secondary`, `outline`, `ghost`, `danger`, `success`), 150ms transition, no scale transforms, no color shadows
- **Input (`ui/input.tsx`):** Clean dark surface (`#0a0a0c`), subtle border, accessible focus ring (`focus-visible:ring-1 focus-visible:ring-neutral-400`)
- **Card (`ui/card.tsx`):** Flat container with 1px border, 6–8px radius, zero backdrop-blur or decorative shadows
- **Badge (`ui/badge.tsx`):** Compact status dot + text label (`● running`, `● deploying`, `● failed`, `● stopped`), animation strictly limited to genuinely live processes
- **Modal (`ui/modal.tsx`):** Accessible dialog with Escape key and backdrop click handling, subtle popover shadow, compact close button
- **Terminal Viewer (`features/deployments/terminal-viewer.tsx`):** Near-black surface (`#070709`), Geist Mono telemetry, copy/clear/scroll controls, live WebSocket status dot
- **Skeleton (`ui/skeleton.tsx`):** Dimension-matched loading states replacing generic centered spinners

---

## 8. Multi-Step Project Import & Detection UI

The obsolete MVP host-path modal has been replaced with a modern, multi-step import modal (`components/dashboard/create-project-modal.tsx`):

### Step 1: Source Ingestion
1. **GitHub Flow:**
   - Evaluates repository permission state (`GET /api/integrations/github`).
   - If not connected, displays an authorization banner explaining required scopes with an "Authorize GitHub Repositories" action.
   - Once authorized, renders a searchable list of the user's GitHub repositories with visibility badges (Public/Private), default branch tags, and timestamps.
   - When a repository is selected, fetches branches via `/branches` and triggers automatic heuristic detection via `/detect`.
2. **Local Computer Flow (`Import from Computer`):**
   - Features a clean sub-mode selector between **`[ Existing Directory ]`** (Primary Workflow) and **`[ Upload Archive ]`** (Fallback Workflow):
   - **Mode A: Existing Directory (`localMode: 'directory'`):**
     - Primary, high-performance workflow for local development.
     - User specifies a directory path accessible to the ForgeLAB backend (e.g. `C:\Users\...\Projects\my-app` or `/host-projects/my-app`).
     - Triggers fast metadata validation via `POST /api/sources/local/validate`.
     - Displays explicit, real-time states:
       - *Path Required:* Prompts user for a valid host directory path.
       - *Validating Directory:* Visual progress indicator while the backend validates canonical boundaries and parses manifests.
       - *Directory Found & Validated:* Rich status card displaying canonical path, project name, detected framework, runtime, build strategy (Dockerfile detected or Auto), build context, suggested port, total file count, and uncompressed size.
       - *Security / Validation Error:* Actionable error explanation if the directory does not exist, is outside `FORGELAB_ALLOWED_SOURCE_ROOTS`, or is unmounted in Docker.
     - Zero browser file enumeration, zero `FormData` construction, and zero source network upload.
   - **Mode B: Upload Archive (`localMode: 'archive'`):**
     - Fallback workflow when direct filesystem access is unavailable.
     - Accepts `.zip`, `.tar.gz`, or `.tgz` archive files (enforcing the 100MB limit).
     - Operates an explicit `ImportPhase` lifecycle state machine:
       - `idle`: Initial dropzone state.
       - `preparing`: Archive validation and packaging.
       - `uploading`: Honest network transfer progress (0–100%, transfer speed, ETA) with user-triggered cancellation (`AbortController`).
       - `processing`: Server-side ingestion and runtime detection monitoring with authentic status display.
       - `ready`: Summary badge displaying detected framework, runtime, and port before advancing to configuration.
       - `failed` / `cancelled`: Actionable error classification and state reset.
     - Uploads via streaming `POST /api/sources/upload` and polls `GET /api/sources/{id}` until status transitions to `ready` or `failed`.

### Step 2: Configuration & Runtime Review
- Summarizes the imported source:
  - For GitHub: repository name and branch.
  - For Direct Local Directory: host path, file count, and `Direct Build` badge.
  - For Local Archive: archive name and file count.
- Allows user to choose between:
  - **Automatic Build Strategy:** Generates a tailored multi-stage container build without requiring a Dockerfile.
  - **Dockerfile Strategy:** Uses the existing Dockerfile in the project.
- Configures internal container listening port (pre-populated by detection: e.g. 3000, 8000, 8080).
- Configures health/readiness check strategy (`auto`, `http`, `tcp`, `none`) and HTTP path.
- Customizes optional build and start commands.


