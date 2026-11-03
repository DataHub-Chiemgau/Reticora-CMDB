-- Migration 000206: templates assigned to a CI (WP-710, MET-16, API-06,
-- AUD-07).
--
-- 1. ci_template holds the templates of a CI in a defined order (MET-16):
--    one row per CI and template with the published version the CI uses
--    (the effective schema of a CI follows from ci.schema_version of its
--    type and these versions; basis of the template migration, WP-253) and
--    its position, unique per CI. The rows go with the CI (foreign key with
--    the organization, K19, ON DELETE CASCADE). The template and its
--    version are referenced without an action: a template or version a CI
--    uses cannot be deleted (23503, 422 through api.WriteDBError).
--    Foreign keys are checked without row level security, so the
--    application checks the assignment rules with a query under RLS
--    (published version of an active template of the organization or a
--    system template, of the type of the CI or a type of its chain).
-- 2. client_id and site_id follow the CI (derive_ci_scope, 000061); a CI
--    that changes client or site re-derives its rows with the client and
--    site scope widened like propagate_ci_scope (000141, WP-345), which stays
--    unchanged.
-- 3. Policies as on ci_field_value (E-08, E-09): USING with the client and
--    site predicate (NULL stays readable), WITH CHECK without NULL for
--    scoped principals, restrictive UPDATE and DELETE policies; the TRUNCATE
--    guard of the tenant tables (WP-352).

-- ─── 1. Table ──────────────────────────────────────────────────────────────────

CREATE TABLE ci_template (
    organization_id UUID NOT NULL,
    client_id       UUID,
    site_id         UUID,
    ci_id           UUID NOT NULL,
    template_id     UUID NOT NULL,
    version         INTEGER NOT NULL,
    position        INTEGER NOT NULL CONSTRAINT ci_template_position_check CHECK (position >= 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ci_template_pkey PRIMARY KEY (ci_id, template_id),
    CONSTRAINT ci_template_ci_id_position_key UNIQUE (ci_id, position),
    CONSTRAINT ci_template_ci_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT ci_template_template_id_fkey FOREIGN KEY (template_id) REFERENCES template (id),
    CONSTRAINT ci_template_version_fkey FOREIGN KEY (template_id, version)
        REFERENCES template_version (template_id, version)
);

CREATE INDEX idx_ci_template_template_version ON ci_template (template_id, version);

-- ─── 2. Scope ──────────────────────────────────────────────────────────────────

CREATE TRIGGER ci_template_scope BEFORE INSERT OR UPDATE ON ci_template
    FOR EACH ROW EXECUTE FUNCTION derive_ci_scope('ci_id');

-- A no-op UPDATE re-runs the derivation of the rows of the CI.
CREATE FUNCTION propagate_ci_template_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE ci_template SET ci_id = ci_id WHERE ci_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER ci_template_scope_propagation AFTER UPDATE ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_ci_template_scope();

-- ─── 3. Policies ───────────────────────────────────────────────────────────────

ALTER TABLE ci_template ENABLE ROW LEVEL SECURITY;
ALTER TABLE ci_template FORCE ROW LEVEL SECURITY;

DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    client_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    client_ids CONSTANT text := 'string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_ids CONSTANT text := 'string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[]';
    writable text := format('%1$s AND (%2$s OR client_id = ANY (%3$s)) AND (%4$s OR site_id = ANY (%5$s))',
                            org_match, client_all, client_ids, site_all, site_ids);
BEGIN
    EXECUTE format(
        'CREATE POLICY ci_template_isolation ON ci_template '
        'USING (%1$s AND (%2$s OR client_id IS NULL OR client_id = ANY (%3$s)) '
            'AND (%4$s OR site_id IS NULL OR site_id = ANY (%5$s))) '
        'WITH CHECK (%6$s)',
        org_match, client_all, client_ids, site_all, site_ids, writable);
    EXECUTE format('CREATE POLICY ci_template_scoped_update ON ci_template AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                   writable, writable);
    EXECUTE format('CREATE POLICY ci_template_scoped_delete ON ci_template AS RESTRICTIVE FOR DELETE USING (%s)', writable);
END
$$;

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON ci_template
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
