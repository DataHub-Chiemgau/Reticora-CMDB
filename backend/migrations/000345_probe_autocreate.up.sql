-- Migration 000345 (provisional number): automatic ping probes for
-- observable CIs with a management IP (MON-02, PRB-01, WP-949, E-182).
--
-- 1. probe.origin: manual (created through the API) or auto (created by the
--    job probe.autocreate). Only auto probes follow a change of the
--    management IP.
-- 2. probe_autocreate_suppression(ci, probe_type): a deleted auto probe
--    leaves an entry, and the job creates nothing more of that type for the
--    CI. client_id and site_id are those of the CI: the trigger
--    probe_autocreate_suppression_scope derives them like probe_ci_scope,
--    and a moved CI takes its entries along (propagate_monitoring_scope,
--    version of 000307 plus this table); actor_id is the deleting user
--    (Konvention 16, without foreign key).
--    ENABLE and FORCE RLS with policies after Konvention 5 (located-scope:
--    a restricted principal reads the rows of its clients and sites and
--    those without, writes only rows inside its scope), truncate guard;
--    every reference carries organization_id (Konvention 19).

ALTER TABLE probe ADD COLUMN origin text NOT NULL DEFAULT 'manual'
    CONSTRAINT probe_origin_check CHECK (origin IN ('manual', 'auto'));

CREATE TABLE probe_autocreate_suppression (
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       uuid NULL,
    site_id         uuid NULL,
    ci_id           uuid NOT NULL,
    probe_type      text NOT NULL CONSTRAINT probe_autocreate_suppression_type_check CHECK (probe_type ~ '^[a-z][a-z0-9_]{1,30}$'),
    created_at      timestamptz NOT NULL DEFAULT now(),
    actor_id        uuid NULL,
    PRIMARY KEY (organization_id, ci_id, probe_type),
    CONSTRAINT probe_autocreate_suppression_ci_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT probe_autocreate_suppression_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id),
    CONSTRAINT probe_autocreate_suppression_site_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id)
);
CREATE INDEX idx_probe_autocreate_suppression_client ON probe_autocreate_suppression (organization_id, client_id)
    WHERE client_id IS NOT NULL;
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON probe_autocreate_suppression
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- Client and site of an entry are those of its CI, read with the scope
-- widened (000139) so a scoped writer cannot keep a stale scope; the
-- policies check the row afterwards (like probe_ci_scope, 000307).
CREATE FUNCTION probe_autocreate_suppression_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
BEGIN
    previous := app_scope_widen();
    SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
      FROM ci c WHERE c.id = NEW.ci_id AND c.organization_id = NEW.organization_id FOR SHARE;
    PERFORM app_scope_restore(previous);
    RETURN NEW;
END
$$;
CREATE TRIGGER probe_autocreate_suppression_scope
    BEFORE INSERT OR UPDATE OF ci_id, client_id, site_id, organization_id ON probe_autocreate_suppression
    FOR EACH ROW EXECUTE FUNCTION probe_autocreate_suppression_scope();

-- A moved CI re-derives client and site of its suppression entries as of
-- its probes (propagate_monitoring_scope of 000307 plus this table).
CREATE OR REPLACE FUNCTION propagate_monitoring_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    IF TG_TABLE_NAME = 'ci' THEN
        UPDATE probe SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
        UPDATE monitoring_group_member SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
        UPDATE monitoring_profile SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
        UPDATE probe_autocreate_suppression SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
    ELSE
        UPDATE monitoring_group_member SET group_id = group_id WHERE group_id = NEW.id AND organization_id = NEW.organization_id;
        UPDATE monitoring_profile SET group_id = group_id WHERE group_id = NEW.id AND organization_id = NEW.organization_id;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

ALTER TABLE probe_autocreate_suppression ENABLE ROW LEVEL SECURITY;
ALTER TABLE probe_autocreate_suppression FORCE ROW LEVEL SECURITY;

DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_match CONSTANT text := 'site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[])';
    t CONSTANT text := 'probe_autocreate_suppression';
BEGIN
    EXECUTE format('CREATE POLICY %I ON %I USING (%s AND (%s OR client_id IS NULL OR %s) AND (%s OR site_id IS NULL OR %s)) '
        || 'WITH CHECK (%s AND (%s OR %s) AND (%s OR %s))',
        t || '_isolation', t, org_match, scope_all, scope_match, site_all, site_match,
        org_match, scope_all, scope_match, site_all, site_match);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR UPDATE USING (%s AND (%s OR %s) AND (%s OR %s)) '
        || 'WITH CHECK (%s AND (%s OR %s) AND (%s OR %s))',
        t || '_scoped_update', t, org_match, scope_all, scope_match, site_all, site_match,
        org_match, scope_all, scope_match, site_all, site_match);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR DELETE USING (%s AND (%s OR %s) AND (%s OR %s))',
        t || '_scoped_delete', t, org_match, scope_all, scope_match, site_all, site_match);
END
$$;
