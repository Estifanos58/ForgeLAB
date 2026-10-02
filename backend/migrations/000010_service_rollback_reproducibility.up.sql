-- 000010_service_rollback_reproducibility.up.sql
-- Add execution_mode and encrypted env_snapshot for service rollback reproducibility

ALTER TABLE service_deployments
    ADD COLUMN IF NOT EXISTS execution_mode VARCHAR(50) NOT NULL DEFAULT 'build',
    ADD COLUMN IF NOT EXISTS env_snapshot BYTEA;
