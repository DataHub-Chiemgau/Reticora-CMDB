-- WP-1048 (ALR-06, E-160, E-181): availability targets per CI, service
-- (business_service/application), connection (port or CPE CI) or client.
--
-- scope_kind           ci, service, connection: ci_id is set and client_id /
--                      site_id follow the CI (the service copies them);
--                      client: ci_id is NULL and client_id is set
-- target_percent       0 < x <= 100, three decimals (99.950)
-- period               month, quarter or year
-- budget_warn_percent  the share of the error budget that raises the alarm
--                      (1..100, default 80; WP-1049)
-- calendar_id          the service time (business calendar), NULL: all day
--
-- One target per object and period (NULLS NOT DISTINCT). RLS and FORCE RLS
-- after Konvention 5 (E-09), but a restricted principal sees no
-- organization-wide row: a client principal reads only targets of its
-- clients. Composite foreign keys with organization_id (Konvention 19); a
-- deleted CI deletes its targets. UNIQUE (id, organization_id) for the
-- results of WP-1049.

CREATE TABLE availability_target (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id uuid,
    site_id uuid,
    name text NOT NULL CONSTRAINT availability_target_name_check CHECK (btrim(name) <> '' AND length(name) <= 200),
    scope_kind text NOT NULL
        CONSTRAINT availability_target_scope_kind_check CHECK (scope_kind IN ('ci', 'service', 'connection', 'client')),
    ci_id uuid,
    target_percent numeric(6,3) NOT NULL
        CONSTRAINT availability_target_target_percent_check CHECK (target_percent > 0 AND target_percent <= 100),
    period text NOT NULL CONSTRAINT availability_target_period_check CHECK (period IN ('month', 'quarter', 'year')),
    budget_warn_percent integer NOT NULL DEFAULT 80
        CONSTRAINT availability_target_budget_warn_percent_check CHECK (budget_warn_percent BETWEEN 1 AND 100),
    calendar_id uuid,
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT availability_target_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT availability_target_object_check CHECK (
        (scope_kind IN ('ci', 'service', 'connection') AND ci_id IS NOT NULL)
        OR (scope_kind = 'client' AND ci_id IS NULL AND client_id IS NOT NULL)),
    CONSTRAINT availability_target_object_key UNIQUE NULLS NOT DISTINCT
        (organization_id, scope_kind, ci_id, client_id, period),
    CONSTRAINT availability_target_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id),
    CONSTRAINT availability_target_ci_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT availability_target_calendar_fkey FOREIGN KEY (calendar_id, organization_id)
        REFERENCES business_calendar (id, organization_id)
);

CREATE INDEX idx_availability_target_ci ON availability_target (organization_id, ci_id) WHERE ci_id IS NOT NULL;
CREATE INDEX idx_availability_target_client ON availability_target (organization_id, client_id);
CREATE INDEX idx_availability_target_calendar ON availability_target (calendar_id) WHERE calendar_id IS NOT NULL;

ALTER TABLE availability_target ENABLE ROW LEVEL SECURITY;
ALTER TABLE availability_target FORCE ROW LEVEL SECURITY;

DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_match CONSTANT text := 'site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[])';
BEGIN
    -- A client principal reads no organization-wide target (client_id
    -- NULL); a site principal reads the targets of its sites and those of
    -- its clients without site.
    EXECUTE format('CREATE POLICY availability_target_isolation ON availability_target '
        || 'USING (%s AND (%s OR %s) AND (%s OR site_id IS NULL OR %s)) '
        || 'WITH CHECK (%s AND (%s OR %s) AND (%s OR %s))',
        org_match, scope_all, scope_match, site_all, site_match,
        org_match, scope_all, scope_match, site_all, site_match);
    EXECUTE format('CREATE POLICY availability_target_scoped_update ON availability_target AS RESTRICTIVE FOR UPDATE '
        || 'USING (%s AND (%s OR %s) AND (%s OR %s)) WITH CHECK (%s AND (%s OR %s) AND (%s OR %s))',
        org_match, scope_all, scope_match, site_all, site_match,
        org_match, scope_all, scope_match, site_all, site_match);
    EXECUTE format('CREATE POLICY availability_target_scoped_delete ON availability_target AS RESTRICTIVE FOR DELETE '
        || 'USING (%s AND (%s OR %s) AND (%s OR %s))',
        org_match, scope_all, scope_match, site_all, site_match);
END
$$;

-- Client and site of a target of a CI (ci, service, connection) are those
-- of its CI, read with the scope widened (000139, pattern probe_ci_scope of
-- 000307), so neither a direct INSERT nor a stale copy can put a target
-- into another client than its CI; the policies check the row afterwards.
-- A client target has no site.
CREATE FUNCTION availability_target_ci_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
BEGIN
    IF NEW.ci_id IS NOT NULL THEN
        previous := app_scope_widen();
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
          FROM ci c WHERE c.id = NEW.ci_id AND c.organization_id = NEW.organization_id FOR SHARE;
        PERFORM app_scope_restore(previous);
    ELSIF NEW.scope_kind = 'client' THEN
        NEW.site_id := NULL;
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER availability_target_ci_scope
    BEFORE INSERT OR UPDATE OF ci_id, client_id, site_id, organization_id, scope_kind ON availability_target
    FOR EACH ROW EXECUTE FUNCTION availability_target_ci_scope();

-- A moved CI re-derives client and site of its targets (a no-op UPDATE
-- re-runs the derivation), like trg_ci_monitoring_scope of 000307 for the
-- probes.
CREATE FUNCTION propagate_availability_target_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE availability_target SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_ci_availability_target_scope
    AFTER UPDATE ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_availability_target_scope();

CREATE TRIGGER trg_availability_target_updated_at BEFORE UPDATE ON availability_target
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON availability_target
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
