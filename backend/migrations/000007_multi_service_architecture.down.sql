-- 000007_multi_service_architecture.down.sql

ALTER TABLE deployment_logs DROP COLUMN IF EXISTS service_id;
DROP TABLE IF EXISTS service_deployments CASCADE;
DROP TABLE IF EXISTS services CASCADE;
ALTER TABLE projects DROP COLUMN IF EXISTS source_id;
DROP TABLE IF EXISTS sources CASCADE;
