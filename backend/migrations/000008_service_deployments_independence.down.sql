-- 000008_service_deployments_independence.down.sql
DROP INDEX IF EXISTS idx_deployment_logs_service_deployment;
ALTER TABLE deployment_logs DROP COLUMN IF EXISTS service_deployment_id;
ALTER TABLE deployment_logs ALTER COLUMN deployment_id SET NOT NULL;

DROP INDEX IF EXISTS uq_active_service_deployment;
DROP INDEX IF EXISTS uq_service_deployments_service_number;

ALTER TABLE service_deployments
    DROP COLUMN IF EXISTS deploy_number,
    DROP COLUMN IF EXISTS dockerfile_path,
    DROP COLUMN IF EXISTS build_context,
    DROP COLUMN IF EXISTS health_strategy,
    DROP COLUMN IF EXISTS health_check_path;

-- Re-add project active deployment lock
CREATE UNIQUE INDEX IF NOT EXISTS uq_active_deployment_per_project 
ON deployments(project_id) 
WHERE status IN ('queued', 'cloning', 'building', 'starting', 'health_checking');
