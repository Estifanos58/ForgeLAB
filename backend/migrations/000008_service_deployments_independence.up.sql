-- 000008_service_deployments_independence.up.sql
-- Decouple service deployments from project-wide deployment locks and make ServiceDeployment the primary deploy unit

-- 1. Remove project-wide active deployment constraint from deployments table
DROP INDEX IF EXISTS uq_active_deployment_per_project;

-- 2. Allow service_deployments to exist independently of a project release (deployment_id nullable)
ALTER TABLE service_deployments ALTER COLUMN deployment_id DROP NOT NULL;
ALTER TABLE service_deployments DROP CONSTRAINT IF EXISTS uq_service_deployments;

-- 3. Add service-specific deployment numbering and immutable build configuration to service_deployments
ALTER TABLE service_deployments
    ADD COLUMN IF NOT EXISTS deploy_number INTEGER DEFAULT 1 NOT NULL,
    ADD COLUMN IF NOT EXISTS dockerfile_path VARCHAR(500) DEFAULT 'Dockerfile',
    ADD COLUMN IF NOT EXISTS build_context VARCHAR(500) DEFAULT '.',
    ADD COLUMN IF NOT EXISTS health_strategy VARCHAR(50) DEFAULT 'auto',
    ADD COLUMN IF NOT EXISTS health_check_path VARCHAR(500) DEFAULT '/health';

-- 4. Backfill deploy_number sequentially per service
WITH numbered AS (
    SELECT id, ROW_NUMBER() OVER (PARTITION BY service_id ORDER BY created_at ASC) as rnum
    FROM service_deployments
)
UPDATE service_deployments sd
SET deploy_number = numbered.rnum
FROM numbered
WHERE sd.id = numbered.id;

-- 5. Backfill immutable configuration from the parent services table
UPDATE service_deployments sd
SET dockerfile_path = COALESCE(s.dockerfile_path, 'Dockerfile'),
    build_context = COALESCE(s.build_context, '.'),
    health_strategy = COALESCE(s.health_strategy, 'auto'),
    health_check_path = COALESCE(s.health_check_path, '/health')
FROM services s
WHERE sd.service_id = s.id;

-- 6. Enforce unique service deployment numbering per service
CREATE UNIQUE INDEX IF NOT EXISTS uq_service_deployments_service_number
ON service_deployments(service_id, deploy_number);

-- 7. Enforce active deployment lock per service_id (not per project)
-- Exactly one active deployment per service allowed at any given time
CREATE UNIQUE INDEX IF NOT EXISTS uq_active_service_deployment
ON service_deployments(service_id)
WHERE status IN ('queued', 'cloning', 'building', 'starting', 'health_checking');

-- 8. Enable service-scoped deployment logging by making deployment_id nullable and adding service_deployment_id
ALTER TABLE deployment_logs ALTER COLUMN deployment_id DROP NOT NULL;

ALTER TABLE deployment_logs
    ADD COLUMN IF NOT EXISTS service_deployment_id UUID REFERENCES service_deployments(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_deployment_logs_service_deployment ON deployment_logs(service_deployment_id);

-- 9. Backfill service_deployment_id for existing deployment logs where deployment_id and service_id match
UPDATE deployment_logs dl
SET service_deployment_id = sd.id
FROM service_deployments sd
WHERE dl.deployment_id = sd.deployment_id
  AND dl.service_id = sd.service_id
  AND dl.service_deployment_id IS NULL;
