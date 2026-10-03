-- 000012_add_encrypted_session_token_to_sources.down.sql
-- Revert dedicated encrypted_session_token column

ALTER TABLE sources
    DROP COLUMN IF EXISTS encrypted_session_token;
