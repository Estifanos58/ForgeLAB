# ForgeLAB — Frontend Architecture & UI Specification

**Status:** Current Implementation Documentation  
**Framework:** Next.js 14 (App Router) + React 18 + TypeScript  
**Styling:** Vanilla CSS (`globals.css`) with CSS custom properties (Dark Mode theme)  
**Location:** `frontend/`  

---

## Related Documents

- [INSTRUCTION.md](../INSTRUCTION.md) — Operational memory & architectural constraints
- [docs/00-current-state.md](00-current-state.md) — Current state snapshot & matrix
- [docs/06-websocket-contract.md](06-websocket-contract.md) — WebSocket message formats & isolation
- [docs/09-api-contract.md](09-api-contract.md) — Backend REST and WebSocket contracts
- [docs/12-manual-verification.md](12-manual-verification.md) — Verification procedures for frontend pages

---

## 1. Architecture Overview & Design Goals

The ForgeLAB frontend is an operational dashboard designed for application lifecycle management. It focuses on clarity, real-time log observation, and explicit deployment state feedback.

```text
┌─────────────────────────────────────────────────────────────┐
│                      Next.js 14 App                         │
│                                                             │
│   ┌───────────────┐  ┌───────────────┐  ┌───────────────┐   │
│   │    /login     │  │   /register   │  │  /dashboard   │   │
│   └───────────────┘  └───────────────┘  └───────┬───────┘   │
│                                                 │           │
│                                                 ▼           │
│                                         ┌───────────────┐   │
│                                         │ /projects/:id │   │
│                                         └───────┬───────┘   │
│                                                 │           │
│                                ┌────────────────┴────────┐  │
│                                ▼                         ▼  │
│                        ┌──────────────┐         ┌───────────┤
│                        │    api.ts    │         │useWebSock.│
│                        │ (REST Client)│         │(WS Hook)  │
│                        └──────┬───────┘         └─────┬─────┘
└───────────────────────────────┼───────────────────────┼─────┘
                                │ REST                  │ WS
                                ▼                       ▼
                 ┌────────────────────────────────────────────┐
                 │          ForgeLAB Go Backend (8080)        │
                 └────────────────────────────────────────────┘
```

---

## 2. Directory & Page Structure

```text
frontend/
├── src/
│   ├── app/
│   │   ├── layout.tsx         # Root layout with dark background & Inter font
│   │   ├── globals.css        # Theme variables, glassmorphism, & status badges
│   │   ├── page.tsx           # Root redirect to /dashboard
│   │   ├── login/
│   │   │   └── page.tsx       # Authentication login form
│   │   ├── register/
│   │   │   └── page.tsx       # User registration form
│   │   ├── dashboard/
│   │   │   └── page.tsx       # Project list & creation modal
│   │   └── projects/[id]/
│   │       └── page.tsx       # Project view: controls, logs, history, secrets
│   └── lib/
│       ├── api.ts             # Typed REST API client
│       └── useWebSocket.ts    # Realtime WebSocket hook
├── package.json
└── tsconfig.json
```

---

## 3. Page Responsibilities & Flows

### 1. `/login` (`src/app/login/page.tsx`)
- **Purpose:** Authenticates existing users with email and password.
- **Workflow:**
  1. Submits credentials to `api.login({ email, password })`.
  2. Extracts `res.tokens.access_token` and writes it to `localStorage.setItem('forgelab_token', ...)`.
  3. Directs the user to `/dashboard`.
- **Error Handling:** Displays an inline red alert banner if credentials fail or network errors occur.

### 2. `/register` (`src/app/register/page.tsx`)
- **Purpose:** Creates a new ForgeLAB developer account.
- **Workflow:**
  1. Captures `display_name`, `email`, and `password`.
  2. Submits to `api.register(...)`.
  3. On success, persists `forgelab_token` in `localStorage` and routes to `/dashboard`.
- **Validation:** Enforces minimum 8-character password constraint client-side and server-side.

### 3. `/dashboard` (`src/app/dashboard/page.tsx`)
- **Purpose:** High-level project index and project creation entry point.
- **Workflow:**
  1. On mount, calls `api.me()` to verify JWT validity. If it throws an error (e.g. 401), redirects to `/login`.
  2. Concurrently calls `api.listProjects()` to populate project cards.
  3. Clicking "+ Create Project" opens a modal capturing:
     - Project Name
     - Repository Path (Host directory)
     - Branch (defaults to `main`)
     - Dockerfile Path (defaults to `Dockerfile`)
     - Build Context (defaults to `.`)
     - Health Check Path (defaults to `/health`)
  4. On submit, calls `api.createProject(...)`, closes the modal, and routes immediately to `/projects/<new-id>`.
  5. The "Logout" button invokes `api.logout()`, removes `forgelab_token` from `localStorage`, and navigates to `/login`.

### 4. `/projects/[id]` (`src/app/projects/[id]/page.tsx`)
- **Purpose:** Primary operational console for an individual project.
- **Layout:**
  - **Header:** Project name, current status badge, and action bar:
    - `🚀 Deploy Release`: Queues a new release.
    - `Stop`: Stops running container (visible when status is `running`).
    - `Start`: Starts stopped container (visible when status is `stopped`).
    - `Restart`: Restarts active container (visible when status is `running`).
    - `↺ Rollback`: Rollback to previous known-good deployment.
    - `Delete`: Cleans up all project containers and deletes project.
  - **Left Sidebar:**
    1. *Configuration Card:* Repository path, branch, Dockerfile, and live host port link (`http://localhost:<port>`).
    2. *Deployment History:* Scrollable list of all project deployments with status badges and timestamps. Clicking any deployment selects it as `activeDeployment`.
    3. *Secrets & Env Vars:* List of configured environment variables (masked if secret) and form to add new ones.
  - **Right Main Panel:**
    - *Terminal Output Box:* Realtime and historical log viewer with colored streams (`stdout` = white, `stderr` = red, `system` = cyan) and auto-scrolling.
    - *Connection Indicator:* Live status dot indicating WebSocket connection state.

---

## 4. State Ownership & Data Synchronization

### Authoritative State vs. Ephemeral Streaming
- **REST is Authoritative:** PostgreSQL (accessed via REST endpoints) is the sole authoritative source of truth for project configuration, deployment status, and durable logs.
- **WebSocket is Ephemeral:** WebSocket messages deliver low-latency visual feedback (live log lines and status change transitions). WebSocket messages are **not** persisted on the client.

### Data Loading Sequence in `/projects/[id]`
```text
Component Mount (or Project ID change)
        │
        ▼
   loadData()
        ├──► api.getProject(id)
        ├──► api.listDeployments(id)
        │         │
        │         ▼
        │    Identify activeDeployment (current_deployment_id or latest)
        │         │
        │         ├──► setWsChannel("deployment:<activeDeployment.id>")
        │         └──► api.getDeploymentLogs(id, activeDeployment.id)
        └──► api.listEnvVars(id)
```

### Event-Driven Refresh Loop
The component observes the `statusChange` state emitted by `useWebSocket`:
```typescript
useEffect(() => {
  if (statusChange) {
    loadData(); // Re-sync authoritative state from REST API
  }
}, [statusChange]);
```
When a deployment transitions through states (e.g. `building` → `starting` → `health_checking` → `running`), the server publishes a `status_change` event. The frontend receives this event and immediately triggers `loadData()` to re-fetch the project status, updated port, and deployment list.

### Deployment History Selection & Switching
When a user clicks a previous deployment in the Deployment History panel:
1. `setActiveDeployment(selectedDeploy)` is set.
2. `setWsChannel("deployment:<selectedDeploy.id>")` is invoked.
3. The `useWebSocket` hook automatically tears down the previous socket/subscription and subscribes to the newly selected channel.
4. `api.getDeploymentLogs(projectId, selectedDeploy.id)` is called to load that deployment's historical logs into `logsList`.
5. The terminal renders historical logs followed by any live streaming logs.

---

## 5. Core Libraries & Utilities

### 1. REST Client (`frontend/src/lib/api.ts`)
- Configures `API_BASE`: Uses `/api` in browser context (leveraging Next.js rewrites/proxy) or `NEXT_PUBLIC_API_URL` during SSR.
- Centralizes JWT header injection:
  ```typescript
  const token = typeof window !== 'undefined' ? localStorage.getItem('forgelab_token') : null;
  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }
  ```
- Includes `credentials: 'include'` on all requests to ensure cookies are transmitted.
- Centralizes error parsing: extracts `errData.error` from server responses and throws typed JavaScript `Error` instances.

### 2. WebSocket Hook (`frontend/src/lib/useWebSocket.ts`)
- **Hook Signature:** `useWebSocket(channel: string | null)`
- **Responsibilities:**
  - Manages native browser `WebSocket` connection lifecycle.
  - Constructs URL with token query param:
    ```typescript
    const wsUrl = `${protocol}//${host}/api/ws?token=${encodeURIComponent(token)}`;
    ```
  - On connection `open`, transmits subscription request:
    ```json
    { "type": "subscribe", "channel": "deployment:<uuid>" }
    ```
  - Parses incoming JSON frames:
    - If `msg.type === 'log'`, appends `msg.data` to `logs` array.
    - If `msg.type === 'status_change'`, sets `statusChange` state.
  - Handles cleanup: On channel change or unmount, sends `{ "type": "unsubscribe", "channel": ... }` and closes socket.
  - Exposes `clearLogs()` to reset log buffer before new deployments.

---

## 6. Current Implementation Limitations & Hardening Backlog

1. **`localStorage` Token Storage:** The access token is stored in `localStorage` (`forgelab_token`), which is vulnerable to XSS. In production, authentication should transition to purely HttpOnly, Secure, SameSite cookies.
2. **WebSocket Query String Token:** The JWT is transmitted in the query string (`/api/ws?token=...`). URLs can be logged by proxies and intermediate gateways. Production hardening should authenticate the upgrade via HttpOnly cookie or an initial auth frame.
3. **No Automatic Refresh Token Loop:** The frontend does not currently intercept 401 responses to call `/api/auth/refresh`. When the 15-minute access token expires, requests fail and redirect the user to `/login`.
4. **Log Rendering Virtualization:** Terminal log lines are stored in React component state and appended directly to the DOM. Deployments producing tens of thousands of log lines will cause memory and rendering bottlenecks. A virtualized list (e.g. `react-window`) should be implemented for high-volume logs.
5. **Unused Project Channel:** The frontend only subscribes to `deployment:<uuid>`. It does not subscribe to `project:<uuid>` because the backend currently has no active producers for project-level events.
