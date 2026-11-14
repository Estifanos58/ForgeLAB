# ForgeLAB Security Policy & Architecture Security Boundaries

ForgeLAB takes security seriously. This document outlines our security policies, how to report vulnerabilities, the architectural security boundaries of the platform, and the operational limitations of container isolation on a shared Docker daemon.

---

## 1. Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| Main (development branch) | :white_check_mark: |

Security patches are prioritized for the latest codebase on the `main` branch.

---

## 2. Reporting a Vulnerability

If you discover a potential security vulnerability within ForgeLAB, please report it responsibly:

- **Do NOT open a public GitHub issue.**
- Email details directly to the author via GitHub or security contact at [https://github.com/Estifanos58/ForgeLAB](https://github.com/Estifanos58/ForgeLAB).
- Include a detailed description of the vulnerability, step-by-step reproduction instructions, a proof-of-concept (PoC), and the affected ForgeLAB components.
- You will receive an acknowledgment within 48 hours and regular updates on remediation progress.

---

## 3. Docker Socket Isolation Limitations & Architecture Trust Model

### Root-Equivalence of the Docker Socket
In Docker's architecture, access to the Docker daemon socket (`/var/run/docker.sock` on Linux or `\\.\pipe\docker_engine` on Windows) is **equivalent to root access on the host operating system**. Any process capable of issuing API commands to the Docker daemon can create privileged containers, mount arbitrary host root filesystems (`/`), and modify host system files.

ForgeLAB's backend control-plane requires access to the Docker daemon to build images, manage networks, and run application containers. Because of this:
1. **The ForgeLAB control-plane must run in a trusted operational environment.**
2. **Mounting the Docker daemon socket into tenant/user application containers is strictly forbidden** and unconditionally blocked by ForgeLAB's deployment engine (`ErrDockerSocketMountRejected`).

### Multi-Tenancy Boundary Realities
Containers share the host kernel. While Linux namespaces, cgroups, and capabilities provide substantial isolation for trusted and semi-trusted workloads, container-level isolation is **not** a hardware hypervisor boundary:
- **Single-Host Shared Daemon:** Standard containers on a single Docker daemon share the host kernel and daemon runtime. A kernel zero-day vulnerability could allow a container breakout.
- **Untrusted Multi-Tenancy Requirements:** Environments hosting fully untrusted, adversarial multi-tenant code require hardware-enforced virtualization boundaries, such as:
  - MicroVM isolation (e.g., AWS Firecracker, Kata Containers).
  - Dedicated virtual machine instances per tenant.
  - Isolated Docker-in-Docker (DinD) daemons running inside unprivileged rootless namespaces or separate VMs.
- ForgeLAB is engineered for developers, engineering teams, and homelab / on-premise application orchestration. Deploying arbitrary untrusted code in a shared multi-tenant environment without hypervisor isolation is outside ForgeLAB's intended design.

---

## 4. Operational Mitigations & Security Controls

ForgeLAB enforces defense-in-depth mitigations across the deployment lifecycle:

### A. Container Security Restrictions
All application containers provisioned by ForgeLAB enforce strict security baselines:
- **Capability Dropping:** Containers drop all capabilities by default (`CapDrop: ["ALL"]`).
- **Minimal Allowlist:** Only explicitly required unprivileged capabilities are granted (`CHOWN`, `SETUID`, `SETGID`, `NET_BIND_SERVICE`).
- **No New Privileges:** Containers are launched with `SecurityOpt: ["no-new-privileges=true"]`, preventing escalation via setuid binaries.
- **No Docker Socket Mounting:** Volume mounting of `/var/run/docker.sock`, `docker.sock`, or Docker named pipes is strictly forbidden and triggers deployment failure.

### B. Dockerfile Security Validation & Fail-Closed Parsing
During image builds, ForgeLAB parses and inspects Dockerfiles with a strict fail-closed scanner:
- **Parser Directives:** Accurately tracks official Docker parser directives (`# escape=\` vs `# escape=`` `) to prevent escaping-based inspection bypasses.
- **Line Continuation:** Handles multiline continuation blocks with odd-vs-even escape counting and intermediate comment filtering.
- **Entitlement Checks:** Detects and rejects dangerous `--security` flags (e.g. `--security=insecure`) and host network modes (`--network=host`, `--network=container:...`).
- **Strict Size Limits:** Enforces a maximum Dockerfile size of 2 MB and individual line limit of 1 MB, rejecting null bytes and scanner errors immediately.

### C. Host Volume Path Isolation
The `PathValidator` ensures that tenant volume mounts cannot traverse into sensitive host directories:
- Strict path canonicalization resolving all symbolic links and relative segments.
- Allowlisting against administrator-approved source roots.
- Blocking access to root filesystems, OS binaries (`/bin`, `/sbin`, `/usr`, `C:\Windows`), Docker configuration directories, and system paths.

### D. Loopback Direct Port Binding & Public Gateway Protection
- **Default Loopback Binding:** Deployed application container host ports (`10000–60000`) bind to loopback (`127.0.0.1`) by default via `FORGELAB_APP_HOST_IP`, preventing accidental exposure to LAN or public WAN networks.
- **Public Gateway Controls:** External public sharing through ForgeLAB's gateway (`cmd/gateway`) or Cloudflare Quick Tunnels is protected by:
  - Cryptographic token-based route isolation (`share_token`).
  - Per-share active connection concurrency limits.
  - Per-share and per-visitor-IP sliding-window rate limiters returning HTTP 429 Too Many Requests.
  - Path traversal inspection blocking dot-dot (`..`) probes.

### E. Secrets Protection & Stream Redaction
- All project environment secrets are encrypted at rest using AES-256-GCM with unique 12-byte initialization vectors.
- Secret values are masked in console APIs (`••••••••`).
- Real-time `LogRedactor` scrubs known secret patterns (`[REDACTED]`) from build and runtime container log streams before persistence and WebSocket broadcast.
