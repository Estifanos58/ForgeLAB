-- 000004_source_and_build_abstractions.up.sql
-- Introduce universal source, build strategy, runtime configuration, and GitHub integrations

-- 1. Extend projects table
ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS source_reference TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS build_strategy VARCHAR(50) NOT NULL DEFAULT 'auto',
    ADD COLUMN IF NOT EXISTS build_command TEXT DEFAULT '',
    ADD COLUMN IF NOT EXISTS start_command TEXT DEFAULT '',
    ADD COLUMN IF NOT EXISTS runtime_type VARCHAR(50) NOT NULL DEFAULT 'generic',
    ADD COLUMN IF NOT EXISTS internal_port INTEGER NOT NULL DEFAULT 8080,
    ADD COLUMN IF NOT EXISTS health_strategy VARCHAR(50) NOT NULL DEFAULT 'auto';

-- 2. Extend deployments table
ALTER TABLE deployments
    ADD COLUMN IF NOT EXISTS source_revision VARCHAR(255),
    ADD COLUMN IF NOT EXISTS build_strategy VARCHAR(50),
    ADD COLUMN IF NOT EXISTS build_command TEXT,
    ADD COLUMN IF NOT EXISTS start_command TEXT,
    ADD COLUMN IF NOT EXISTS runtime_type VARCHAR(50),
    ADD COLUMN IF NOT EXISTS internal_port INTEGER,
    ADD COLUMN IF NOT EXISTS health_strategy VARCHAR(50);

-- 3. Create github_integrations table for secure token storage
CREATE TABLE IF NOT EXISTS github_integrations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    encrypted_access_token BYTEA NOT NULL,
    github_user_id VARCHAR(255) NOT NULL,
    github_username VARCHAR(255) NOT NULL,
    scope VARCHAR(500) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_github_integrations_user UNIQUE(user_id)
);

CREATE INDEX IF NOT EXISTS idx_github_integrations_user_id ON github_integrations(user_id);
