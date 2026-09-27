-- 000006_extend_source_workspaces_lifecycle.up.sql
-- Extend source_workspaces to track asynchronous upload, normalization, and detection lifecycle

ALTER TABLE source_workspaces
    ADD COLUMN IF NOT EXISTS status VARCHAR(50) NOT NULL DEFAULT 'ready',
    ADD COLUMN IF NOT EXISTS phase VARCHAR(50) NOT NULL DEFAULT 'ready',
    ADD COLUMN IF NOT EXISTS processed_files INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS processed_bytes BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS runtime VARCHAR(100),
    ADD COLUMN IF NOT EXISTS framework VARCHAR(100),
    ADD COLUMN IF NOT EXISTS detection_result JSONB,
    ADD COLUMN IF NOT EXISTS error TEXT,
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE INDEX IF NOT EXISTS idx_source_workspaces_status ON source_workspaces(status);
