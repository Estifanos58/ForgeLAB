-- 000015_add_compose_networks.up.sql

ALTER TABLE services
    ADD COLUMN IF NOT EXISTS networks JSONB DEFAULT '[]'::jsonb;

ALTER TABLE service_deployments
    ADD COLUMN IF NOT EXISTS networks JSONB DEFAULT '[]'::jsonb;
