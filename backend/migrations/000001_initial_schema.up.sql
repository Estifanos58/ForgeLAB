-- 000001_initial_schema.up.sql
-- ForgeLab initial database schema
-- Creates: users, projects, deployments, environment_variables, deployment_logs

-- Enable UUID generation
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ==========================================
-- Users
-- ==========================================
CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email VARCHAR(255) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    display_name VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_users_email ON users(email);

-- ==========================================
-- Projects
-- ==========================================
CREATE TABLE projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    slug VARCHAR(255) NOT NULL,
    source_type VARCHAR(50) NOT NULL DEFAULT 'local',
    repository_path TEXT NOT NULL DEFAULT '',
    branch VARCHAR(255) NOT NULL DEFAULT 'main',
    dockerfile_path VARCHAR(500) NOT NULL DEFAULT 'Dockerfile',
    build_context VARCHAR(500) NOT NULL DEFAULT '.',
    health_check_path VARCHAR(500) DEFAULT '/health',
    health_check_enabled BOOLEAN NOT NULL DEFAULT true,
    status VARCHAR(50) NOT NULL DEFAULT 'inactive',
    current_deployment_id UUID,
    port INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_projects_owner_slug UNIQUE(owner_id, slug)
);

CREATE INDEX idx_projects_owner_id ON projects(owner_id);
CREATE INDEX idx_projects_status ON projects(status);

-- ==========================================
-- Deployments
-- ==========================================
CREATE TABLE deployments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    deploy_number INTEGER NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'queued',
    commit_sha VARCHAR(255),
    branch VARCHAR(255) NOT NULL DEFAULT 'main',
    image_tag VARCHAR(500),
    container_id VARCHAR(255),
    started_at TIMESTAMPTZ,
    built_at TIMESTAMPTZ,
    deployed_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    duration_ms BIGINT,
    failure_reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_deployments_project_number UNIQUE(project_id, deploy_number)
);

CREATE INDEX idx_deployments_project_id ON deployments(project_id);
CREATE INDEX idx_deployments_status ON deployments(status);
CREATE INDEX idx_deployments_project_number ON deployments(project_id, deploy_number DESC);

-- ==========================================
-- Environment Variables (Secrets)
-- ==========================================
CREATE TABLE environment_variables (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    key VARCHAR(255) NOT NULL,
    encrypted_value BYTEA NOT NULL,
    is_secret BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_env_vars_project_key UNIQUE(project_id, key)
);

CREATE INDEX idx_env_vars_project_id ON environment_variables(project_id);

-- ==========================================
-- Deployment Logs
-- ==========================================
CREATE TABLE deployment_logs (
    id BIGSERIAL PRIMARY KEY,
    deployment_id UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    timestamp TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    phase VARCHAR(50) NOT NULL DEFAULT 'runtime',
    stream VARCHAR(20) NOT NULL DEFAULT 'stdout',
    message TEXT NOT NULL,

    CONSTRAINT chk_phase CHECK (phase IN ('source', 'build', 'startup', 'health', 'runtime')),
    CONSTRAINT chk_stream CHECK (stream IN ('stdout', 'stderr', 'system'))
);

CREATE INDEX idx_deployment_logs_deployment_id ON deployment_logs(deployment_id);
CREATE INDEX idx_deployment_logs_deployment_timestamp ON deployment_logs(deployment_id, timestamp);

-- ==========================================
-- Refresh Tokens
-- ==========================================
CREATE TABLE refresh_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash VARCHAR(255) NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked BOOLEAN NOT NULL DEFAULT false
);

CREATE INDEX idx_refresh_tokens_user_id ON refresh_tokens(user_id);
CREATE INDEX idx_refresh_tokens_token_hash ON refresh_tokens(token_hash);

-- Add foreign key for projects.current_deployment_id after deployments table exists
ALTER TABLE projects
    ADD CONSTRAINT fk_projects_current_deployment
    FOREIGN KEY (current_deployment_id) REFERENCES deployments(id)
    ON DELETE SET NULL;
