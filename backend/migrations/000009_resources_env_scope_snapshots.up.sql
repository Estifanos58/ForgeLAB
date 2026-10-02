-- 000009_resources_env_scope_snapshots.up.sql
-- Per-service resource configuration, env variable scoping, deployment snapshot fields

-- 1. Add configurable resource limits to services (the "live" config the user edits)
ALTER TABLE services
    ADD COLUMN IF NOT EXISTS cpu_millicores INTEGER NOT NULL DEFAULT 1000,
    ADD COLUMN IF NOT EXISTS memory_mb INTEGER NOT NULL DEFAULT 1024,
    ADD COLUMN IF NOT EXISTS pids_limit INTEGER NOT NULL DEFAULT 256,
    ADD COLUMN IF NOT EXISTS ephemeral_storage_mb INTEGER;

-- 2. Snapshot resource limits into each service deployment (immutable record of what was deployed)
ALTER TABLE service_deployments
    ADD COLUMN IF NOT EXISTS cpu_millicores INTEGER NOT NULL DEFAULT 1000,
    ADD COLUMN IF NOT EXISTS memory_mb INTEGER NOT NULL DEFAULT 1024,
    ADD COLUMN IF NOT EXISTS pids_limit INTEGER NOT NULL DEFAULT 256,
    ADD COLUMN IF NOT EXISTS ephemeral_storage_mb INTEGER,
    ADD COLUMN IF NOT EXISTS source_revision VARCHAR(255),
    ADD COLUMN IF NOT EXISTS env_config_hash VARCHAR(64),
    ADD COLUMN IF NOT EXISTS image_digest VARCHAR(255);

-- 3. Add env variable scoping: scope = 'runtime' | 'build' | 'both', and service-specific overrides
ALTER TABLE environment_variables
    ADD COLUMN IF NOT EXISTS service_id UUID REFERENCES services(id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS scope VARCHAR(20) NOT NULL DEFAULT 'runtime';

ALTER TABLE environment_variables DROP CONSTRAINT IF EXISTS uq_env_vars_project_key;

CREATE UNIQUE INDEX IF NOT EXISTS uq_env_vars_project_key_global
    ON environment_variables(project_id, key)
    WHERE service_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_env_vars_project_service_key
    ON environment_variables(project_id, service_id, key)
    WHERE service_id IS NOT NULL;

-- 4. Backfill existing service resource config from current hard-coded defaults
-- (no-op since defaults match the hard-coded values)

-- 5. Backfill existing service deployment resource config from parent service
UPDATE service_deployments sd
SET cpu_millicores = s.cpu_millicores,
    memory_mb = s.memory_mb,
    pids_limit = s.pids_limit,
    ephemeral_storage_mb = s.ephemeral_storage_mb
FROM services s
WHERE sd.service_id = s.id
  AND sd.cpu_millicores = 1000
  AND sd.memory_mb = 1024;
