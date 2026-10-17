-- 000017_add_guest_identity.down.sql
-- Remove single shared guest identity

DELETE FROM users WHERE id = '00000000-0000-0000-0000-000000000001';
