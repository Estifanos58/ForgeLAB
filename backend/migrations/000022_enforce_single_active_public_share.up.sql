-- Enforce strictly one active public share per project
CREATE UNIQUE INDEX IF NOT EXISTS uq_active_public_share_per_project ON public_shares(project_id) WHERE status = 'active';
