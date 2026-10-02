-- 000009_resources_env_scope_snapshots.down.sql

DROP INDEX IF EXISTS uq_env_vars_project_service_key;
DROP INDEX IF EXISTS uq_env_vars_project_key_global;
ALTER TABLE environment_variables
    ADD CONSTRAINT uq_env_vars_project_key UNIQUE(project_id, key);
ALTER TABLE environment_variables
    DROP COLUMN IF EXISTS service_id,
    DROP COLUMN IF EXISTS scope;

ALTER TABLE service_deployments
    DROP COLUMN IF EXISTS image_digest,
    DROP COLUMN IF EXISTS env_config_hash,
    DROP COLUMN IF EXISTS source_revision,
    DROP COLUMN IF EXISTS ephemeral_storage_mb,
    DROP COLUMN IF EXISTS pids_limit,
    DROP COLUMN IF EXISTS memory_mb,
    DROP COLUMN IF EXISTS cpu_millicores;

ALTER TABLE services
    DROP COLUMN IF EXISTS ephemeral_storage_mb,
    DROP COLUMN IF EXISTS pids_limit,
    DROP COLUMN IF EXISTS memory_mb,
    DROP COLUMN IF EXISTS cpu_millicores;
