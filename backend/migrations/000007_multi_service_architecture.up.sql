-- 000007_multi_service_architecture.up.sql
-- Introduce multi-service domain model: sources, services, service_deployments, and service-scoped logging

-- 1. Create sources table
CREATE TABLE IF NOT EXISTS sources (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_type VARCHAR(50) NOT NULL DEFAULT 'local_agent', -- 'local_agent', 'local_upload', 'github', 'local_directory'
    source_reference TEXT NOT NULL DEFAULT '',
    agent_id VARCHAR(100) DEFAULT '',
    fingerprint VARCHAR(255) DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_sources_owner_id ON sources(owner_id);
CREATE INDEX IF NOT EXISTS idx_sources_type ON sources(source_type);

-- 2. Add source_id foreign key to projects
ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS source_id UUID REFERENCES sources(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_projects_source_id ON projects(source_id);

-- 3. Create services table
CREATE TABLE IF NOT EXISTS services (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source_id UUID REFERENCES sources(id) ON DELETE SET NULL,
    name VARCHAR(255) NOT NULL,
    role VARCHAR(50) NOT NULL DEFAULT 'other', -- 'frontend', 'backend', 'worker', 'other'
    source_path TEXT NOT NULL DEFAULT '.',
    runtime_type VARCHAR(100) NOT NULL DEFAULT 'generic',
    framework VARCHAR(100) NOT NULL DEFAULT '',
    package_manager VARCHAR(50) NOT NULL DEFAULT '',
    build_strategy VARCHAR(50) NOT NULL DEFAULT 'auto',
    build_candidates JSONB NOT NULL DEFAULT '[]'::jsonb,
    build_command TEXT NOT NULL DEFAULT '',
    start_command TEXT NOT NULL DEFAULT '',
    dockerfile_path VARCHAR(500) NOT NULL DEFAULT 'Dockerfile',
    build_context VARCHAR(500) NOT NULL DEFAULT '.',
    internal_port INTEGER NOT NULL DEFAULT 8080,
    host_port INTEGER,
    public_exposed BOOLEAN NOT NULL DEFAULT true,
    health_strategy VARCHAR(50) NOT NULL DEFAULT 'auto',
    health_check_path VARCHAR(500) DEFAULT '/health',
    health_check_enabled BOOLEAN NOT NULL DEFAULT true,
    status VARCHAR(50) NOT NULL DEFAULT 'inactive', -- 'inactive', 'deploying', 'running', 'stopped', 'failed'
    container_id VARCHAR(255),
    image_tag VARCHAR(500),
    current_service_deployment_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_services_project_name UNIQUE(project_id, name)
);

CREATE INDEX IF NOT EXISTS idx_services_project_id ON services(project_id);
CREATE INDEX IF NOT EXISTS idx_services_status ON services(status);

-- 4. Create service_deployments table
CREATE TABLE IF NOT EXISTS service_deployments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_id UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    status VARCHAR(50) NOT NULL DEFAULT 'queued',
    image_tag VARCHAR(500),
    container_id VARCHAR(255),
    host_port INTEGER,
    internal_port INTEGER,
    build_strategy VARCHAR(50),
    build_command TEXT,
    start_command TEXT,
    runtime_type VARCHAR(100),
    started_at TIMESTAMPTZ,
    built_at TIMESTAMPTZ,
    deployed_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    duration_ms BIGINT,
    failure_reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_service_deployments UNIQUE(deployment_id, service_id)
);

CREATE INDEX IF NOT EXISTS idx_service_deployments_deployment ON service_deployments(deployment_id);
CREATE INDEX IF NOT EXISTS idx_service_deployments_service ON service_deployments(service_id);
CREATE INDEX IF NOT EXISTS idx_service_deployments_status ON service_deployments(status);

-- 5. Scope deployment_logs to individual services
ALTER TABLE deployment_logs
    ADD COLUMN IF NOT EXISTS service_id UUID REFERENCES services(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_deployment_logs_deployment_service ON deployment_logs(deployment_id, service_id);

-- 6. Backfill existing projects with a primary service record for full backwards compatibility
INSERT INTO services (
    id, project_id, name, role, source_path, runtime_type, framework, build_strategy,
    build_command, start_command, dockerfile_path, build_context, internal_port, host_port,
    health_strategy, health_check_path, health_check_enabled, status, container_id,
    created_at, updated_at
)
SELECT
    gen_random_uuid(),
    p.id,
    COALESCE(NULLIF(p.name, ''), 'default'),
    CASE 
        WHEN p.runtime_type IN ('nextjs', 'react-vite', 'vue', 'angular') THEN 'frontend'
        WHEN p.runtime_type IN ('go', 'python-fastapi', 'python-flask', 'python-django', 'java', 'java-maven', 'java-gradle', 'rust') THEN 'backend'
        ELSE 'other'
    END,
    '.',
    COALESCE(NULLIF(p.runtime_type, ''), 'generic'),
    COALESCE(NULLIF(p.runtime_type, ''), 'generic'),
    COALESCE(NULLIF(p.build_strategy, ''), 'auto'),
    COALESCE(p.build_command, ''),
    COALESCE(p.start_command, ''),
    COALESCE(NULLIF(p.dockerfile_path, ''), 'Dockerfile'),
    COALESCE(NULLIF(p.build_context, ''), '.'),
    COALESCE(p.internal_port, 8080),
    p.port,
    COALESCE(NULLIF(p.health_strategy, ''), 'auto'),
    p.health_check_path,
    p.health_check_enabled,
    p.status,
    d.container_id,
    p.created_at,
    p.updated_at
FROM projects p
LEFT JOIN deployments d ON d.id = p.current_deployment_id
WHERE NOT EXISTS (
    SELECT 1 FROM services s WHERE s.project_id = p.id
);

-- Backfill service_deployments for existing deployments
INSERT INTO service_deployments (
    id, deployment_id, service_id, status, image_tag, container_id, host_port,
    internal_port, build_strategy, build_command, start_command, runtime_type,
    started_at, built_at, deployed_at, finished_at, duration_ms, failure_reason, created_at
)
SELECT
    gen_random_uuid(),
    d.id,
    s.id,
    d.status,
    d.image_tag,
    d.container_id,
    p.port,
    COALESCE(d.internal_port, s.internal_port),
    d.build_strategy,
    d.build_command,
    d.start_command,
    d.runtime_type,
    d.started_at,
    d.built_at,
    d.deployed_at,
    d.finished_at,
    d.duration_ms,
    d.failure_reason,
    d.created_at
FROM deployments d
JOIN projects p ON p.id = d.project_id
JOIN services s ON s.project_id = p.id
WHERE NOT EXISTS (
    SELECT 1 FROM service_deployments sd WHERE sd.deployment_id = d.id AND sd.service_id = s.id
);

-- Associate existing deployment logs with the service
UPDATE deployment_logs dl
SET service_id = s.id
FROM deployments d
JOIN services s ON s.project_id = d.project_id
WHERE dl.deployment_id = d.id AND dl.service_id IS NULL;
