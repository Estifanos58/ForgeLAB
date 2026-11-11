ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS auto_deploy BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS auto_deploy_branch VARCHAR(255),
    ADD COLUMN IF NOT EXISTS encrypted_webhook_secret BYTEA;

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    delivery_id VARCHAR(128) PRIMARY KEY,
    project_id UUID REFERENCES projects(id) ON DELETE CASCADE,
    event_type VARCHAR(64) NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_project_id ON webhook_deliveries(project_id);
