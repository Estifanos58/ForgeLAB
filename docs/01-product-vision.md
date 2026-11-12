# ForgeLab — Product Vision & Scope

**Status:** Current Reference Specification  
**Architecture:** Single Control Plane Application Deployment Platform  

---

## Related Documents

- [INSTRUCTION.md](../INSTRUCTION.md) — Operational memory & mandatory agent workflow
- [docs/00-current-state.md](00-current-state.md) — Current state snapshot & matrix
- [docs/02-functional-requirements.md](02-functional-requirements.md) — MVP vs. future requirements
- [docs/04-architecture-decisions.md](04-architecture-decisions.md) — Architecture & data models
- [docs/13-decisions.md](13-decisions.md) — Architecture Decision Records (ADRs)
- [docs/14-known-limitations.md](14-known-limitations.md) — Current limitations & backlog

---

## What Is ForgeLab?

ForgeLab is a **self-hosted application deployment platform**. A developer gives ForgeLab a project repository, and ForgeLab manages the resulting application's lifecycle: building, deploying, running, monitoring, logging, restarting, rolling back, and recovering it.

ForgeLab is conceptually a small, self-hosted alternative to platforms like Render, Railway, or Heroku — but it is not a clone of those products.

## Core Product Value

A developer using ForgeLab should no longer need to manually:

- Clone a repository
- Build Docker images
- Start containers
- Check container state
- Read container logs
- Restart failed applications
- Track deployments
- Check health
- Manually reconstruct previous releases
- Configure reverse-proxy routing

ForgeLab provides these capabilities through a **web interface and API**.

## Target Users & Contexts

| Context | Description |
|---------|-------------|
| Personal server | A developer running ForgeLab on their own machine or server |
| VPS | A developer running it on their own VPS |
| Company internal | A company running it internally on dev/staging/production infrastructure |

## Non-Negotiable Constraints

1. **Self-hosted and local-first.** The core project must work without paid cloud infrastructure, paid databases, paid observability, OpenAI credits, Firebase, or other paid SaaS.
2. **Development cost: $0.** The development version runs locally.
3. **Not a generic CRUD app.** ForgeLab is not an e-commerce system, RAG application, or conventional microservice demo.
4. **Educational purpose.** The project must force meaningful learning in Go, deployment systems, container orchestration, networking, reliability, observability, security, and distributed-system concepts.

## What ForgeLab Is NOT

- A Kubernetes wrapper
- A cloud provisioning tool
- A multi-node scheduling system (initially)
- A full PaaS clone
- A microservices demo
- A marketing website with a dashboard

## Explicit Non-Goals for MVP & Single-Node Boundaries

### Implemented Beyond Original MVP Baseline (Post-MVP Stabilization)
- Custom domains with DNS TXT verification & Caddy automatic HTTPS (`proxy_unconfigured` fallback when Caddy is unset)
- GitHub push-to-deploy webhooks (`POST /api/webhooks/github`) & restart-recoverable GitHub imports
- Configurable runtime self-healing & automatic rollback preserving/verifying previous healthy containers
- Opt-in per-service `blue_green` zero-downtime switching (`TrafficRouter`)
- Persistent volume inspection, disk usage telemetry, backup streaming & bounded restore
- Backend-cached runtime metrics, PostgreSQL historical telemetry retention, redacted operational audit trail, & actionable system/project diagnostics

### Remaining Explicit Exclusions (Not Implemented)
- UI polish / decorative complexity
- Full RBAC / team organizations (single-owner + isolated guest session model active)
- Standalone general CLI tool (host agent CLI exists solely for local directory streaming)
- Multi-node scheduling / distributed workers
- Kubernetes integration
- Service mesh
- Cloud infrastructure provisioning
- Unnecessary distributed services
