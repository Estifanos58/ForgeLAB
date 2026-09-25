-- 000002_add_active_deployment_unique_index.down.sql
DROP INDEX IF EXISTS uq_active_deployment_per_project;
