-- 000013_deployment_rollback_persistence.up.sql
-- Add execution_mode, image_digest, env_config_hash, and env_snapshot for project rollback persistence

ALTER TABLE deployments
    ADD COLUMN IF NOT EXISTS execution_mode VARCHAR(50) NOT NULL DEFAULT 'build',
    ADD COLUMN IF NOT EXISTS image_digest VARCHAR(255),
    ADD COLUMN IF NOT EXISTS env_config_hash VARCHAR(64),
    ADD COLUMN IF NOT EXISTS env_snapshot BYTEA;
