-- 000002_add_active_deployment_unique_index.up.sql
-- Enforce at most one active deployment per project at the database level

CREATE UNIQUE INDEX uq_active_deployment_per_project 
ON deployments(project_id) 
WHERE status IN ('queued', 'cloning', 'building', 'starting', 'health_checking');
