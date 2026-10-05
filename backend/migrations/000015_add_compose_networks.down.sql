-- 000015_add_compose_networks.down.sql

ALTER TABLE service_deployments
    DROP COLUMN IF EXISTS networks;

ALTER TABLE services
    DROP COLUMN IF EXISTS networks;
