-- 000017_add_guest_identity.up.sql
-- Ensure single shared guest identity for registration-free local deployments

INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    'guest@forgelab.local',
    '$2a$12$disabled.guest.account.cannot.login.directly.hash',
    'Guest',
    NOW(),
    NOW()
)
ON CONFLICT (id) DO UPDATE SET updated_at = NOW();
