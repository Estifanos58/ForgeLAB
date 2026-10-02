-- 000010_service_rollback_reproducibility.down.sql
-- Remove execution_mode and env_snapshot columns from service_deployments

ALTER TABLE service_deployments
    DROP COLUMN IF EXISTS execution_mode,
    DROP COLUMN IF EXISTS env_snapshot;
