CREATE TABLE IF NOT EXISTS service_metrics_samples (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    service_name VARCHAR(255) NOT NULL,
    container_id VARCHAR(128) NOT NULL DEFAULT '',
    running BOOLEAN NOT NULL DEFAULT FALSE,
    cpu_percent DOUBLE PRECISION NOT NULL DEFAULT 0,
    memory_used_bytes BIGINT NOT NULL DEFAULT 0,
    memory_limit_bytes BIGINT NOT NULL DEFAULT 0,
    memory_percent DOUBLE PRECISION NOT NULL DEFAULT 0,
    network_rx_bytes BIGINT NOT NULL DEFAULT 0,
    network_tx_bytes BIGINT NOT NULL DEFAULT 0,
    pids_current BIGINT NOT NULL DEFAULT 0,
    restart_count INTEGER NOT NULL DEFAULT 0,
    oom_killed BOOLEAN NOT NULL DEFAULT FALSE,
    health_status VARCHAR(32) NOT NULL DEFAULT 'none',
    sampled_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_service_metrics_samples_project_sampled
    ON service_metrics_samples(project_id, sampled_at DESC);
CREATE INDEX IF NOT EXISTS idx_service_metrics_samples_service_sampled
    ON service_metrics_samples(service_id, sampled_at DESC);
CREATE INDEX IF NOT EXISTS idx_service_metrics_samples_sampled_at
    ON service_metrics_samples(sampled_at);
