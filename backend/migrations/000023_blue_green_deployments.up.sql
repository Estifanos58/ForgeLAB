-- Add deployment_mode ('standard' or 'blue_green') to projects and services
ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS deployment_mode VARCHAR(20) NOT NULL DEFAULT 'standard';

ALTER TABLE services
    ADD COLUMN IF NOT EXISTS deployment_mode VARCHAR(20) NOT NULL DEFAULT 'standard';
