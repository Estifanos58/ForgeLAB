-- 000017_add_guest_identity.down.sql
-- Safely rollback the shared guest identity.
-- Refuse to delete the guest user if guest-owned projects or sources exist,
-- preventing orphaned Docker containers and resource leaks outside ForgeLAB's lifecycle management.

DO $$
DECLARE
    project_count INTEGER;
    source_count INTEGER;
BEGIN
    SELECT COUNT(*) INTO project_count 
    FROM projects 
    WHERE owner_id = '00000000-0000-0000-0000-000000000001';

    SELECT COUNT(*) INTO source_count 
    FROM sources 
    WHERE owner_id = '00000000-0000-0000-0000-000000000001';

    IF project_count > 0 OR source_count > 0 THEN
        RAISE EXCEPTION 'Cannot rollback migration 000017_add_guest_identity: % guest project(s) and % guest source(s) exist. Operators must delete all guest-owned projects and clean up runtime resources via ForgeLAB before rolling back the guest identity.', project_count, source_count;
    END IF;

    DELETE FROM users WHERE id = '00000000-0000-0000-0000-000000000001';
END $$;
