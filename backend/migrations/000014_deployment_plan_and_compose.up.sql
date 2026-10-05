-- 000014_deployment_plan_and_compose.up.sql
-- Introduce deployment plan abstraction and compose foundation metadata

ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS deployment_strategy VARCHAR(50) NOT NULL DEFAULT 'auto',
    ADD COLUMN IF NOT EXISTS deployment_plan JSONB DEFAULT '{}'::jsonb;

ALTER TABLE services
    ADD COLUMN IF NOT EXISTS classification VARCHAR(50) NOT NULL DEFAULT 'application',
    ADD COLUMN IF NOT EXISTS image TEXT DEFAULT '',
    ADD COLUMN IF NOT EXISTS depends_on JSONB DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS volumes JSONB DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS networks JSONB DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS healthcheck_config JSONB DEFAULT '{}'::jsonb;

ALTER TABLE service_deployments
    ADD COLUMN IF NOT EXISTS classification VARCHAR(50) NOT NULL DEFAULT 'application',
    ADD COLUMN IF NOT EXISTS image TEXT DEFAULT '',
    ADD COLUMN IF NOT EXISTS depends_on JSONB DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS volumes JSONB DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS networks JSONB DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS healthcheck_config JSONB DEFAULT '{}'::jsonb;

ALTER TABLE deployments
    ADD COLUMN IF NOT EXISTS deployment_strategy VARCHAR(50) DEFAULT 'auto',
    ADD COLUMN IF NOT EXISTS deployment_plan JSONB DEFAULT '{}'::jsonb;
