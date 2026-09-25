You are Claude Opus 4.6 running inside Antigravity, and you are the implementation agent for this project.

Treat this instruction as a transfer of context from a senior engineering lead who has reviewed the original project discussion and subsequent clarifications. Your job is to continue from that context without reinterpreting the product into a different project.

The project is called **ForgeLab**.

Your first responsibility is not to rush into coding. Your first responsibility is to establish a faithful, durable understanding of the product and its engineering constraints, document that understanding for future agents, and only then begin implementation.

==================================================

1. PRODUCT VISION
   ==================================================

ForgeLab is a **self-hosted application deployment platform**.

The core product idea is:

> A developer gives ForgeLab a project repository, and ForgeLab manages the resulting application's lifecycle: building, deploying, running, monitoring, logging, restarting, and eventually rolling back and recovering it.

ForgeLab is conceptually a small, self-hosted alternative to platforms such as Render, Railway, or Heroku, but the purpose is not to clone every feature of those products.

The important product value is that a developer should no longer need to manually perform operations such as:

* cloning a repository
* building Docker images
* starting containers
* checking container state
* reading container logs
* restarting failed applications
* tracking deployments
* checking health
* manually reconstructing previous releases
* configuring reverse-proxy routing

ForgeLab should eventually provide those capabilities through a web interface and API.

The system is intended to be useful in multiple contexts:

* a developer running ForgeLab on their own machine or server
* a developer running it on their own VPS
* a company running it internally on development/staging/production infrastructure

The product must remain self-hosted and local-first. The core project should be possible to develop and operate without requiring paid cloud infrastructure, paid databases, paid observability products, OpenAI credits, Firebase, or other paid SaaS.

Do not turn ForgeLab into another generic CRUD application, e-commerce system, RAG application, or conventional microservice demo.

The purpose of this project is also educational: it should force meaningful learning in Go, deployment systems, container orchestration, networking, reliability, observability, security, and distributed-system concepts.

==================================================
2. IMPORTANT CONTEXT ABOUT WHY THE PROJECT EXISTS
=================================================

The project was deliberately chosen to move beyond the technologies and patterns already familiar to the developer.

The developer already has experience with TypeScript/NestJS, Spring Boot, PostgreSQL, Redis, Kafka, Docker, WebSockets, microservices, and full-stack development.

The use of **Go for the ForgeLab control plane is deliberate** because Go is a new learning dimension for the developer.

Do not replace Go with NestJS, Spring Boot, or another familiar backend technology simply because it would be easier.

Likewise, do not turn the project into a large Kubernetes-based infrastructure exercise simply because deployment platforms eventually become distributed.

The project should teach when additional infrastructure is justified, not assume that more infrastructure is automatically better.

==================================================
3. CURRENTLY ACCEPTED TECHNOLOGY DIRECTION
==========================================

The technology direction established in the discussion is:

* Backend/control plane: Go
* API style: REST
* Realtime communication: WebSocket
* Database: PostgreSQL
* Queue/background work coordination: Redis
* Application runtime/build environment: Docker
* Reverse proxy/network edge: Caddy
* Frontend: Next.js + TypeScript
* Observability/metrics direction: OpenTelemetry

Treat those as the current chosen direction unless a later explicit requirement in the source context overrides them.

Do not introduce alternative technologies merely because you personally prefer them.

Before implementation, verify current library/API behavior against authoritative documentation rather than relying on remembered APIs.

==================================================
4. THE MOST IMPORTANT ARCHITECTURAL CONSTRAINT
==============================================

Do not begin by creating a large collection of microservices.

The original discussion explicitly rejected starting with:

* many microservices
* Kubernetes
* AWS
* Terraform
* Kafka
* a service mesh
* a large distributed cluster

The initial system is intentionally centered around a single control-plane application with PostgreSQL, Redis, Docker, and the web application.

Worker separation can be introduced later when there is a real engineering reason.

The project should demonstrate that distributed architecture is earned by requirements such as concurrency, workload isolation, scale, failure domains, or operational need. It must not become a collection of services simply because "microservices are good."

Any architecture diagrams or repository trees shown in the earlier conversation were illustrative discussion material, not immutable implementation instructions. Do not blindly reproduce an earlier diagram or directory tree.

==================================================
5. PROJECT IMPORT REQUIREMENTS
==============================

There are two fundamentally different project-source workflows that must be supported:

### A. Local repository import

A user must be able to import a project from a local repository.

Do not silently reinterpret "local repository import" as merely entering a repository URL.

The exact transport/mechanism for making the user's local repository available to ForgeLab must be explicitly reasoned about and documented before implementation.

Do not invent a product behavior that was never agreed upon simply because it is convenient to implement.

### B. GitHub repository import

GitHub import must be **explicit, permission-based, and user initiated**.

The system must not simply accept an arbitrary GitHub URL and clone whatever the backend happens to be able to access.

The intended workflow is:

User
→ Connect GitHub
→ GitHub authentication/authorization
→ User explicitly grants access
→ ForgeLab shows repositories available through that authorization
→ User selects a repository
→ User explicitly chooses/imports the project

This is intentionally similar in spirit to the GitHub import experience used by products such as Google AI Studio, Vercel, Railway, and Render.

Important GitHub requirements:

* GitHub authentication must be explicit.
* Repository access must depend on what the user authorized.
* Private repositories must be supported.
* Repository selection must happen before project import.
* Branch selection must be possible.
* GitHub credentials/tokens must never be stored as plaintext.
* The GitHub connection should also provide a clean foundation for future webhooks and automatic deployments.

Do not weaken this into "give ForgeLab a repository URL and we will clone it."

==================================================
6. CORE USER WORKFLOW
=====================

The core product experience should eventually look like this:

A developer opens ForgeLab.

They authenticate.

They create/import a project.

They provide or select:

* project name
* repository/source
* branch
* build configuration where necessary

ForgeLab registers the project.

Registration alone must not imply that an application is already running.

The developer then deploys.

ForgeLab should:

* obtain the project source
* build the application
* create/run the Docker container
* track deployment state
* expose deployment logs
* perform health checking
* report whether the application is running

The developer should be able to manage that deployment from ForgeLab rather than manually using Docker commands.

The complete product eventually includes:

* build
* deployment
* runtime management
* logs
* health
* restart
* stop/start
* deployment history
* rollback
* networking/routing
* environment variables
* secrets
* resource limits
* monitoring
* auditability
* authentication
* eventual teams/RBAC
* eventual webhooks/automatic deployments
* eventual CLI
* eventual self-healing
* eventual zero-downtime deployment behavior

==================================================
7. DO NOT BUILD EVERYTHING AT ONCE
==================================

The most important initial implementation target is a narrow, end-to-end vertical slice.

The first useful ForgeLab should be able to take a project containing a Dockerfile and demonstrate the complete lifecycle:

* authenticate a ForgeLab user
* create/import a project
* configure the repository/source
* queue a deployment
* build the Docker image
* start the application container
* expose deployment status through the API
* stream live deployment/runtime logs to the browser
* perform a health check
* allow stop/start/restart
* record deployment history
* support a basic rollback concept

That is the first serious milestone.

Do not start with:

* UI polish
* custom domains
* advanced DNS management
* automatic GitHub webhooks
* complex auto-deployment workflows
* full RBAC
* CLI
* self-healing automation
* sophisticated zero-downtime rollout strategies
* multi-node scheduling
* Kubernetes
* service mesh
* cloud infrastructure
* unnecessary distributed services

Those are later capabilities.

==================================================
8. DOCKERFILE IS THE FOUNDATION
===============================

ForgeLab should be language-agnostic at its foundation.

A Dockerfile is the most important initial build contract.

Eventually the platform can detect common application types, including:

* Node.js / package.json
* Spring Boot / pom.xml
* Spring Boot / Gradle
* Python / requirements.txt
* Dockerfile-based applications

But automatic detection must not replace explicit Dockerfile control.

The important principle is:

> Dockerfile support is the foundation, and higher-level detection can be layered on top later.

Do not make ForgeLab depend on one programming language.

==================================================
9. DEPLOYMENT SAFETY INVARIANTS
===============================

These are important product behaviors, not optional polish.

A failed deployment must not automatically destroy the last working deployment.

Example:

* deployment 14 is working
* deployment 15 is attempted
* deployment 15 fails during build or health checking

Deployment 14 must remain available.

ForgeLab must record deployment 15 as failed with enough information to determine why.

Similarly, rollback must work conceptually like:

current version
→ start known-good previous version
→ health check
→ switch traffic only after success
→ stop/remove the broken version

Do not implement a deployment workflow where a bad release immediately leaves production unavailable simply because the new version was started first.

==================================================
10. DEPLOYMENT HISTORY
======================

Every deployment should become a durable deployment record.

The deployment history should preserve information such as:

* deployment identifier
* commit SHA where applicable
* branch
* status
* start time
* finish time
* duration
* resulting image/version
* build/deployment logs
* failure reason

Deployment history is important because ForgeLab is meant to manage application lifecycle rather than merely start containers.

Treat deployment records as operational history, not disposable UI state.

==================================================
11. HEALTH CHECKS
=================

ForgeLab should support application health checking.

A typical application may expose:

GET /health

ForgeLab should be able to determine states such as:

* RUNNING
* UNHEALTHY
* STOPPED
* CRASHED
* DEPLOYING

Health checks should eventually have configurable behavior such as:

* path
* interval
* timeout
* failure threshold

Do not hardcode assumptions that every application exposes exactly one fixed health endpoint forever.

The state transition logic must be documented before implementation.

==================================================
12. WEBSOCKET REQUIREMENT — CRITICAL ISOLATION RULE
===================================================

Realtime communication is a core ForgeLab requirement.

WebSockets are intended for things such as:

* deployment progress
* live build logs
* live runtime/container logs
* status changes
* eventually other operational events

However, **WebSocket traffic must be explicitly scoped to the correct project/deployment**.

This is a critical requirement from the current project clarification.

Events from one deployment must never appear in another deployment's dashboard.

Events from one project must never leak into another project.

Do not use a vague/global broadcast channel merely because it is easier.

Every realtime stream must have a clear identity boundary based on stable identifiers.

At minimum, the design must distinguish:

* user
* project
* deployment

Where a project has multiple deployments, events for deployment A must never appear in the UI for deployment B.

A project using the same repository as another project must still remain isolated.

Do not rely only on:

* project name
* repository URL
* container name
* branch name

as the realtime identity.

Use durable unique identifiers and make the identity explicit in the WebSocket protocol.

The server must also authorize subscriptions so that a user cannot subscribe to another user's project/deployment stream simply by guessing an identifier.

Document the WebSocket event contract before implementing it.

==================================================
13. LOGGING REQUIREMENTS
========================

Live logs are one of the most visible features of ForgeLab.

A deployment log stream should be able to show progress such as:

* cloning/source acquisition
* dependency installation
* Docker build
* image creation
* container startup
* health check
* successful completion
* failure

Runtime logs should also be streamable.

The pipeline conceptually involves:

container/runtime
→ log collection
→ ForgeLab
→ WebSocket
→ browser

Do not assume that "the browser refreshes every few seconds" is equivalent to the live-log requirement.

Realtime behavior is intentional.

Logs must not leak secrets.

Environment variables and sensitive credentials must never accidentally appear in the log stream.

==================================================
14. SECURITY REQUIREMENTS
=========================

Security is part of the product rather than a later cosmetic addition.

At minimum, the implementation must account for:

### ForgeLab authentication

ForgeLab users must be authenticated.

The platform's users are completely separate from the users of applications deployed onto ForgeLab.

Do not confuse:

* ForgeLab account identity
* deployed application account identity

### GitHub credentials

GitHub credentials/tokens must be encrypted at rest.

Do not store OAuth access tokens as ordinary plaintext database strings.

### Application secrets

Environment variables may contain sensitive information such as:

* DATABASE_URL
* REDIS_URL
* JWT_SECRET
* passwords
* API keys

Secrets must not be stored or displayed as ordinary plaintext throughout the system.

At a minimum, the system must establish a secure secret-storage strategy before implementing the production-facing secret workflow.

Secrets must also be redacted from logs.

### Authorization

A user must only be able to:

* view projects they are authorized to view
* deploy projects they are authorized to deploy
* retrieve logs for authorized deployments
* manage authorized project configuration
* manage authorized secrets

Realtime subscriptions must obey the same authorization boundaries.

### Container isolation

The system eventually needs resource limits and execution restrictions so one deployed application cannot arbitrarily consume the whole host.

Do not treat Docker as an automatic guarantee that every security problem is solved.

Document the threat model and the trust boundary before implementing features that execute arbitrary user-controlled repositories.

==================================================
15. RESOURCE MANAGEMENT
=======================

Eventually each deployment/project should support controls such as:

* CPU limit
* memory limit
* restart policy
* container timeout
* maximum build duration
* maximum log volume

This is important for running multiple applications on one machine.

Do not make resource scheduling the first feature, but do not design the early implementation in a way that makes resource constraints impossible to introduce later.

==================================================
16. GITHUB FUTURE WORKFLOW
==========================

Once GitHub integration exists, the architecture should leave room for:

GitHub push
→ webhook
→ ForgeLab
→ deployment
→ build
→ health check
→ release

Automatic deployment is a future feature.

Do not implement webhook automation before the foundational deployment lifecycle is stable.

When webhooks are eventually implemented, webhook authenticity/signature validation must be treated as a security requirement.

==================================================
17. NETWORKING / CADDY
======================

Caddy is the chosen reverse-proxy direction.

The long-term idea is that a developer can deploy applications and ForgeLab manages routing instead of forcing the developer to manually edit reverse-proxy configuration.

Eventually this can support domains such as:

* api.example.com
* frontend.example.com
* admin.example.com

and local development equivalents.

HTTPS automation is also a future concern.

Do not make custom domains and public HTTPS a prerequisite for the first deployment vertical slice.

==================================================
18. OBSERVABILITY
=================

OpenTelemetry is the chosen observability direction.

The long-term platform may expose:

* CPU usage
* memory usage
* container restarts
* request counts
* request latency
* HTTP errors
* deployment duration
* build duration

The dashboard should not be decorative.

Its purpose is to answer:

> What is happening on my server right now?

The UI should eventually make operational state obvious.

Do not build elaborate analytics before the fundamental lifecycle and realtime behavior work correctly.

==================================================
19. DASHBOARD / PRODUCT EXPERIENCE
==================================

The dashboard should communicate operational state, for example:

* number of projects
* running projects
* deploying projects
* failed projects
* recent deployments
* current deployment/version
* health
* basic resource information

A project view should eventually expose operations such as:

* Deploy
* Restart
* Stop
* Rollback

and information such as:

* current status
* deployment/version
* commit
* uptime
* resource state
* deployment history
* logs
* metrics
* settings

Do not optimize for decorative complexity.

Optimize for operational clarity.

==================================================
20. PROJECT SETTINGS
====================

The long-term project configuration model may include:

* General
* Build
* Environment
* Networking
* Resources
* Health
* Deployments
* Logs
* Access

Build configuration may eventually include:

* repository
* branch
* Dockerfile
* build command
* start command
* build arguments

Health configuration may eventually include:

* path
* interval
* timeout
* failure threshold

Do not assume every one of these features belongs in the first release.

==================================================
21. MULTI-USER / TEAMS
======================

The first implementation needs secure individual user authentication.

Later, ForgeLab should be capable of evolving into a team platform.

Future organization roles discussed include:

* Owner
* Admin
* Developer
* Viewer

Potential permissions include:

Viewer:

* see deployments
* see logs

Developer:

* deploy

Admin:

* change configuration

Owner:

* manage organization

Do not build a full organization/RBAC subsystem before the deployment lifecycle is stable.

But keep authorization boundaries explicit enough that adding it later is not a rewrite.

==================================================
22. AUDIT LOGGING
=================

A mature ForgeLab should record important actions such as:

* deployment
* configuration changes
* secret changes
* restart
* rollback

Audit records are important for accountability in a company environment.

Do not treat audit logging as a reason to over-engineer the MVP.

Record it as a future requirement and preserve the domain boundaries needed to add it cleanly.

==================================================
23. API-FIRST REQUIREMENT
=========================

ForgeLab must not depend exclusively on the browser UI.

Major operations should be available through a well-defined API.

The long-term direction includes operations such as:

* create project
* inspect project
* list deployments
* trigger deployment
* inspect deployment
* retrieve deployment logs
* rollback
* restart
* stop/start
* delete project

Eventually these APIs should be usable by a CLI and potentially other automation.

The API should therefore be treated as a first-class product surface, not merely an internal endpoint implementation behind the UI.

==================================================
24. CLI IS A FUTURE PRODUCT SURFACE
===================================

A later ForgeLab CLI may support commands conceptually like:

* forge login
* forge projects
* forge deploy
* forge logs
* forge restart
* forge rollback

This is not an MVP requirement.

Do not implement the CLI before the API and lifecycle are stable.

But do not design the backend around UI-only operations.

==================================================
25. FUTURE STAGING / PRODUCTION ENVIRONMENTS
============================================

The long-term product may support multiple environments such as:

* Development
* Staging
* Production

Each environment could eventually have its own:

* environment variables
* domains
* resources
* deployment history

This is a future consideration.

Do not force the MVP to become a multi-environment platform prematurely.

==================================================
26. FUTURE SELF-HEALING
=======================

Self-healing is one of the important future capabilities.

The intended idea is roughly:

container starts
→ health check
→ healthy
→ running

or:

application crashes
→ health failure
→ restart
→ health failure again
→ mark unhealthy
→ eventually rollback

This introduces:

* retry policies
* backoff
* failure thresholds
* state transitions
* recovery logic

Do not create an uncontrolled infinite restart loop.

Do not build this as the first milestone.

==================================================
27. FUTURE ZERO-DOWNTIME DEPLOYMENTS
====================================

Eventually ForgeLab may support:

old version running
→ new version starts
→ new version passes health checks
→ traffic moves to new version
→ old version stops

A failed new release should leave the last healthy version available.

Do not attempt to make the MVP a full zero-downtime orchestrator.

But ensure the deployment model does not make this future capability impossible.

==================================================
28. FUTURE AUTOMATED DEPLOYMENTS
================================

Automatic deployment from GitHub push events is a future feature.

The eventual flow is:

git push
→ GitHub webhook
→ ForgeLab creates deployment
→ build
→ health check
→ release

Manual deployment must work correctly first.

==================================================
29. FUTURE POSTGRES / INFRASTRUCTURE LEARNING
=============================================

The original discussion identified PostgreSQL replication/failover as an interesting future learning direction.

This does not mean you should add replication to the first version.

Treat it as a future infrastructure experiment after the core system is stable.

==================================================
30. COST CONSTRAINT
===================

The development version is intended to cost $0.

The core system should be capable of running locally.

Do not make the architecture dependent on:

* AWS
* paid VPS
* paid hosted PostgreSQL
* paid Redis
* paid observability SaaS
* OpenAI API credits
* Firebase
* other paid infrastructure

The project can eventually be deployed elsewhere, but paid cloud services are not part of the core requirement.

==================================================
31. DOCUMENTATION MUST COME BEFORE IMPLEMENTATION
=================================================

Before writing substantial production code, establish the project's durable engineering documentation.

Do not just create documentation after the code exists.

The documentation should capture the decisions that future coding agents need in order to work safely without rereading the original conversation.

At minimum, establish documentation covering:

1. Product vision and scope
2. Current functional requirements
3. Explicit non-goals
4. Current technology decisions
5. Architecture decisions
6. Deployment lifecycle/state behavior
7. Security and trust boundaries
8. GitHub authentication/import flow
9. Local repository import behavior
10. WebSocket/realtime contract and isolation rules
11. Deployment data/state model
12. Secret handling rules
13. Failure and recovery behavior
14. Testing/validation expectations
15. Local development/setup requirements
16. Current MVP boundaries
17. Future roadmap
18. Important rejected alternatives/approaches
19. Open questions and unresolved decisions
20. Instructions for future AI coding agents

The documentation must distinguish clearly between:

* current requirements
* implemented behavior
* future ideas
* unresolved questions
* historical discussion
* rejected decisions

Do not allow future agents to mistake a future feature for an MVP requirement.

==================================================
32. ARCHITECTURE WORK BEFORE CODE
=================================

Before substantial implementation, reason through the system at the architecture level.

In particular, explicitly settle and document:

* the project lifecycle
* the deployment lifecycle
* deployment state transitions
* what constitutes a project
* what constitutes a deployment
* how project identity differs from deployment identity
* how source/import identity is represented
* how authentication and authorization boundaries work
* how GitHub credentials are handled
* how secrets are protected
* how deployment work is queued
* how Docker operations are represented
* how logs move from the runtime to the browser
* how WebSocket subscriptions are authorized
* how WebSocket streams are isolated by project/deployment
* how a failed deployment relates to the previous healthy deployment
* how rollback is represented
* how the system can later evolve toward workers/multiple nodes without prematurely requiring them

Do not start implementation while these fundamental boundaries are ambiguous.

At the same time, do not spend unlimited time designing theoretical distributed infrastructure that the MVP does not need.

The goal is a clear, extensible foundation, not speculative over-engineering.

==================================================
33. PLANS MUST BE EXPLICIT
==========================

Before coding, create a clear implementation roadmap that is derived from the documented requirements.

The roadmap must show the dependency order of work rather than simply being a feature wishlist.

The first implementation target must remain the smallest complete end-to-end deployment lifecycle.

Later work should be clearly separated from that foundation.

Whenever a later decision changes the roadmap, update the durable project documentation rather than relying on conversation memory.

==================================================
34. DOCUMENTATION IS FOR FUTURE AI AGENTS
=========================================

Assume future implementation agents will not have access to this entire conversation.

The documentation therefore needs to act as the project's persistent memory.

A future agent should be able to answer:

* What is ForgeLab?
* What is currently implemented?
* What is explicitly not implemented?
* What decisions are already settled?
* Which assumptions are forbidden?
* What security boundaries exist?
* What identifiers define ownership and isolation?
* What does a deployment mean?
* How are live logs delivered?
* How is WebSocket traffic isolated?
* How is GitHub access granted?
* How are secrets handled?
* What is the next intended milestone?
* Which future features must not accidentally be implemented early?
* Why was the current architecture chosen?
* Which alternative approaches were rejected?

Do not rely on undocumented knowledge from this prompt.

==================================================
35. CHANGE CONTROL
==================

Whenever a new requirement is discovered, do not silently rewrite earlier behavior.

Determine whether the new information:

* confirms an existing decision
* modifies an existing decision
* replaces an earlier assumption
* introduces a new requirement
* is merely a future idea
* remains unresolved

Record important changes in the project documentation.

Historical decisions should remain understandable, but current decisions must be unmistakable.

==================================================
36. IMPORTANT ASSUMPTION-AVOIDANCE RULES
========================================

Do not make any of these assumptions:

* that GitHub repository URLs automatically imply permission
* that every repository is public
* that every application is Node.js
* that every application has the same health endpoint
* that project name is a globally unique identity
* that repository URL is a globally unique identity
* that deployment name is enough for realtime isolation
* that one project can only have one deployment
* that build failure means the currently running version should be stopped
* that a browser-only implementation is sufficient
* that WebSockets can safely broadcast globally
* that Docker alone solves arbitrary-code security
* that all future features need to be implemented now
* that microservices are required because ForgeLab is a deployment platform
* that cloud infrastructure is required to validate the project
* that illustrative diagrams or directory trees from earlier discussion are binding architecture decisions
* that an implementation detail should become a product requirement simply because it is easy to code

When something is not specified, document it as an open question and resolve it deliberately rather than silently inventing product behavior.

==================================================
37. WHAT YOU SHOULD BUILD FIRST
===============================

After the documentation and architecture groundwork are complete, start with the smallest complete vertical slice of ForgeLab.

The first implementation should prioritize the real product loop:

Authenticate
→ import/create project
→ configure a Dockerfile-based source
→ trigger deployment
→ queue/build
→ run container
→ record deployment
→ expose status through API
→ stream scoped logs over WebSocket
→ perform health checks
→ allow stop/start/restart
→ preserve deployment history
→ provide a safe basic rollback path

The first slice should be demonstrable end-to-end.

Do not spend the first implementation cycle building:

* a sophisticated marketing landing page
* advanced dashboard animations
* custom-domain systems
* teams/RBAC
* automated GitHub deployments
* CLI
* self-healing orchestration
* zero-downtime releases
* multi-node workers
* Kubernetes
* cloud provisioning

Those belong after the foundation works.

==================================================
38. IMPLEMENTATION EXPECTATIONS
===============================

Implement the product as a real engineering project, not as a mockup.

Prefer:

* explicit state transitions
* durable identifiers
* validated API contracts
* authorization at backend boundaries
* deterministic deployment behavior
* observable operations
* meaningful automated tests
* integration coverage for infrastructure behavior
* strong error handling
* secure secret handling
* clear separation between current and future capabilities

Critical behaviors must be testable, especially:

* authentication/authorization
* project ownership
* GitHub authorization boundaries
* deployment state transitions
* failed deployment preservation
* rollback behavior
* health-check transitions
* WebSocket authorization
* WebSocket project/deployment isolation
* secret redaction
* deployment history consistency

Do not consider "the UI appears to work" sufficient validation for infrastructure behavior.

==================================================
39. FINAL OPERATING PRINCIPLE
=============================

Build ForgeLab as a coherent product, not as a collection of disconnected technologies.

The dashboard is not the product.

Docker is not the product.

WebSockets are not the product.

Go is not the product.

The product is the **self-hosted application deployment lifecycle**.

Everything else exists to make that lifecycle reliable, observable, secure, and usable.

Begin by absorbing this context, inspecting the actual project/environment you are working in, and establishing the durable project documentation and architecture/implementation groundwork described above.

Do not silently reinterpret the product.

Do not skip directly into UI coding.

Do not expand the MVP because an advanced feature sounds interesting.

Do not replace the chosen technology direction with a more familiar stack.

Do not introduce infrastructure complexity merely for the appearance of sophistication.

When a decision is unclear, identify the uncertainty explicitly, preserve it in the project documentation, and resolve it through engineering reasoning and current authoritative technical documentation before committing the implementation.

Your implementation should make it possible for a future AI agent to continue the project safely without needing to reconstruct the original conversation from scratch.
