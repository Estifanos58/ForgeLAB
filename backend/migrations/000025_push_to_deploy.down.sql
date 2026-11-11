DROP INDEX IF EXISTS idx_webhook_deliveries_project_id;
DROP TABLE IF EXISTS webhook_deliveries;

ALTER TABLE projects
    DROP COLUMN IF EXISTS encrypted_webhook_secret,
    DROP COLUMN IF EXISTS auto_deploy_branch,
    DROP COLUMN IF EXISTS auto_deploy;
