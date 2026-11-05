-- Migration 000269: drop of the table quantity_item (STK-03, WP-758, E-40,
-- E-26).
--
-- Since 000256 (WP-757) every quantity_item is a consumable with the same
-- id and its stock a stock_level row; no path reads or writes the table
-- any more. The former references are gone: asset_movement (000246),
-- stock_movement.quantity_item_id (000256) and reservation.quantity_item_id
-- (000264).
--
-- 1. A foreign key that still references quantity_item aborts the
--    migration with the referencing constraints named (no CASCADE).
-- 2. Every row goes with its full row to migration_quarantine
--    (source_table 'quantity_item', migration '000269'): reason
--    quantity_item_archived when a consumable of the same id and
--    organization exists, quantity_item_not_merged otherwise.
-- 3. DROP TABLE quantity_item without CASCADE. With the table go its
--    policies (quantity_item_isolation, quantity_item_scoped_update,
--    quantity_item_scoped_delete of 000151), its triggers
--    (trg_quantity_item_updated_at, trg_quantity_item_derive_site,
--    quantity_item_search_scope_propagation, reticora_truncate_guard),
--    the index idx_quantity_item_site and its foreign keys.
-- 4. location_refresh_asset_site() (000151) loses its UPDATE of
--    quantity_item. propagate_search_scope() has no quantity_item branch
--    since 000264; derive_location_site() stays for asset.

-- ─── 1. Remaining references ──────────────────────────────────────────────────

DO $$
DECLARE
    refs text;
BEGIN
    SELECT string_agg(format('%s.%s', c.conrelid::regclass, c.conname), ', ' ORDER BY c.conrelid::regclass::text, c.conname)
      INTO refs
      FROM pg_constraint c
     WHERE c.contype = 'f' AND c.confrelid = 'quantity_item'::regclass;
    IF refs IS NOT NULL THEN
        RAISE EXCEPTION 'quantity_item (000269): foreign keys still reference the table: %', refs
            USING HINT = 'move the references to consumable before the drop (E-40)';
    END IF;
END
$$;

-- ─── 2. Safe keeping ──────────────────────────────────────────────────────────

DO $$
DECLARE
    archived bigint;
    not_merged bigint;
BEGIN
    INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
    SELECT q.organization_id, 'quantity_item', to_jsonb(q),
           CASE WHEN EXISTS (SELECT 1 FROM consumable c WHERE c.id = q.id AND c.organization_id = q.organization_id)
                THEN 'quantity_item_archived' ELSE 'quantity_item_not_merged' END,
           '000269'
      FROM quantity_item q
     ORDER BY q.organization_id, q.id;
    SELECT count(*) FILTER (WHERE reason = 'quantity_item_archived'),
           count(*) FILTER (WHERE reason = 'quantity_item_not_merged')
      INTO archived, not_merged
      FROM migration_quarantine WHERE migration = '000269' AND source_table = 'quantity_item';
    RAISE NOTICE 'quantity_item (000269): % rows archived, % rows without consumable kept in migration_quarantine',
        archived, not_merged;
END
$$;

-- ─── 3. Drop ──────────────────────────────────────────────────────────────────

DROP TABLE quantity_item;

-- ─── 4. Site propagation ──────────────────────────────────────────────────────

-- location_refresh_asset_site re-derives the assets of a location whose
-- site changed.
CREATE OR REPLACE FUNCTION location_refresh_asset_site() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE asset SET location_id = location_id WHERE location_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
