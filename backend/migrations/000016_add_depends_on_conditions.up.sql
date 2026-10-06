-- 000016_add_depends_on_conditions.up.sql
-- Add depends_on_conditions column to preserve Compose and DAG dependency conditions

ALTER TABLE services
    ADD COLUMN IF NOT EXISTS depends_on_conditions JSONB DEFAULT '{}'::jsonb;

ALTER TABLE service_deployments
    ADD COLUMN IF NOT EXISTS depends_on_conditions JSONB DEFAULT '{}'::jsonb;
