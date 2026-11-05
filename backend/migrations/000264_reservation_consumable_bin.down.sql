-- Reverts 000264_reservation_consumable_bin (WP-284).
--
-- 1. The functions and triggers return to their state before: the scope
--    derivation and propagation of 000258 (quantity_item), the search scope
--    of 000152 and derive_stock_movement_scope of 000256.
-- 2. reservation gets quantity_item_id back (foreign key of 000055).
--    Reservations of a consumable with a quantity_item row of the same id
--    return to item_kind quantity_item; a reservation of a consumable
--    without one (created after 000256) cannot be expressed and moves to
--    migration_quarantine (reason reservation_without_quantity_item; the up
--    migration brings it back). The reservations the up migration
--    quarantined (reservation_without_bin) are restored, those it released
--    (over_reservation) are active again, and its quarantine entries are
--    removed. stock_level.reserved returns to 0.
-- 3. consumable_id, location_id, their foreign key and index, the key of
--    stock_level and the checks of 000264 are gone; the checks of 000055
--    are back.

-- ─── 1. Functions and triggers ─────────────────────────────────────────────────

ALTER TABLE reservation ADD COLUMN quantity_item_id UUID
    CONSTRAINT reservation_quantity_item_id_fkey REFERENCES quantity_item (id) ON DELETE CASCADE;

DROP TRIGGER reservation_scope ON reservation;

CREATE OR REPLACE FUNCTION derive_reservation_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    IF NEW.asset_id IS NOT NULL THEN
        SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id
          FROM asset a WHERE a.id = NEW.asset_id AND a.organization_id = NEW.organization_id;
    ELSIF NEW.quantity_item_id IS NOT NULL THEN
        SELECT q.client_id, q.site_id INTO NEW.client_id, NEW.site_id
          FROM quantity_item q WHERE q.id = NEW.quantity_item_id AND q.organization_id = NEW.organization_id;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER reservation_scope
    BEFORE INSERT OR UPDATE OF organization_id, asset_id, quantity_item_id, client_id, site_id ON reservation
    FOR EACH ROW EXECUTE FUNCTION derive_reservation_scope();

CREATE OR REPLACE FUNCTION propagate_reservation_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    IF TG_TABLE_NAME = 'asset' THEN
        UPDATE reservation SET asset_id = asset_id
         WHERE asset_id = NEW.id AND organization_id = NEW.organization_id;
    ELSE
        UPDATE reservation SET quantity_item_id = quantity_item_id
         WHERE quantity_item_id = NEW.id AND organization_id = NEW.organization_id AND asset_id IS NULL;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

DROP TRIGGER stock_level_reservation_scope_propagation ON stock_level;
CREATE TRIGGER quantity_item_reservation_scope_propagation AFTER UPDATE ON quantity_item
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_reservation_scope();

CREATE OR REPLACE FUNCTION derive_search_document_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    CASE NEW.entity_type
        WHEN 'ci' THEN
            SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id FROM ci c WHERE c.id = NEW.entity_id;
        WHEN 'ticket' THEN
            SELECT t.client_id, t.site_id INTO NEW.client_id, NEW.site_id FROM ticket t WHERE t.id = NEW.entity_id;
        WHEN 'location' THEN
            SELECT l.client_id, l.site_id INTO NEW.client_id, NEW.site_id FROM location l WHERE l.id = NEW.entity_id;
        WHEN 'asset' THEN
            SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id FROM asset a WHERE a.id = NEW.entity_id;
        WHEN 'contact' THEN
            SELECT c.client_id INTO NEW.client_id FROM contact c WHERE c.id = NEW.entity_id;
        WHEN 'reservation' THEN
            -- An asset reservation has the scope of its asset, any other the
            -- scope of its quantity item.
            SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id
              FROM reservation r JOIN asset a ON a.id = r.asset_id WHERE r.id = NEW.entity_id;
            IF NOT FOUND THEN
                SELECT q.client_id, q.site_id INTO NEW.client_id, NEW.site_id
                  FROM reservation r JOIN quantity_item q ON q.id = r.quantity_item_id WHERE r.id = NEW.entity_id;
            END IF;
        ELSE
            NULL; -- document, compliance: org-wide
    END CASE;
    RETURN NEW;
END
$$;

-- propagate_search_scope re-derives the search rows of the changed entity;
-- for an asset or quantity item also those of its reservations.
CREATE OR REPLACE FUNCTION propagate_search_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE search_document SET entity_id = entity_id
     WHERE entity_type = TG_ARGV[0] AND entity_id = NEW.id;
    IF TG_ARGV[0] = 'asset' THEN
        UPDATE search_document SET entity_id = entity_id
         WHERE entity_type = 'reservation'
           AND entity_id IN (SELECT r.id FROM reservation r WHERE r.asset_id = NEW.id);
    ELSIF TG_ARGV[0] = 'quantity_item' THEN
        UPDATE search_document SET entity_id = entity_id
         WHERE entity_type = 'reservation'
           AND entity_id IN (SELECT r.id FROM reservation r WHERE r.quantity_item_id = NEW.id);
    END IF;
    RETURN NULL;
END
$$;

DROP TRIGGER reservation_search_scope_propagation ON reservation;
CREATE TRIGGER reservation_search_scope_propagation AFTER UPDATE ON reservation
    FOR EACH ROW WHEN (OLD.asset_id IS DISTINCT FROM NEW.asset_id OR OLD.quantity_item_id IS DISTINCT FROM NEW.quantity_item_id)
    EXECUTE FUNCTION propagate_search_scope('reservation');

-- derive_stock_movement_scope reads the object and the locations under the
-- writer's row level security (no app_scope_widen): what the writer cannot
-- see derives NULL.
CREATE OR REPLACE FUNCTION derive_stock_movement_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    object_site UUID;
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    CASE NEW.item_kind
        WHEN 'asset' THEN
            SELECT a.client_id, a.site_id INTO NEW.client_id, object_site
              FROM asset a WHERE a.id = NEW.asset_id AND a.organization_id = NEW.organization_id;
        WHEN 'consumable' THEN
            SELECT c.client_id INTO NEW.client_id
              FROM consumable c WHERE c.id = NEW.consumable_id AND c.organization_id = NEW.organization_id;
        ELSE
            NULL; -- refused by stock_movement_item_kind_check
    END CASE;
    IF NEW.to_location_id IS NOT NULL THEN
        SELECT l.site_id INTO NEW.site_id
          FROM location l WHERE l.id = NEW.to_location_id AND l.organization_id = NEW.organization_id;
    ELSIF NEW.from_location_id IS NOT NULL THEN
        SELECT l.site_id INTO NEW.site_id
          FROM location l WHERE l.id = NEW.from_location_id AND l.organization_id = NEW.organization_id;
    ELSE
        NEW.site_id := object_site;
    END IF;
    RETURN NEW;
END
$$;

-- ─── 2. Data ───────────────────────────────────────────────────────────────────

ALTER TABLE reservation DISABLE TRIGGER trg_reservation_updated_at;
ALTER TABLE reservation DROP CONSTRAINT reservation_item_check,
    DROP CONSTRAINT reservation_item_kind_check;

UPDATE reservation r SET state = q.row_data ->> 'state'
  FROM migration_quarantine q
 WHERE q.migration = '000264' AND q.reason = 'over_reservation'
   AND q.organization_id = r.organization_id AND q.row_data ->> 'id' = r.id::text;

WITH moved AS (
    DELETE FROM reservation r
     WHERE r.item_kind = 'consumable'
       AND NOT EXISTS (SELECT 1 FROM quantity_item q WHERE q.id = r.consumable_id AND q.organization_id = r.organization_id)
    RETURNING r.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'reservation', to_jsonb(moved) - 'quantity_item_id', 'reservation_without_quantity_item_pending', '000264'
  FROM moved;

UPDATE reservation
   SET item_kind = 'quantity_item', quantity_item_id = consumable_id, consumable_id = NULL, location_id = NULL
 WHERE item_kind = 'consumable';

INSERT INTO reservation
SELECT (jsonb_populate_record(NULL::reservation, q.row_data)).*
  FROM migration_quarantine q
 WHERE q.migration = '000264' AND q.reason = 'reservation_without_bin';

DELETE FROM migration_quarantine
 WHERE migration = '000264' AND reason IN ('reservation_without_bin', 'over_reservation');
UPDATE migration_quarantine SET reason = 'reservation_without_quantity_item'
 WHERE migration = '000264' AND reason = 'reservation_without_quantity_item_pending';

UPDATE stock_level SET reserved = 0 WHERE reserved <> 0;

ALTER TABLE reservation
    ADD CONSTRAINT reservation_item_kind_check CHECK (item_kind IN ('asset', 'quantity_item')),
    ADD CONSTRAINT reservation_item_check CHECK (asset_id IS NOT NULL OR quantity_item_id IS NOT NULL);
ALTER TABLE reservation ENABLE TRIGGER trg_reservation_updated_at;

-- ─── 3. Columns ────────────────────────────────────────────────────────────────

DROP INDEX idx_reservation_consumable_bin_active;
ALTER TABLE reservation DROP CONSTRAINT reservation_stock_level_fkey,
    DROP COLUMN location_id, DROP COLUMN consumable_id;
ALTER TABLE stock_level DROP CONSTRAINT stock_level_consumable_location_organization_key;

UPDATE search_document SET entity_id = entity_id WHERE entity_type = 'reservation';
