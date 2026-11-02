-- WP-434 (NFR-01, REC-02; F-114 identity part, SR-04): the MACs a CI is
-- identified by, in a side table whose B-tree index serves the identity
-- search under row level security.
--
-- The identity search of discovery (identityFilter, the batch candidate
-- queries) looked the interface MACs of the fingerprint up with
-- attributes @> '{"fingerprint":{"mac_addresses":[...]}}'. jsonb_contains is
-- not leakproof: below the RLS barrier of reticora_app it never becomes an
-- index condition, and one such term turns the whole OR of the identity
-- terms into a scan of every CI of the organization, on the queue path once
-- per re-discovered device.
--
-- 1. ci_identity_mac holds one row per CI and MAC: the primary MAC and every
--    string of attributes.fingerprint.mac_addresses that is a MAC address
--    (macaddr input, so the spelling does not matter; other values are
--    skipped), with the organization, client and site of the CI. The search
--    runs on (organization_id, mac) with leakproof equality only (uuid_eq,
--    macaddr_eq) and reaches ci through its primary key.
-- 2. sync_ci_identity_mac keeps the rows after INSERT and after an UPDATE
--    that changes the MACs, the client or the site. The trigger has no
--    column list: ci_derive_location changes site_id without it being in the
--    SET list (like ci_scope_propagation). Soft delete and restore change no
--    row: the search for deleted candidates (CI-13) uses the rows of
--    soft-deleted CIs as well. The rows go with the CI (foreign key with the
--    organization, ON DELETE CASCADE; ON UPDATE CASCADE for a CI that changes
--    organization, which only the migration role can do).
--    The trigger runs with the rights and the RLS of the writer (SECURITY
--    INVOKER, scope not widened): its rows carry exactly the organization,
--    client and site of the CI and are checked by the same predicates, so a
--    writer that may write the CI may write its rows, and nobody else.
-- 3. Policies as on ci (E-08, E-09): USING with the client and site
--    predicate (NULL stays readable), WITH CHECK without NULL for scoped
--    principals, restrictive UPDATE and DELETE policies like ci_scoped_update
--    and ci_scoped_delete; the TRUNCATE guard of the tenant tables.
-- 4. Backfill from every CI, soft-deleted ones included.

-- ─── 1. Table ──────────────────────────────────────────────────────────────────

CREATE TABLE ci_identity_mac (
    organization_id UUID NOT NULL,
    client_id       UUID,
    site_id         UUID,
    ci_id           UUID NOT NULL,
    mac             MACADDR NOT NULL,
    CONSTRAINT ci_identity_mac_pkey PRIMARY KEY (ci_id, mac),
    CONSTRAINT ci_identity_mac_ci_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON UPDATE CASCADE ON DELETE CASCADE
);

CREATE INDEX idx_ci_identity_mac ON ci_identity_mac (organization_id, mac);

-- ─── 2. Maintenance ────────────────────────────────────────────────────────────

-- The identity MACs of a CI: the primary MAC and the fingerprint strings that
-- are MAC addresses.
CREATE FUNCTION ci_identity_macs(primary_mac macaddr, attributes jsonb) RETURNS macaddr[]
LANGUAGE sql STABLE PARALLEL SAFE
SET search_path = pg_catalog
AS $$
    SELECT COALESCE(array_agg(DISTINCT m.mac), '{}')
      FROM (SELECT primary_mac AS mac
            UNION ALL
            SELECT CASE WHEN jsonb_typeof(e.value) = 'string' AND pg_input_is_valid(e.value #>> '{}', 'macaddr')
                        THEN (e.value #>> '{}')::macaddr END
              FROM jsonb_array_elements(CASE WHEN jsonb_typeof(attributes -> 'fingerprint' -> 'mac_addresses') = 'array'
                                             THEN attributes -> 'fingerprint' -> 'mac_addresses' END) AS e(value)) m
     WHERE m.mac IS NOT NULL
$$;

CREATE FUNCTION sync_ci_identity_mac() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    macs macaddr[] := ci_identity_macs(NEW.primary_mac, NEW.attributes);
BEGIN
    IF TG_OP = 'UPDATE' THEN
        DELETE FROM ci_identity_mac WHERE ci_id = NEW.id AND mac <> ALL (macs);
        UPDATE ci_identity_mac SET client_id = NEW.client_id, site_id = NEW.site_id
         WHERE ci_id = NEW.id AND (client_id, site_id) IS DISTINCT FROM (NEW.client_id, NEW.site_id);
    END IF;
    INSERT INTO ci_identity_mac (organization_id, client_id, site_id, ci_id, mac)
    SELECT NEW.organization_id, NEW.client_id, NEW.site_id, NEW.id, m FROM unnest(macs) AS m
    ON CONFLICT (ci_id, mac) DO NOTHING;
    RETURN NULL;
END
$$;

CREATE TRIGGER ci_identity_mac_insert AFTER INSERT ON ci
    FOR EACH ROW WHEN (NEW.primary_mac IS NOT NULL OR jsonb_typeof(NEW.attributes -> 'fingerprint' -> 'mac_addresses') = 'array')
    EXECUTE FUNCTION sync_ci_identity_mac();
CREATE TRIGGER ci_identity_mac_update AFTER UPDATE ON ci
    FOR EACH ROW WHEN (OLD.primary_mac IS DISTINCT FROM NEW.primary_mac
        OR (OLD.attributes -> 'fingerprint' -> 'mac_addresses') IS DISTINCT FROM (NEW.attributes -> 'fingerprint' -> 'mac_addresses')
        OR OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION sync_ci_identity_mac();

-- ─── 3. Policies ───────────────────────────────────────────────────────────────

ALTER TABLE ci_identity_mac ENABLE ROW LEVEL SECURITY;
ALTER TABLE ci_identity_mac FORCE ROW LEVEL SECURITY;

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
        'CREATE POLICY ci_identity_mac_isolation ON ci_identity_mac '
        'USING (%1$s AND (%2$s OR client_id IS NULL OR client_id = ANY (%3$s)) '
            'AND (%4$s OR site_id IS NULL OR site_id = ANY (%5$s))) '
        'WITH CHECK (%6$s)',
        org_match, client_all, client_ids, site_all, site_ids, writable);
    EXECUTE format('CREATE POLICY ci_identity_mac_scoped_update ON ci_identity_mac AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                   writable, writable);
    EXECUTE format('CREATE POLICY ci_identity_mac_scoped_delete ON ci_identity_mac AS RESTRICTIVE FOR DELETE USING (%s)', writable);
END
$$;

-- Every tenant table carries the TRUNCATE guard of the tenant tables.
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON ci_identity_mac
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── 4. Backfill ───────────────────────────────────────────────────────────────

INSERT INTO ci_identity_mac (organization_id, client_id, site_id, ci_id, mac)
SELECT c.organization_id, c.client_id, c.site_id, c.id, m.mac
  FROM ci c CROSS JOIN LATERAL unnest(ci_identity_macs(c.primary_mac, c.attributes)) AS m(mac)
 WHERE c.primary_mac IS NOT NULL OR jsonb_typeof(c.attributes -> 'fingerprint' -> 'mac_addresses') = 'array';

-- Statistics from the start: the identity search plans on the backfilled
-- rows, not on the estimate of a table never analyzed.
ANALYZE ci_identity_mac;
