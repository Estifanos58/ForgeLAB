-- 000014_deployment_plan_and_compose.down.sql

ALTER TABLE deployments
    DROP COLUMN IF EXISTS deployment_strategy,
    DROP COLUMN IF EXISTS deployment_plan;

ALTER TABLE service_deployments
    DROP COLUMN IF EXISTS classification,
    DROP COLUMN IF EXISTS image,
    DROP COLUMN IF EXISTS depends_on,
    DROP COLUMN IF EXISTS volumes,
    DROP COLUMN IF EXISTS healthcheck_config;

ALTER TABLE services
    DROP COLUMN IF EXISTS classification,
    DROP COLUMN IF EXISTS image,
    DROP COLUMN IF EXISTS depends_on,
    DROP COLUMN IF EXISTS volumes,
    DROP COLUMN IF EXISTS healthcheck_config;

ALTER TABLE projects
    DROP COLUMN IF EXISTS deployment_strategy,
    DROP COLUMN IF EXISTS deployment_plan;
