-- Migration 000255: barcode of locations and the barcode register over all
-- object kinds (INV-03, WP-289, E-26).
--
-- 1. location gets barcode (TEXT). It is written directly on location, also
--    for the kinds with a specialist table: location_maintain locks only
--    name, move and client there.
-- 2. barcode_registry holds every code of an organization once, whatever
--    object carries it: the inventory number and the barcode of an asset,
--    the barcode of a consumable and of a location. UNIQUE (organization_id,
--    code) is the only conflict check: a 23505 from the index sees rows of
--    every client and parallel writers, which a SELECT over the specialist
--    tables under row level security would not (FORCE RLS, 000058). Each
--    row names exactly one object (barcode_registry_one_object); asset,
--    consumable, location and client are referenced with the organization
--    (K19) and ON DELETE CASCADE: a row only mirrors a code of its object.
--    client_id and site_id are the scope of the object (RuleLocatedScope of
--    WP-393 because of location_id): site_id of an asset from asset.site_id
--    (000151), of a location from location.site_id, NULL for a consumable;
--    client_id is NULL for an asset or consumable without client.
-- 3. Row level security with ENABLE and FORCE, barcode_registry_isolation
--    with the organization, client and site predicates (pattern 000058 and
--    000063, NULL client or site readable like asset_tag_link of 000249),
--    the restrictive barcode_registry_scoped_update and
--    barcode_registry_scoped_delete (pattern 000091, rule of 000079) and the
--    truncate guard of WP-352 (000144).
-- 4. Backfill in this order: asset.inventory_no, asset.barcode,
--    consumable.barcode, then location.barcode from
--    location_node_retired.node->>'barcode' over location_node_retired.
--    location_id, only when node->>'node_type' equals location.kind (the
--    node was mapped onto the location itself, not an ancestor of a floor,
--    desk or vehicle node) and exactly one such node carries a code for the
--    location. A code that is taken already is not registered (E-26): the
--    asset or consumable row goes to migration_quarantine with reason
--    barcode_conflict and its barcode becomes NULL (updated_at, version and
--    the history row of the asset stay, pattern 000249); for a location the
--    old value stays in location_node_retired and its node row is recorded
--    in migration_quarantine (barcode_conflict, or barcode_ambiguous when
--    several mapped nodes carry a code).
-- 5. barcode_registry_sync_asset, _consumable and _location (trigger
--    barcode_registry_sync, AFTER INSERT OR UPDATE, FOR EACH ROW) rebuild
--    the rows of the object when the code columns (barcode, for an asset
--    also inventory_no), client_id or site_id changed (OLD and NEW compared
--    with IS DISTINCT FROM in the function; UPDATE OF would miss values set
--    by BEFORE triggers, such as the mirror of asset_tag and inventory_no or
--    the site derived from the location). An asset whose barcode equals its
--    inventory number has one row (source inventory_no). The scope is
--    widened for the rebuild (app_scope_widen, 000139) like the other scope
--    propagations; deletion follows ON DELETE CASCADE. They are created
--    after the backfill, which writes the register itself.

-- ─── 1. location.barcode ──────────────────────────────────────────────────────

ALTER TABLE location ADD COLUMN barcode TEXT;

-- ─── 2. Register ──────────────────────────────────────────────────────────────

CREATE TABLE barcode_registry (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       UUID,
    site_id         UUID,
    code            TEXT NOT NULL,
    source          TEXT NOT NULL
        CONSTRAINT barcode_registry_source_check CHECK (source IN ('barcode', 'inventory_no')),
    asset_id        UUID,
    consumable_id   UUID,
    location_id     UUID,
    CONSTRAINT barcode_registry_organization_id_code_key UNIQUE (organization_id, code),
    CONSTRAINT barcode_registry_one_object CHECK (num_nonnulls(asset_id, consumable_id, location_id) = 1),
    CONSTRAINT barcode_registry_inventory_no_asset CHECK (source = 'barcode' OR asset_id IS NOT NULL),
    CONSTRAINT barcode_registry_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT barcode_registry_asset_fkey FOREIGN KEY (asset_id, organization_id)
        REFERENCES asset (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT barcode_registry_consumable_fkey FOREIGN KEY (consumable_id, organization_id)
        REFERENCES consumable (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT barcode_registry_location_fkey FOREIGN KEY (location_id, organization_id)
        REFERENCES location (id, organization_id) ON DELETE CASCADE
);

CREATE INDEX idx_barcode_registry_asset ON barcode_registry (asset_id) WHERE asset_id IS NOT NULL;
CREATE INDEX idx_barcode_registry_consumable ON barcode_registry (consumable_id) WHERE consumable_id IS NOT NULL;
CREATE INDEX idx_barcode_registry_location ON barcode_registry (location_id) WHERE location_id IS NOT NULL;
CREATE INDEX idx_barcode_registry_client ON barcode_registry (client_id) WHERE client_id IS NOT NULL;

-- ─── 3. Row level security ────────────────────────────────────────────────────

ALTER TABLE barcode_registry ENABLE ROW LEVEL SECURITY;
ALTER TABLE barcode_registry FORCE ROW LEVEL SECURITY;

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
        'CREATE POLICY barcode_registry_isolation ON barcode_registry '
        'USING (%1$s AND (%2$s OR client_id IS NULL OR client_id = ANY (%3$s)) '
            'AND (%4$s OR site_id IS NULL OR site_id = ANY (%5$s))) '
        'WITH CHECK (%6$s)',
        org_match, client_all, client_ids, site_all, site_ids, writable);
    EXECUTE format('CREATE POLICY barcode_registry_scoped_update ON barcode_registry AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                   writable, writable);
    EXECUTE format('CREATE POLICY barcode_registry_scoped_delete ON barcode_registry AS RESTRICTIVE FOR DELETE USING (%s)', writable);
END
$$;

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON barcode_registry
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── 4. Backfill ──────────────────────────────────────────────────────────────

-- The inventory numbers are unique per organization (idx_asset_tag_org).
INSERT INTO barcode_registry (organization_id, client_id, site_id, code, source, asset_id)
SELECT a.organization_id, a.client_id, a.site_id, a.inventory_no, 'inventory_no', a.id
  FROM asset a
 WHERE NULLIF(a.inventory_no, '') IS NOT NULL
ON CONFLICT (organization_id, code) DO NOTHING;

INSERT INTO barcode_registry (organization_id, client_id, site_id, code, source, asset_id)
SELECT a.organization_id, a.client_id, a.site_id, a.barcode, 'barcode', a.id
  FROM asset a
 WHERE NULLIF(a.barcode, '') IS NOT NULL AND a.barcode IS DISTINCT FROM a.inventory_no
 ORDER BY a.organization_id, a.created_at, a.id
ON CONFLICT (organization_id, code) DO NOTHING;

INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT a.organization_id, 'asset', to_jsonb(a), 'barcode_conflict', '000255'
  FROM asset a
 WHERE NULLIF(a.barcode, '') IS NOT NULL
   AND NOT EXISTS (SELECT 1 FROM barcode_registry r WHERE r.asset_id = a.id AND r.code = a.barcode);

ALTER TABLE asset DISABLE TRIGGER trg_asset_updated_at;
ALTER TABLE asset DISABLE TRIGGER asset_version_bump;
ALTER TABLE asset DISABLE TRIGGER asset_history;
UPDATE asset a SET barcode = NULL
  FROM migration_quarantine q
 WHERE q.migration = '000255' AND q.source_table = 'asset'
   AND q.organization_id = a.organization_id AND q.row_data ->> 'id' = a.id::text;
ALTER TABLE asset ENABLE TRIGGER asset_history;
ALTER TABLE asset ENABLE TRIGGER asset_version_bump;
ALTER TABLE asset ENABLE TRIGGER trg_asset_updated_at;

-- The barcodes of consumables are unique among them
-- (consumable_organization_id_barcode_key, 000252).
INSERT INTO barcode_registry (organization_id, client_id, code, source, consumable_id)
SELECT c.organization_id, c.client_id, c.barcode, 'barcode', c.id
  FROM consumable c
 WHERE NULLIF(c.barcode, '') IS NOT NULL
ON CONFLICT (organization_id, code) DO NOTHING;

INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT c.organization_id, 'consumable', to_jsonb(c), 'barcode_conflict', '000255'
  FROM consumable c
 WHERE NULLIF(c.barcode, '') IS NOT NULL
   AND NOT EXISTS (SELECT 1 FROM barcode_registry r WHERE r.consumable_id = c.id);

UPDATE consumable c SET barcode = NULL
  FROM migration_quarantine q
 WHERE q.migration = '000255' AND q.source_table = 'consumable'
   AND q.organization_id = c.organization_id AND q.row_data ->> 'id' = c.id::text;

-- Location codes of the retired side tree: only nodes mapped onto the
-- location itself, and only when exactly one of them carries a code.
CREATE TEMPORARY TABLE barcode_location_backfill AS
SELECT r.id AS node_id, r.organization_id, r.location_id, r.node ->> 'barcode' AS code,
       count(*) OVER (PARTITION BY r.location_id) AS candidates
  FROM location_node_retired r
  JOIN location l ON l.id = r.location_id AND l.organization_id = r.organization_id
 WHERE r.node ->> 'node_type' = l.kind
   AND NULLIF(btrim(r.node ->> 'barcode'), '') IS NOT NULL;

INSERT INTO barcode_registry (organization_id, client_id, site_id, code, source, location_id)
SELECT b.organization_id, l.client_id, l.site_id, b.code, 'barcode', b.location_id
  FROM barcode_location_backfill b
  JOIN location l ON l.id = b.location_id
 WHERE b.candidates = 1
 ORDER BY b.organization_id, l.path::text
ON CONFLICT (organization_id, code) DO NOTHING;

INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT r.organization_id, 'location_node_retired', to_jsonb(r),
       CASE WHEN b.candidates > 1 THEN 'barcode_ambiguous' ELSE 'barcode_conflict' END, '000255'
  FROM barcode_location_backfill b
  JOIN location_node_retired r ON r.id = b.node_id
 WHERE NOT EXISTS (SELECT 1 FROM barcode_registry g WHERE g.location_id = b.location_id);

DROP TABLE barcode_location_backfill;

ALTER TABLE location DISABLE TRIGGER trg_location_updated_at;
UPDATE location l SET barcode = r.code
  FROM barcode_registry r
 WHERE r.location_id = l.id;
ALTER TABLE location ENABLE TRIGGER trg_location_updated_at;

-- ─── 5. Register triggers ─────────────────────────────────────────────────────

CREATE FUNCTION barcode_registry_sync_asset() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
BEGIN
    IF TG_OP = 'UPDATE'
       AND NEW.barcode IS NOT DISTINCT FROM OLD.barcode
       AND NEW.inventory_no IS NOT DISTINCT FROM OLD.inventory_no
       AND NEW.client_id IS NOT DISTINCT FROM OLD.client_id
       AND NEW.site_id IS NOT DISTINCT FROM OLD.site_id THEN
        RETURN NULL;
    END IF;
    previous := app_scope_widen();
    IF TG_OP = 'UPDATE' THEN
        DELETE FROM barcode_registry WHERE asset_id = NEW.id;
    END IF;
    INSERT INTO barcode_registry (organization_id, client_id, site_id, code, source, asset_id)
    SELECT NEW.organization_id, NEW.client_id, NEW.site_id, c.code, c.source, NEW.id
      FROM (VALUES (NULLIF(NEW.inventory_no, ''), 'inventory_no'),
                   (NULLIF(NEW.barcode, ''), 'barcode')) AS c (code, source)
     WHERE c.code IS NOT NULL
       AND NOT (c.source = 'barcode' AND c.code IS NOT DISTINCT FROM NEW.inventory_no);
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER barcode_registry_sync AFTER INSERT OR UPDATE ON asset
    FOR EACH ROW EXECUTE FUNCTION barcode_registry_sync_asset();

CREATE FUNCTION barcode_registry_sync_consumable() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
BEGIN
    IF (CASE WHEN TG_OP = 'INSERT' THEN NULLIF(NEW.barcode, '') IS NULL
            ELSE NEW.barcode IS NOT DISTINCT FROM OLD.barcode
                 AND NEW.client_id IS NOT DISTINCT FROM OLD.client_id END) THEN
        RETURN NULL;
    END IF;
    previous := app_scope_widen();
    IF TG_OP = 'UPDATE' THEN
        DELETE FROM barcode_registry WHERE consumable_id = NEW.id;
    END IF;
    IF NULLIF(NEW.barcode, '') IS NOT NULL THEN
        INSERT INTO barcode_registry (organization_id, client_id, code, source, consumable_id)
        VALUES (NEW.organization_id, NEW.client_id, NEW.barcode, 'barcode', NEW.id);
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER barcode_registry_sync AFTER INSERT OR UPDATE ON consumable
    FOR EACH ROW EXECUTE FUNCTION barcode_registry_sync_consumable();

CREATE FUNCTION barcode_registry_sync_location() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
BEGIN
    IF (CASE WHEN TG_OP = 'INSERT' THEN NULLIF(NEW.barcode, '') IS NULL
            ELSE NEW.barcode IS NOT DISTINCT FROM OLD.barcode
                 AND NEW.client_id IS NOT DISTINCT FROM OLD.client_id
                 AND NEW.site_id IS NOT DISTINCT FROM OLD.site_id END) THEN
        RETURN NULL;
    END IF;
    previous := app_scope_widen();
    IF TG_OP = 'UPDATE' THEN
        DELETE FROM barcode_registry WHERE location_id = NEW.id;
    END IF;
    IF NULLIF(NEW.barcode, '') IS NOT NULL THEN
        INSERT INTO barcode_registry (organization_id, client_id, site_id, code, source, location_id)
        VALUES (NEW.organization_id, NEW.client_id, NEW.site_id, NEW.barcode, 'barcode', NEW.id);
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER barcode_registry_sync AFTER INSERT OR UPDATE ON location
    FOR EACH ROW EXECUTE FUNCTION barcode_registry_sync_location();
