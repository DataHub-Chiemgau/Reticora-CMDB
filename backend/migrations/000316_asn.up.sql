-- WP-1066 (IPM-16, E-217 option 1): AS numbers and AS number ranges as
-- tables of the organization.
--
-- 1. asn: one AS number per organization (1 to 4294967294 without the
--    reserved 23456 (AS_TRANS) and 65535), with name, kind (own, customer,
--    peer, upstream, ixp), regional registry and an optional client.
-- 2. asn_range: a named block of AS numbers (range_start..range_end, both
--    inclusive; the API names them start and end) with purpose and an
--    optional client; ranges of an organization do not overlap (exclusion
--    constraint over int8range, btree_gist of 000021).
-- 3. Client foreign keys with organization_id (Konvention 19, key
--    client_id_organization_key of 000136); UNIQUE (id, organization_id)
--    for the references of later work packages (WP-1067, WP-1068).
-- 4. ENABLE and FORCE RLS after Konvention 5 (E-09; pattern
--    vlan_scoped_update/vlan_scoped_delete of 000091, monitoring_group of
--    000307): rows without client are read by every principal of the
--    organization and written only by principals without client and site
--    restriction; a principal restricted to clients reads and writes the
--    rows of its clients; a principal restricted to sites writes none (the
--    tables carry no site). Truncate guard, updated_at.

CREATE TABLE asn (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       uuid NULL,
    number          bigint NOT NULL,
    name            text NOT NULL CONSTRAINT asn_name_check CHECK (btrim(name) <> '' AND length(name) <= 200),
    kind            text NOT NULL CONSTRAINT asn_kind_check CHECK (kind IN ('own', 'customer', 'peer', 'upstream', 'ixp')),
    rir             text NULL CONSTRAINT asn_rir_check CHECK (rir IN ('ripe', 'arin', 'apnic', 'lacnic', 'afrinic', 'private')),
    description     text NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT asn_number_check CHECK (number BETWEEN 1 AND 4294967294 AND number NOT IN (23456, 65535)),
    CONSTRAINT asn_number_key UNIQUE (organization_id, number),
    CONSTRAINT asn_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT asn_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id)
);
CREATE INDEX idx_asn_client ON asn (organization_id, client_id) WHERE client_id IS NOT NULL;
CREATE INDEX idx_asn_kind ON asn (organization_id, kind);

CREATE TABLE asn_range (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       uuid NULL,
    name            text NOT NULL CONSTRAINT asn_range_name_check CHECK (btrim(name) <> '' AND length(name) <= 200),
    range_start     bigint NOT NULL,
    range_end       bigint NOT NULL,
    purpose         text NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT asn_range_bounds_check CHECK (range_start BETWEEN 1 AND 4294967294
        AND range_end BETWEEN 1 AND 4294967294 AND range_start <= range_end),
    CONSTRAINT asn_range_name_key UNIQUE (organization_id, name),
    CONSTRAINT asn_range_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT asn_range_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id),
    CONSTRAINT asn_range_no_overlap EXCLUDE USING gist (organization_id WITH =, int8range(range_start, range_end, '[]') WITH &&)
);
CREATE INDEX idx_asn_range_client ON asn_range (organization_id, client_id) WHERE client_id IS NOT NULL;

CREATE TRIGGER trg_asn_updated_at BEFORE UPDATE ON asn
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_asn_range_updated_at BEFORE UPDATE ON asn_range
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON asn
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON asn_range
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE asn ENABLE ROW LEVEL SECURITY;
ALTER TABLE asn FORCE ROW LEVEL SECURITY;
ALTER TABLE asn_range ENABLE ROW LEVEL SECURITY;
ALTER TABLE asn_range FORCE ROW LEVEL SECURITY;

DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['asn', 'asn_range'] LOOP
        EXECUTE format('CREATE POLICY %I ON %I USING (%s AND (%s OR client_id IS NULL OR %s)) WITH CHECK (%s AND (%s OR %s) AND %s)',
            t || '_isolation', t, org_match, scope_all, scope_match, org_match, scope_all, scope_match, site_all);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR UPDATE USING (%s AND (%s OR %s) AND %s) WITH CHECK (%s AND (%s OR %s) AND %s)',
            t || '_scoped_update', t, org_match, scope_all, scope_match, site_all, org_match, scope_all, scope_match, site_all);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR DELETE USING (%s AND (%s OR %s) AND %s)',
            t || '_scoped_delete', t, org_match, scope_all, scope_match, site_all);
    END LOOP;
END
$$;
