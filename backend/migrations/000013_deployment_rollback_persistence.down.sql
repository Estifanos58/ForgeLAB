-- 000013_deployment_rollback_persistence.down.sql
-- Remove execution_mode, image_digest, env_config_hash, and env_snapshot

ALTER TABLE deployments
    DROP COLUMN IF EXISTS execution_mode,
    DROP COLUMN IF EXISTS image_digest,
    DROP COLUMN IF EXISTS env_config_hash,
    DROP COLUMN IF EXISTS env_snapshot;
