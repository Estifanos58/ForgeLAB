# ForgeLAB — GitHub Integration Architecture

**Status:** FUTURE FEATURE — ARCHITECTURE DECIDED, IMPLEMENTATION NOT STARTED  
**Priority:** Post-MVP Phase  
**MVP Reality Check:** **Not implemented in current MVP codebase.** The current MVP strictly uses local host filesystem paths (`source_type: "local"`).  

---

## Related Documents

- [INSTRUCTION.md](../INSTRUCTION.md) — Operational memory & architectural constraints
- [docs/00-current-state.md](00-current-state.md) — Current state snapshot & matrix
- [docs/02-functional-requirements.md](02-functional-requirements.md) — R4 GitHub import requirements
- [docs/05-security-trust-boundaries.md](05-security-trust-boundaries.md) — Token encryption & untrusted source code trust model
- [docs/13-decisions.md](13-decisions.md) — DEC-008: Explicit OAuth + repository selection model

---

## 1. Product Decision: Why Arbitrary URL Cloning is Rejected

A critical architectural decision established early in ForgeLAB's design is:

> **ForgeLAB must NOT simply accept an arbitrary GitHub URL (e.g. `https://github.com/org/repo.git`) and clone whatever the backend can reach.**

### Rationale:
1. **Security & Boundary Leaks:** Blind cloning allows SSRF (Server-Side Request Forgery) attacks where a user triggers internal network discovery or accesses internal git mirrors via forge credentials.
2. **Access Control Integrity:** ForgeLAB must only access repositories that the user has explicitly authorized their ForgeLAB identity to view.
3. **Private Repository Support:** Public-only URL cloning breaks down for private enterprise and personal repositories.
4. **Foundation for Automated Webhooks:** True deployment automation requires bidirectional trust (OAuth App or GitHub App installation), enabling webhook subscription for push-to-deploy workflows.

---

## 2. Intended GitHub Integration Flow

The agreed user workflow mirrors developer platforms such as Vercel, Railway, Render, and Google AI Studio:

```text
┌──────────────┐
│  Developer   │
└──────┬───────┘
       │ 1. Clicks "Connect GitHub" in Account / Project Settings
       ▼
┌──────────────┐
│   ForgeLAB   │─── 2. Redirects to GitHub OAuth authorize URL ───►┌──────────────┐
│   Backend    │                                                   │    GitHub    │
└──────┬───────┘                                                   │   Platform   │
       │ 4. Exchanges authorization code for Access Token          └──────┬───────┘
       │    (Token encrypted at rest with AES-256-GCM)                    │
       │                                                                  │ 3. User grants
       ▼                                                                  │    repository
┌──────────────┐                                                          │    permissions
│ List Repos   │◄── 5. Calls GitHub API to list authorized repos ─────────┘
└──────┬───────┘
       │ 6. Renders authorized repository picker in UI
       ▼
┌──────────────┐
│ User Selects │─── 7. User selects Repository & default Branch
└──────┬───────┘
       ▼
┌──────────────┐
│ Import Proj. │─── 8. Creates Project with source_type: "github"
└──────────────┘
```

---

## 3. Core Functional Requirements for Implementation

### 1. Explicit User Authorization (OAuth 2.0)
- The integration will use a dedicated **GitHub App** (preferred for granular repository permissions) or **GitHub OAuth App**.
- The OAuth callback handler (`/api/auth/github/callback`) receives the temporary code, exchanges it with GitHub's token endpoint, and records the access token.

### 2. Encryption at Rest
- GitHub personal access tokens or OAuth installation tokens are classified as high-risk platform secrets.
- Tokens must **never** be stored in plaintext.
- Tokens must be encrypted using ForgeLAB's existing encryption service (`FORGELAB_ENCRYPTION_KEY` using AES-256-GCM with unique nonces) and stored in a dedicated `github_integrations` database table.

### 3. Repository & Branch Selection
- Before creating a project, the frontend queries `/api/auth/github/repositories` to display a searchable list of repositories permitted by the user's GitHub grant.
- The user explicitly chooses:
  - Repository full name (`owner/repo`)
  - Target deployment branch (e.g. `main` or `release`)
  - Build context & Dockerfile path within that repository.

### 4. Disconnect & Revocation Semantics
- Users must be able to disconnect GitHub at any time (`POST /api/auth/github/disconnect`).
- Disconnecting must:
  - Securely delete the encrypted access token from PostgreSQL.
  - Invalidate active webhook registrations on GitHub.
  - Retain historical deployment records, but prevent subsequent automated builds until reconnected.

---

## 4. Relationship to Future Webhooks & Git Push Automation

Once the GitHub App/OAuth authorization foundation is established:
1. **Webhook Registration:** When a project is imported, ForgeLAB registers a webhook endpoint (`/api/webhooks/github`) on the target repository with a cryptographically signed HMAC secret.
2. **Push Event Delivery:** On `git push` to the configured branch, GitHub delivers an event payload containing the new commit SHA.
3. **Automated Deployment:** ForgeLAB verifies the webhook signature, fetches the commit archive, creates a deployment record (`QUEUED`), and pushes the deployment job to the Redis queue.

---

## 5. Explicit Current Limitation

> **CRITICAL REMINDER FOR CODING AGENTS:**  
> **Do NOT implement GitHub OAuth or webhooks at this time.**  
> Existing local-path deployment functionality must undergo physical manual verification by the project owner before any work begins on GitHub integration.
