-- Migration 000056: enforce tenant isolation at the database level and repair
-- the seeded system lifecycle so it is visible to every tenant.
--
-- Two production blockers are addressed here:
--
--   1. Row Level Security was only effective when the application connected as
--      a role that is neither a superuser, nor BYPASSRLS, nor the table owner.
--      The shipped compose/installer role is a superuser, so every policy was
--      silently bypassed and one tenant could read and modify another tenant's
--      rows. This migration creates a dedicated NOSUPERUSER NOBYPASSRLS
--      application role that the connection pool switches into, and marks every
--      RLS-enabled table FORCE ROW LEVEL SECURITY so the table owner is subject
--      to its own policies as well.
--
--   2. The seeded `physical_asset` lifecycle definition is global
--      (organization_id IS NULL) while its states and transitions were seeded
--      against a placeholder organization. Because lifecycle_state and
--      lifecycle_transition are RLS-scoped by organization_id, real tenants saw
--      a definition with zero states and every lifecycle transition failed.

-- ─── 1. Application role used for all tenant-scoped connections ──────────────

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'reticora_app') THEN
        CREATE ROLE reticora_app NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
    END IF;
END
$$;

-- The role must never be able to escape its policies even if it is later
-- granted additional attributes by an operator.
ALTER ROLE reticora_app NOSUPERUSER NOBYPASSRLS;

GRANT USAGE ON SCHEMA public TO reticora_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO reticora_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO reticora_app;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO reticora_app;

-- Tables created by later migrations inherit the same grants.
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO reticora_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO reticora_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT EXECUTE ON FUNCTIONS TO reticora_app;

-- The migrating role must be a member of reticora_app so the application can
-- SET ROLE into it on every pooled connection.
DO $$
BEGIN
    EXECUTE format('GRANT reticora_app TO %I', current_user);
EXCEPTION
    WHEN OTHERS THEN NULL; -- already a member, or current_user is reticora_app
END
$$;

-- ─── 2. FORCE row level security on every tenant-scoped table ────────────────
-- Without FORCE, the table owner (which the application role may be in
-- single-role deployments) bypasses its own policies.

DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public'
          AND c.relkind = 'r'
          AND c.relrowsecurity
          AND NOT c.relforcerowsecurity
    LOOP
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', rec.relname);
    END LOOP;
END
$$;

-- ─── 3. Repair the seeded system lifecycle ───────────────────────────────────
-- System lifecycle rows are global, exactly like their definition, so make the
-- organization reference nullable and expose NULL-owned rows to every tenant
-- read. WITH CHECK still requires a concrete organization_id, so a tenant can
-- never create or modify a global row.

ALTER TABLE lifecycle_state ALTER COLUMN organization_id DROP NOT NULL;
ALTER TABLE lifecycle_transition ALTER COLUMN organization_id DROP NOT NULL;

UPDATE lifecycle_state
SET organization_id = NULL
WHERE definition_id IN (SELECT id FROM lifecycle_definition WHERE is_system AND organization_id IS NULL);

UPDATE lifecycle_transition
SET organization_id = NULL
WHERE definition_id IN (SELECT id FROM lifecycle_definition WHERE is_system AND organization_id IS NULL);

DROP POLICY IF EXISTS lifecycle_state_isolation ON lifecycle_state;
CREATE POLICY lifecycle_state_isolation ON lifecycle_state
    USING (organization_id IS NULL OR organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DROP POLICY IF EXISTS lifecycle_transition_isolation ON lifecycle_transition;
CREATE POLICY lifecycle_transition_isolation ON lifecycle_transition
    USING (organization_id IS NULL OR organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
