-- 000003_add_auth_identities.up.sql
-- Add auth_identities table for OAuth provider management (Google, GitHub)
-- Make users.password_hash nullable to support OAuth-only accounts

ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;

CREATE TABLE auth_identities (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider VARCHAR(50) NOT NULL,
    provider_subject VARCHAR(255) NOT NULL,
    provider_email VARCHAR(255) NOT NULL DEFAULT '',
    email_verified BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_auth_identities_provider_subject UNIQUE(provider, provider_subject)
);

CREATE INDEX idx_auth_identities_user_id ON auth_identities(user_id);
CREATE INDEX idx_auth_identities_lookup ON auth_identities(provider, provider_subject);
CREATE INDEX idx_auth_identities_provider_email ON auth_identities(provider, provider_email);
