-- Migration 000129: discovery scopes (DIS-09, WP-153).
--
-- A scope is what one collector discovers: a CIDR, a host or an address
-- range of a client (and site) in a VRF, on a schedule (interval or cron),
-- with the credentials and plugins to use. The collector receives its
-- enabled scopes over its NATS channel; the run result is written back.
-- The site decides the client; without a site the collector's client and
-- site apply.

CREATE TABLE discovery_scope (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       UUID REFERENCES client (id) ON DELETE CASCADE,
    site_id         UUID REFERENCES site (id) ON DELETE SET NULL,
    collector_id    UUID NOT NULL REFERENCES collector (id) ON DELETE CASCADE,
    name            TEXT NOT NULL CHECK (btrim(name) <> ''),
    kind            TEXT NOT NULL CHECK (kind IN ('cidr', 'host', 'range')),
    target          TEXT NOT NULL CHECK (btrim(target) <> ''),
    vrf_id          UUID,
    schedule        TEXT NOT NULL DEFAULT '24h' CHECK (btrim(schedule) <> ''),
    credential_ids  UUID[] NOT NULL DEFAULT '{}',
    plugins         TEXT[] NOT NULL DEFAULT '{sweep,snmp}',
    enabled         BOOLEAN NOT NULL DEFAULT true,
    last_run_at     TIMESTAMPTZ,
    next_run_at     TIMESTAMPTZ,
    last_result     JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT discovery_scope_vrf_fkey FOREIGN KEY (vrf_id, organization_id) REFERENCES vrf (id, organization_id),
    CONSTRAINT discovery_scope_unique_target UNIQUE (collector_id, kind, target)
);
CREATE INDEX idx_discovery_scope_collector ON discovery_scope (organization_id, collector_id);
CREATE INDEX idx_discovery_scope_client ON discovery_scope (client_id);
CREATE INDEX idx_discovery_scope_site ON discovery_scope (site_id);

CREATE TRIGGER trg_discovery_scope_updated_at BEFORE UPDATE ON discovery_scope
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Collector and site belong to the scope's organization; the site decides
-- the client, otherwise the collector's client and site apply. A client
-- collector only discovers its own client.
CREATE FUNCTION discovery_scope_binding() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    col_org UUID;
    col_client UUID;
    col_site UUID;
    site_org UUID;
    site_client UUID;
BEGIN
    SELECT organization_id, client_id, site_id INTO col_org, col_client, col_site
        FROM collector WHERE id = NEW.collector_id;
    IF col_org IS DISTINCT FROM NEW.organization_id THEN
        RAISE EXCEPTION 'collector of another organization' USING ERRCODE = 'foreign_key_violation',
            CONSTRAINT = 'discovery_scope_collector_id_fkey';
    END IF;
    IF NEW.site_id IS NULL AND TG_OP = 'INSERT' THEN
        NEW.site_id := col_site;
    END IF;
    IF NEW.site_id IS NOT NULL THEN
        SELECT organization_id, client_id INTO site_org, site_client FROM site WHERE id = NEW.site_id;
        IF site_org IS DISTINCT FROM NEW.organization_id THEN
            RAISE EXCEPTION 'site of another organization' USING ERRCODE = 'foreign_key_violation',
                CONSTRAINT = 'discovery_scope_site_id_fkey';
        END IF;
        IF NEW.client_id IS NOT NULL AND NEW.client_id <> site_client THEN
            RAISE EXCEPTION 'the site belongs to another client' USING ERRCODE = 'check_violation',
                CONSTRAINT = 'discovery_scope_site_client';
        END IF;
        NEW.client_id := site_client;
    END IF;
    IF NEW.client_id IS NULL THEN
        NEW.client_id := col_client;
    END IF;
    IF col_client IS NOT NULL AND NEW.client_id IS DISTINCT FROM col_client THEN
        RAISE EXCEPTION 'the collector belongs to another client' USING ERRCODE = 'check_violation',
            CONSTRAINT = 'discovery_scope_collector_client';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_discovery_scope_binding BEFORE INSERT OR UPDATE ON discovery_scope
    FOR EACH ROW EXECUTE FUNCTION discovery_scope_binding();

ALTER TABLE discovery_scope ENABLE ROW LEVEL SECURITY;
ALTER TABLE discovery_scope FORCE ROW LEVEL SECURITY;
CREATE POLICY discovery_scope_isolation ON discovery_scope FOR ALL
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id IS NULL OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));
CREATE POLICY discovery_scope_scoped_update ON discovery_scope AS RESTRICTIVE FOR UPDATE
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));
CREATE POLICY discovery_scope_scoped_delete ON discovery_scope AS RESTRICTIVE FOR DELETE
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));
