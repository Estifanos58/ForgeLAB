-- 000001_initial_schema.down.sql
-- Rollback initial schema

ALTER TABLE projects DROP CONSTRAINT IF EXISTS fk_projects_current_deployment;

DROP TABLE IF EXISTS deployment_logs;
DROP TABLE IF EXISTS environment_variables;
DROP TABLE IF EXISTS deployments;
DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS users;

DROP EXTENSION IF EXISTS "pgcrypto";
