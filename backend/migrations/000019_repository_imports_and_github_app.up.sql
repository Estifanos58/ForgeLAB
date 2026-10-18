-- 1. Extend github_integrations with auth_type
ALTER TABLE github_integrations
    ADD COLUMN IF NOT EXISTS auth_type VARCHAR(50) NOT NULL DEFAULT 'oauth';

-- 2. Create github_app_installations for GitHub App installations
CREATE TABLE IF NOT EXISTS github_app_installations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    installation_id BIGINT NOT NULL,
    account_login VARCHAR(255) NOT NULL,
    account_id BIGINT NOT NULL,
    account_type VARCHAR(50) NOT NULL DEFAULT 'User',
    account_avatar_url VARCHAR(500) NOT NULL DEFAULT '',
    repository_selection VARCHAR(50) NOT NULL DEFAULT 'all',
    permissions JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_github_app_installations_user_inst UNIQUE (user_id, installation_id)
);

CREATE INDEX IF NOT EXISTS idx_github_app_installations_user_id ON github_app_installations(user_id);
CREATE INDEX IF NOT EXISTS idx_github_app_installations_account_login ON github_app_installations(account_login);

-- 3. Create repository_imports table for durable import lifecycle
CREATE TABLE IF NOT EXISTS repository_imports (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_type VARCHAR(50) NOT NULL DEFAULT 'github',
    repository_id VARCHAR(255) NOT NULL DEFAULT '',
    repository_full_name VARCHAR(255) NOT NULL,
    is_private BOOLEAN NOT NULL DEFAULT false,
    branch VARCHAR(255) NOT NULL DEFAULT '',
    commit_sha VARCHAR(64) NOT NULL DEFAULT '',
    root_dir VARCHAR(500) NOT NULL DEFAULT '',
    snapshot_path VARCHAR(1000) NOT NULL DEFAULT '',
    status VARCHAR(50) NOT NULL DEFAULT 'pending',
    progress INTEGER NOT NULL DEFAULT 0,
    stage VARCHAR(100) NOT NULL DEFAULT 'Initializing import',
    error_message TEXT NOT NULL DEFAULT '',
    error_code VARCHAR(100) NOT NULL DEFAULT '',
    discovery_result JSONB,
    deployment_plan JSONB,
    project_id UUID REFERENCES projects(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '24 hours')
);

CREATE INDEX IF NOT EXISTS idx_repository_imports_owner_id ON repository_imports(owner_id);
CREATE INDEX IF NOT EXISTS idx_repository_imports_status ON repository_imports(status);
