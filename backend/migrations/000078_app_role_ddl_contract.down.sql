-- Migration 000078 down: the migrating role owns the tables again and the
-- owner role is removed. ENABLE/FORCE ROW LEVEL SECURITY stays, as in the down
-- migration of 000056: reverting it would weaken tenant isolation (all tables
-- concerned already had both before this migration).

DROP EVENT TRIGGER IF EXISTS reticora_restrict_app_ddl;
DROP FUNCTION IF EXISTS reticora_restrict_app_ddl();
DROP EVENT TRIGGER IF EXISTS reticora_assign_owner;
DROP FUNCTION IF EXISTS reticora_assign_owner();

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'reticora_owner') THEN
        EXECUTE format('REASSIGN OWNED BY reticora_owner TO %I', current_user);
        DROP OWNED BY reticora_owner;
        BEGIN
            DROP ROLE reticora_owner;
        EXCEPTION
            WHEN dependent_objects_still_exist THEN NULL; -- still used by another database
        END;
    END IF;
END
$$;
