CREATE TABLE IF NOT EXISTS custom_domains (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    service_id UUID REFERENCES services(id) ON DELETE SET NULL,
    domain VARCHAR(255) NOT NULL UNIQUE,
    verification_token VARCHAR(128) NOT NULL,
    verified BOOLEAN NOT NULL DEFAULT FALSE,
    tls_status VARCHAR(32) NOT NULL DEFAULT 'pending',
    target_port INTEGER NOT NULL DEFAULT 8080,
    last_checked_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_custom_domains_project_id ON custom_domains(project_id);
CREATE INDEX IF NOT EXISTS idx_custom_domains_verified ON custom_domains(verified) WHERE verified = TRUE;
