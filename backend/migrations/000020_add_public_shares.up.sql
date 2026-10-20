CREATE TABLE IF NOT EXISTS public_shares (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    share_token VARCHAR(64) NOT NULL UNIQUE,
    public_url VARCHAR(512) NOT NULL,
    public_host VARCHAR(255) NOT NULL,
    primary_service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    route_mappings JSONB NOT NULL DEFAULT '[]',
    status VARCHAR(32) NOT NULL DEFAULT 'active',
    revoked_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_public_shares_project_id ON public_shares(project_id);
CREATE INDEX IF NOT EXISTS idx_public_shares_owner_id ON public_shares(owner_id);
CREATE INDEX IF NOT EXISTS idx_public_shares_share_token ON public_shares(share_token);
CREATE INDEX IF NOT EXISTS idx_public_shares_status ON public_shares(status);
