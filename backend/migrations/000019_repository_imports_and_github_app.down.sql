DROP TABLE IF EXISTS repository_imports;
DROP TABLE IF EXISTS github_app_installations;
ALTER TABLE github_integrations DROP COLUMN IF EXISTS auth_type;
