-- 000006_extend_source_workspaces_lifecycle.down.sql
-- Revert source_workspaces lifecycle extensions

DROP INDEX IF EXISTS idx_source_workspaces_status;

ALTER TABLE source_workspaces
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS error,
    DROP COLUMN IF EXISTS detection_result,
    DROP COLUMN IF EXISTS framework,
    DROP COLUMN IF EXISTS runtime,
    DROP COLUMN IF EXISTS processed_bytes,
    DROP COLUMN IF EXISTS processed_files,
    DROP COLUMN IF EXISTS phase,
    DROP COLUMN IF EXISTS status;
