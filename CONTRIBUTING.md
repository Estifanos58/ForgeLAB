# Contributing to ForgeLAB

Thank you for your interest in contributing to ForgeLAB! We welcome contributions that align with our focus on reliability, security, and developer ergonomics.

---

## Architecture Principles & Critical Invariants

Before contributing code, please ensure you understand and adhere to the core architectural invariants of ForgeLAB:

1. **Safety Invariant:** A failed deployment must **never** replace or degrade an existing healthy service container. Automatic rollback and previous container verification must be preserved.
2. **Fail-Closed Security:** Validation of Dockerfiles, source paths, volume mounts, and network permissions must fail closed. Parsing errors or oversized inputs must trigger an error, never silent bypasses.
3. **Container Security Boundaries:** Never weaken container security options. All application containers drop capabilities (`CapDrop: ["ALL"]`), enforce `no-new-privileges=true`, and forbid mounting the host Docker socket.
4. **Guest Mode Compatibility:** Guest mode is enabled by default to allow zero-barrier local exploration. New features and API endpoints must remain fully functional with Guest mode.
5. **No Unnecessary Complexity:** ForgeLAB is a streamlined platform. Avoid introducing heavy microservices, external orchestration frameworks, or unneeded third-party dependencies when standard Go and Docker abstractions suffice.

---

## Development Setup

### Prerequisites
- **Go:** 1.22 or higher
- **Node.js:** 18 or higher (with npm)
- **Docker:** Docker Desktop or Docker Engine running locally
- **PostgreSQL & Redis:** (Provided via `docker-compose.yml`)

### Running the Stack Locally
1. Start infrastructure services:
   ```bash
   docker compose up -d postgres redis
   ```
2. Run database migrations:
   ```bash
   cd backend
   go run ./cmd/migrate
   ```
3. Run the backend server:
   ```bash
   cd backend
   go run ./cmd/server
   ```
4. Run the frontend development server:
   ```bash
   cd frontend
   npm install
   npm run dev
   ```

---

## Testing & Quality Assurance

All changes must include relevant automated unit, regression, or integration tests:

### Running Backend Tests
From the `backend` directory:
```bash
# Run all unit tests
go test ./internal/...

# Run targeted package tests
go test -v ./internal/docker
go test -v ./internal/gateway
go test -v ./internal/tunnel
go test -v ./internal/security
```

### Running Frontend Tests
From the `frontend` directory:
```bash
npm test
```

### Static Analysis & Verification
Ensure code is properly formatted and passes standard linting:
```bash
# Backend
go vet ./...

# Frontend
npm run lint
```

---

## Pull Request Guidelines

1. **Branch Naming:** Use descriptive branch names (e.g. `fix/dockerfile-validation`, `feat/gateway-rate-limiting`).
2. **Commit Messages:** Follow standard conventional commit format (`fix: ...`, `feat: ...`, `docs: ...`, `test: ...`).
3. **Documentation:** Update relevant documents in `docs/` if modifying architecture, configuration parameters, or limitations. Use repository-relative links rather than machine-specific absolute file paths.
4. **Licensing:** By contributing, you agree that your contributions will be licensed under the terms established by the repository author.
