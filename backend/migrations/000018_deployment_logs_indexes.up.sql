-- Add indexes to deployment_logs for efficient service log querying, cursor pagination, and phase filtering
CREATE INDEX IF NOT EXISTS idx_deployment_logs_service_id_id ON deployment_logs(service_id, id);
CREATE INDEX IF NOT EXISTS idx_deployment_logs_deployment_id_id ON deployment_logs(deployment_id, id);
CREATE INDEX IF NOT EXISTS idx_deployment_logs_phase ON deployment_logs(phase);
