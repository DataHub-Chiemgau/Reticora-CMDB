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

-- ─── Append-only audit log ──────────────────────────────────────────────────
-- The hash chain makes tampering *detectable*; these triggers make it
-- impossible for the application role in the first place. Row-level triggers
-- are not enforced against the table owner when explicitly disabled, so an
-- operator with owner rights can still run a supervised correction — but that
-- requires a deliberate, privileged act rather than an ordinary UPDATE.
CREATE OR REPLACE FUNCTION reject_audit_log_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_log rows are append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_audit_log_no_update ON audit_log;
CREATE TRIGGER trg_audit_log_no_update BEFORE UPDATE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION reject_audit_log_mutation();

DROP TRIGGER IF EXISTS trg_audit_log_no_delete ON audit_log;
CREATE TRIGGER trg_audit_log_no_delete BEFORE DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION reject_audit_log_mutation();

-- ─── Tenant-consistent references ───────────────────────────────────────────
-- Foreign key checks are executed with the privileges of the referenced table
-- and are not subject to row level security. A plain
-- "REFERENCES ci(id)" therefore still accepts an identifier belonging to
-- another organization, which would let a tenant attach a relationship or a
-- composition to a CI it can neither see nor own. Composite foreign keys that
-- include organization_id close that gap declaratively.
ALTER TABLE ci ADD CONSTRAINT ci_id_organization_key UNIQUE (id, organization_id);
ALTER TABLE asset ADD CONSTRAINT asset_id_organization_key UNIQUE (id, organization_id);

-- Rows whose references cross tenants (or dangle) would block the composite
-- foreign keys below. They are not deleted but moved, unchanged, into
-- migration_quarantine so an operator can review and repair them (E-26); the
-- down migration moves them back.
CREATE TABLE migration_quarantine (
    id              BIGSERIAL PRIMARY KEY,
    organization_id UUID NOT NULL,
    source_table    TEXT NOT NULL,
    row_data        JSONB NOT NULL,
    reason          TEXT NOT NULL,
    migration       TEXT NOT NULL,
    quarantined_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_migration_quarantine_org ON migration_quarantine (organization_id, source_table);
ALTER TABLE migration_quarantine ENABLE ROW LEVEL SECURITY;
ALTER TABLE migration_quarantine FORCE ROW LEVEL SECURITY;
CREATE POLICY migration_quarantine_isolation ON migration_quarantine
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

WITH moved AS (
    DELETE FROM ci_relationship r
    WHERE NOT EXISTS (SELECT 1 FROM ci c WHERE c.id = r.source_ci_id AND c.organization_id = r.organization_id)
       OR NOT EXISTS (SELECT 1 FROM ci c WHERE c.id = r.target_ci_id AND c.organization_id = r.organization_id)
    RETURNING r.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'ci_relationship', to_jsonb(moved),
       'source or target CI missing in the same organization', '000056'
FROM moved;

ALTER TABLE ci_relationship
    ADD CONSTRAINT ci_relationship_source_tenant_fkey
    FOREIGN KEY (source_ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE;
ALTER TABLE ci_relationship
    ADD CONSTRAINT ci_relationship_target_tenant_fkey
    FOREIGN KEY (target_ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE;

WITH moved AS (
    DELETE FROM composition k
    WHERE NOT EXISTS (SELECT 1 FROM asset a WHERE a.id = k.parent_asset_id AND a.organization_id = k.organization_id)
       OR (k.child_ci_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM ci c WHERE c.id = k.child_ci_id AND c.organization_id = k.organization_id))
       OR (k.child_asset_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM asset a WHERE a.id = k.child_asset_id AND a.organization_id = k.organization_id))
    RETURNING k.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'composition', to_jsonb(moved),
       'parent or child missing in the same organization', '000056'
FROM moved;

ALTER TABLE composition
    ADD CONSTRAINT composition_parent_tenant_fkey
    FOREIGN KEY (parent_asset_id, organization_id) REFERENCES asset (id, organization_id) ON DELETE CASCADE;
ALTER TABLE composition
    ADD CONSTRAINT composition_child_ci_tenant_fkey
    FOREIGN KEY (child_ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE;
ALTER TABLE composition
    ADD CONSTRAINT composition_child_asset_tenant_fkey
    FOREIGN KEY (child_asset_id, organization_id) REFERENCES asset (id, organization_id) ON DELETE CASCADE;

-- ─── Stable CI type keys ────────────────────────────────────────────────────
-- The `key` column is the stable identifier used by templates, imports and the
-- API, but only `name` was unique per organization, so two types could share a
-- key and key-based lookups became non-deterministic. Existing duplicates are
-- disambiguated with a numeric suffix before the constraint is added.
WITH ranked AS (
    SELECT id, key, row_number() OVER (PARTITION BY organization_id, key ORDER BY created_at, id) AS rn
    FROM ci_type
)
UPDATE ci_type t
SET key = ranked.key || '-' || ranked.rn
FROM ranked
WHERE t.id = ranked.id AND ranked.rn > 1;

CREATE UNIQUE INDEX ci_type_org_key_uniq ON ci_type (organization_id, key)
    WHERE organization_id IS NOT NULL;
CREATE UNIQUE INDEX ci_type_system_key_uniq ON ci_type (key)
    WHERE organization_id IS NULL;
