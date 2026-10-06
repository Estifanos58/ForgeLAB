-- 000016_add_depends_on_conditions.down.sql

ALTER TABLE service_deployments
    DROP COLUMN IF EXISTS depends_on_conditions;

ALTER TABLE services
    DROP COLUMN IF EXISTS depends_on_conditions;
