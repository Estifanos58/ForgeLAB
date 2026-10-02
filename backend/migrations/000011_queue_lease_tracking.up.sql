-- Queue lease tracking: adds lease metadata to service_deployments for observability and orphan detection.
ALTER TABLE service_deployments
    ADD COLUMN IF NOT EXISTS lease_acquired_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS lease_worker_id   TEXT;

-- Index for reconciliation queries: find queued/active deployments that are stale.
CREATE INDEX IF NOT EXISTS idx_service_deployments_active_lease
    ON service_deployments (status, created_at)
    WHERE status IN ('queued', 'cloning', 'building', 'starting', 'health_checking');
