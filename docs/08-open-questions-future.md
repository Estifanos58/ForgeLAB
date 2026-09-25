# ForgeLab — Open Questions & Future Roadmap

**Status:** Current  
**Last Updated:** 2026-09-25  

---

## Open Questions (Unresolved)

These questions are explicitly documented as unresolved. They should be resolved deliberately through engineering reasoning, not silently assumed.

### OQ-1: Local Repository Path — Validation & Security

**Question:** How should ForgeLab validate that a local repository path is safe and actually contains a project?

**Current thinking:** 
- Validate the path exists on the host
- Check for a Dockerfile at the configured path
- Do NOT allow paths like `/etc`, `/var`, system directories
- Consider a configurable allowlist of base directories

**Status:** Defer validation details until Phase 2 implementation. Basic existence check for MVP.

---

### OQ-2: Docker Network Strategy

**Question:** How should deployed containers be networked?

**Options:**
1. Each container gets a mapped host port (simple, port conflicts)
2. Docker bridge network with ForgeLab managing port allocation
3. Docker network per project

**Current thinking:** Option 2 (bridge network + port allocation) for MVP. ForgeLab assigns an available host port to each project.

**Status:** Resolve during Phase 3 implementation.

---

### OQ-3: Build Log Retention

**Question:** How long should build and deployment logs be retained? Forever? Configurable TTL?

**Current thinking:** Keep all logs for MVP. Add retention policy as a future feature.

**Status:** Not a blocker for MVP.

---

### OQ-4: Concurrent Deployments for Same Project

**Question:** What happens if a user triggers a new deployment while one is already building?

**Current thinking:** 
- Option A: Reject — only one active deployment process per project at a time
- Option B: Queue — second deployment waits until first completes/fails

**Decision:** Option A for MVP. Simpler, avoids race conditions. Return 409 Conflict.

**Status:** Decided.

---

### OQ-5: Image Cleanup

**Question:** Should ForgeLab clean up old Docker images? How many should it keep?

**Current thinking:** Keep the last N images (e.g., 5) for rollback capability. Clean up older images.

**Status:** Defer to post-MVP. Images accumulate slowly enough for development.

---

### OQ-6: Container Restart Policy on Host Reboot

**Question:** If the host reboots, should ForgeLab automatically restart previously-running containers?

**Current thinking:** Docker's own restart policy (`unless-stopped`) handles this at the container level. ForgeLab should reconcile container state on startup.

**Status:** Defer reconciliation logic to post-MVP. Use Docker restart policy for basic behavior.

---

### OQ-7: WebSocket Reconnection Strategy

**Question:** How should the frontend handle WebSocket disconnections?

**Current thinking:** Client-side exponential backoff reconnection with re-subscription to previously subscribed channels.

**Status:** Resolve during Phase 9 (frontend).

---

## Future Roadmap (Post-MVP)

### Near-Term (After MVP Stable)

1. **GitHub OAuth Integration** — Connect GitHub, list repos, import
2. **Caddy Integration** — Automatic reverse proxy, subdomain routing
3. **Resource Limits** — CPU/memory caps per container
4. **Improved Health Checks** — Configurable interval, timeout, threshold

### Mid-Term

5. **Teams & RBAC** — Organizations, roles, permissions
6. **Audit Logging** — Track deployments, config changes, secret changes
7. **OpenTelemetry Observability** — CPU/memory metrics, request counts
8. **CLI Tool** — `forge deploy`, `forge logs`, etc.

### Long-Term

9. **Automatic Deployments** — GitHub webhooks trigger builds
10. **Self-Healing** — Automatic restart, backoff, rollback
11. **Zero-Downtime Deployments** — Blue/green or rolling strategy
12. **Multi-Environment** — dev/staging/production
13. **Multi-Node Workers** — Distributed deployment processing
14. **PostgreSQL Replication** — Infrastructure learning exercise

---

## Instructions for Future AI Agents

If you are a future AI agent continuing work on ForgeLab:

1. **Read all docs in `/docs/`** before writing any code
2. **Check the roadmap** to understand what phase the project is in
3. **Do not implement future features** unless the roadmap explicitly says they're next
4. **Do not change the technology stack** unless there's a documented reason
5. **Do not introduce microservices** — the system is a single control plane
6. **Do not introduce cloud dependencies** — the project must run locally at $0
7. **Preserve deployment safety invariants** — failed deploys never destroy working ones
8. **WebSocket isolation is non-negotiable** — read doc 06 before touching realtime code
9. **Secrets are never plaintext** — read doc 05 before touching secret/env var code
10. **Update documentation** when you make architectural decisions
11. **Document open questions** rather than silently inventing behavior
