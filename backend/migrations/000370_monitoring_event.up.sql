-- DIS-06, WP-979: monitoring events (SNMP traps of the monitoring mode) of
-- the collectors with probe and CI assignment. The collector assigns the
-- id (UUIDv7), so a repeated delivery is no second row; the ingest worker
-- probe_ingest writes the events of a result batch in the same tenant
-- transaction as the results. kind is derived from snmpTrapOID.0
-- (1.3.6.1.6.3.1.1.5.1–5) or the generic SNMPv1 trap type (0–4).
--
-- Scope (Konvention 5, E-09): client_id and site_id come from the CI, an
-- event without CI takes those of the collector; they carry no foreign key
-- of their own. A restricted principal reads the events of its clients and
-- sites and those without and writes only inside its scope (pattern 000307).

CREATE TABLE monitoring_event (
    id              uuid        NOT NULL PRIMARY KEY,
    organization_id uuid        NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       uuid        NULL,
    site_id         uuid        NULL,
    collector_id    uuid        NOT NULL,
    ci_id           uuid        NULL,
    probe_id        uuid        NULL,
    source_ip       inet        NOT NULL,
    kind            text        NOT NULL,
    trap_oid        text        NOT NULL DEFAULT '',
    varbinds        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    truncated       boolean     NOT NULL DEFAULT false,
    observed_at     timestamptz NOT NULL,
    received_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT monitoring_event_id_org_key UNIQUE (id, organization_id),
    CONSTRAINT chk_monitoring_event_kind CHECK (kind IN ('link_down', 'link_up', 'cold_start', 'warm_start',
        'auth_failure', 'other')),
    CONSTRAINT chk_monitoring_event_varbinds CHECK (jsonb_typeof(varbinds) = 'object' AND octet_length(varbinds::text) <= 32768),
    CONSTRAINT chk_monitoring_event_trap_oid CHECK (char_length(trap_oid) <= 256),
    CONSTRAINT monitoring_event_ci_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT monitoring_event_probe_fkey FOREIGN KEY (probe_id, organization_id)
        REFERENCES probe (id, organization_id) ON DELETE SET NULL (probe_id),
    CONSTRAINT monitoring_event_collector_fkey FOREIGN KEY (collector_id, organization_id)
        REFERENCES collector (id, organization_id) ON DELETE CASCADE
);

COMMENT ON TABLE monitoring_event IS
    'SNMP traps of the monitoring mode as events with probe and CI assignment (DIS-06, WP-979); kept 30 days';

CREATE INDEX idx_monitoring_event_org_observed ON monitoring_event (organization_id, observed_at DESC);
CREATE INDEX idx_monitoring_event_ci ON monitoring_event (ci_id, observed_at DESC) WHERE ci_id IS NOT NULL;
CREATE INDEX idx_monitoring_event_probe ON monitoring_event (probe_id, observed_at DESC) WHERE probe_id IS NOT NULL;
CREATE INDEX idx_monitoring_event_collector ON monitoring_event (collector_id, organization_id);

-- client_id and site_id from the CI, without CI from the collector.
CREATE FUNCTION monitoring_event_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF NEW.ci_id IS NOT NULL THEN
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
          FROM ci c WHERE c.id = NEW.ci_id AND c.organization_id = NEW.organization_id;
    ELSE
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
          FROM collector c WHERE c.id = NEW.collector_id AND c.organization_id = NEW.organization_id;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER monitoring_event_scope BEFORE INSERT OR UPDATE OF ci_id, collector_id, organization_id ON monitoring_event
    FOR EACH ROW EXECUTE FUNCTION monitoring_event_scope();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON monitoring_event
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE monitoring_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE monitoring_event FORCE ROW LEVEL SECURITY;

DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_match CONSTANT text := 'site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[])';
BEGIN
    EXECUTE format('CREATE POLICY monitoring_event_isolation ON monitoring_event '
        || 'USING (%s AND (%s OR client_id IS NULL OR %s) AND (%s OR site_id IS NULL OR %s)) '
        || 'WITH CHECK (%s AND (%s OR %s) AND (%s OR %s))',
        org_match, scope_all, scope_match, site_all, site_match,
        org_match, scope_all, scope_match, site_all, site_match);
    EXECUTE format('CREATE POLICY monitoring_event_scoped_update ON monitoring_event AS RESTRICTIVE FOR UPDATE '
        || 'USING (%s AND (%s OR %s) AND (%s OR %s)) WITH CHECK (%s AND (%s OR %s) AND (%s OR %s))',
        org_match, scope_all, scope_match, site_all, site_match,
        org_match, scope_all, scope_match, site_all, site_match);
    EXECUTE format('CREATE POLICY monitoring_event_scoped_delete ON monitoring_event AS RESTRICTIVE FOR DELETE '
        || 'USING (%s AND (%s OR %s) AND (%s OR %s))',
        org_match, scope_all, scope_match, site_all, site_match);
END
$$;
