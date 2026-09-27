-- 000004_source_and_build_abstractions.down.sql

DROP TABLE IF EXISTS github_integrations;

ALTER TABLE deployments
    DROP COLUMN IF EXISTS source_revision,
    DROP COLUMN IF EXISTS build_strategy,
    DROP COLUMN IF EXISTS build_command,
    DROP COLUMN IF EXISTS start_command,
    DROP COLUMN IF EXISTS runtime_type,
    DROP COLUMN IF EXISTS internal_port,
    DROP COLUMN IF EXISTS health_strategy;

ALTER TABLE projects
    DROP COLUMN IF EXISTS source_reference,
    DROP COLUMN IF EXISTS build_strategy,
    DROP COLUMN IF EXISTS build_command,
    DROP COLUMN IF EXISTS start_command,
    DROP COLUMN IF EXISTS runtime_type,
    DROP COLUMN IF EXISTS internal_port,
    DROP COLUMN IF EXISTS health_strategy;
