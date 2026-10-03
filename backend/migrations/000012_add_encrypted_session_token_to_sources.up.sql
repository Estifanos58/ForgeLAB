-- 000012_add_encrypted_session_token_to_sources.up.sql
-- Add dedicated encrypted_session_token column for secure local agent credential storage

ALTER TABLE sources
    ADD COLUMN IF NOT EXISTS encrypted_session_token BYTEA;
