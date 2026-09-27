-- 000005_add_source_workspaces.up.sql
-- Introduce persistent source ownership to enforce authorization on uploaded workspaces

CREATE TABLE IF NOT EXISTS source_workspaces (
    id UUID PRIMARY KEY,
    owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_path TEXT NOT NULL,
    files_count INTEGER NOT NULL DEFAULT 0,
    total_bytes BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_source_workspaces_owner_id ON source_workspaces(owner_id);
