DROP INDEX IF EXISTS idx_service_deployments_active_lease;
ALTER TABLE service_deployments
    DROP COLUMN IF EXISTS lease_acquired_at,
    DROP COLUMN IF EXISTS lease_worker_id;
