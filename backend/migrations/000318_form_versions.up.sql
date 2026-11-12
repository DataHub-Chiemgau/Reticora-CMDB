-- Migration 000318: immutable versions of form definitions; a submission is
-- bound to the version it was captured against (WFL-01, WP-1100, E-228
-- option 1).
--
-- 1. form_def gets UNIQUE (id, organization_id) for composite foreign keys
--    (Konvention 19), only when the key is missing.
-- 2. form_def_version(organization, form, client, version >= 1, schema,
--    ui_hints, created_by NULL, created_at), UNIQUE (form_id, version). The
--    client is that of the form definition (the parent row), set by the
--    trigger form_def_version_scope and carried along when the form changes
--    its client (propagate_form_def_version_scope). form_def has no site, so
--    a version has none either: a site_id column could only stay NULL and
--    Konvention 5 would then refuse every write of a principal restricted
--    to sites, who writes form_def itself (policy 000058 checks no site).
--    A version never changes its content: UPDATE is refused except for the
--    client and created_by columns changed by a trigger or foreign-key
--    action (depth > 1), DELETE except as the cascade of its form
--    definition or organization.
--    ENABLE and FORCE RLS, policies after Konvention 5 over organization
--    and client (pattern 000058, no EXISTS), truncate guard.
-- 3. form_def.current_version_id (DEFERRABLE INITIALLY DEFERRED: the
--    definition and its first version are created in one transaction) and
--    form_def.version, the counter of every change (If-Match, API-07).
-- 4. Backfill: version 1 of every form from schema and ui_hints; every
--    submission is bound to it, then form_submission.form_version_id is
--    NOT NULL with a composite foreign key. Both the current version of a
--    form and the version of a submission must belong to the same form
--    (foreign keys over id, form_id and organization_id).

-- ─── 1. form_def key ──────────────────────────────────────────────────────────

-- form-def-key:begin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'form_def'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE form_def ADD CONSTRAINT form_def_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
-- form-def-key:end

-- ─── 2. form_def_version ──────────────────────────────────────────────────────

CREATE TABLE form_def_version (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    form_id         uuid NOT NULL,
    client_id       uuid NULL,
    version         integer NOT NULL CONSTRAINT form_def_version_version_check CHECK (version >= 1),
    schema          jsonb NOT NULL,
    ui_hints        jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by      uuid NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT form_def_version_form_version_key UNIQUE (form_id, version),
    CONSTRAINT form_def_version_id_organization_key UNIQUE (id, organization_id),
    -- Target of the composite keys that bind a form's current version and a
    -- submission to a version of the same form.
    CONSTRAINT form_def_version_id_form_organization_key UNIQUE (id, form_id, organization_id),
    CONSTRAINT form_def_version_form_fkey FOREIGN KEY (form_id, organization_id)
        REFERENCES form_def (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT form_def_version_created_by_fkey FOREIGN KEY (created_by, organization_id)
        REFERENCES app_user (id, organization_id) ON DELETE SET NULL (created_by),
    CONSTRAINT form_def_version_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id) ON DELETE SET NULL (client_id)
);
CREATE INDEX idx_form_def_version_org_form ON form_def_version (organization_id, form_id, version DESC);
CREATE INDEX idx_form_def_version_client ON form_def_version (organization_id, client_id) WHERE client_id IS NOT NULL;
CREATE INDEX idx_form_def_version_created_by ON form_def_version (organization_id, created_by) WHERE created_by IS NOT NULL;

-- The client of a version is that of its form definition (the parent row,
-- read with widened scope and FOR SHARE as in 000308).
CREATE FUNCTION form_def_version_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
BEGIN
    previous := app_scope_widen();
    SELECT f.client_id INTO NEW.client_id
      FROM form_def f WHERE f.id = NEW.form_id AND f.organization_id = NEW.organization_id FOR SHARE;
    PERFORM app_scope_restore(previous);
    RETURN NEW;
END
$$;
CREATE TRIGGER form_def_version_scope BEFORE INSERT OR UPDATE ON form_def_version
    FOR EACH ROW EXECUTE FUNCTION form_def_version_scope();

-- A stored version never changes its content. Only a trigger or foreign-key
-- action (depth > 1) may touch the derived client and created_by; only the
-- cascade of its form definition or organization deletes it.
CREATE FUNCTION form_def_version_immutable() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF pg_trigger_depth() > 1 THEN
            RETURN OLD;
        END IF;
    ELSIF pg_trigger_depth() > 1
        AND NEW.id = OLD.id AND NEW.organization_id = OLD.organization_id AND NEW.form_id = OLD.form_id
        AND NEW.version = OLD.version AND NEW.schema = OLD.schema AND NEW.ui_hints = OLD.ui_hints
        AND NEW.created_at = OLD.created_at THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'a form definition version cannot be changed'
        USING ERRCODE = 'check_violation', CONSTRAINT = 'form_def_version_immutable';
END
$$;
CREATE TRIGGER form_def_version_immutable BEFORE UPDATE OR DELETE ON form_def_version
    FOR EACH ROW EXECUTE FUNCTION form_def_version_immutable();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON form_def_version
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- A form that moves to another client takes its versions along.
CREATE FUNCTION propagate_form_def_version_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE form_def_version SET client_id = NEW.client_id WHERE form_id = NEW.id AND organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_form_def_version_scope
    AFTER UPDATE ON form_def
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION propagate_form_def_version_scope();

ALTER TABLE form_def_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE form_def_version FORCE ROW LEVEL SECURITY;

-- Konvention 5 (E-09): a restricted principal reads the versions of its
-- clients and those without a client; it writes only rows of its clients,
-- rows without a client only without client restriction (as form_def).
DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    t CONSTANT text := 'form_def_version';
BEGIN
    EXECUTE format('CREATE POLICY %I ON %I USING (%s AND (%s OR client_id IS NULL OR %s)) WITH CHECK (%s AND (%s OR %s))',
        t || '_isolation', t, org_match, scope_all, scope_match, org_match, scope_all, scope_match);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR UPDATE USING (%s AND (%s OR %s)) WITH CHECK (%s AND (%s OR %s))',
        t || '_scoped_update', t, org_match, scope_all, scope_match, org_match, scope_all, scope_match);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR DELETE USING (%s AND (%s OR %s))',
        t || '_scoped_delete', t, org_match, scope_all, scope_match);
END
$$;

-- ─── 3. form_def.current_version_id and version ───────────────────────────────

ALTER TABLE form_def
    ADD COLUMN current_version_id uuid NULL,
    ADD COLUMN version bigint NOT NULL DEFAULT 1 CONSTRAINT form_def_version_counter_check CHECK (version >= 1);

-- ─── 4. backfill ──────────────────────────────────────────────────────────────

INSERT INTO form_def_version (organization_id, form_id, version, schema, ui_hints, created_by, created_at)
SELECT d.organization_id, d.id, 1, d.schema, d.ui_hints, NULL, d.created_at
  FROM form_def d;
-- The update of form_def fires no scope propagation (client unchanged).
UPDATE form_def d SET current_version_id = v.id
  FROM form_def_version v
 WHERE v.form_id = d.id AND v.organization_id = d.organization_id AND v.version = 1;

ALTER TABLE form_def
    ADD CONSTRAINT form_def_current_version_fkey FOREIGN KEY (current_version_id, id, organization_id)
        REFERENCES form_def_version (id, form_id, organization_id) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE form_submission ADD COLUMN form_version_id uuid NULL;
UPDATE form_submission s SET form_version_id = d.current_version_id
  FROM form_def d
 WHERE d.id = s.form_id AND d.organization_id = s.organization_id;
ALTER TABLE form_submission
    ALTER COLUMN form_version_id SET NOT NULL,
    ADD CONSTRAINT form_submission_form_version_fkey FOREIGN KEY (form_version_id, form_id, organization_id)
        REFERENCES form_def_version (id, form_id, organization_id) ON DELETE CASCADE;
CREATE INDEX idx_form_submission_form_version ON form_submission (organization_id, form_version_id);
