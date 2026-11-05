-- Migration 000264: quantity reservations on consumable and bin (STK-03,
-- WP-284, E-40, E-26, E-09).
--
-- 1. reservation gets consumable_id and location_id (the bin) with one
--    foreign key (consumable_id, location_id, organization_id) on the stock
--    row of the bin in stock_level, ON DELETE RESTRICT (K19, pattern 000136;
--    the key stock_level_consumable_location_organization_key arises here).
--    The partial column index idx_reservation_consumable_bin_active serves
--    the active reservations of a stock row (K22).
-- 2. Scope (E-40): derive_reservation_scope (trigger reservation_scope,
--    000258) takes client_id and site_id of a quantity reservation from the
--    stock row (consumable_id, location_id), i.e. the client and site of the
--    bin, not from consumable.client_id. It reads under the writer's row
--    level security like before. The propagation hangs on stock_level
--    instead of quantity_item (stock_level_reservation_scope_propagation;
--    location_refresh_stock_level re-derives the stock rows of a bin when
--    the bin changes client or site).
-- 3. Search scope: derive_search_document_scope reads client and site of a
--    reservation from its own columns; reservation_search_scope_propagation
--    reacts to client_id and site_id; propagate_search_scope loses its
--    quantity_item branch, which read reservation.quantity_item_id
--    (location_refresh_asset_site of 000151 still updates quantity_item).
-- 4. Ledger scope: the movements reservation and release of a consumable
--    take the client of their bin when the consumable has none, or is not
--    visible to the writer (an org-wide article reserved by a client-scoped
--    principal in its own bin, E-09); the other bookings are unchanged.
-- 5. Data (E-26): a reservation with quantity_item_id (consumable.id =
--    quantity_item.id since 000256) takes item_kind consumable,
--    consumable_id = quantity_item_id and the bin of the only stock_level
--    row of the article; a reservation without such a bin moves unchanged to
--    migration_quarantine (reason reservation_without_bin). Rows a former
--    down migration of 000264 quarantined come back. Per stock row the
--    youngest active reservations beyond on_hand are released and copied to
--    migration_quarantine (reason over_reservation, the row before the
--    release); afterwards stock_level.reserved is the sum of the active
--    quantities. A reservation naming an asset stays one of the asset (its
--    quantity_item_id, which the former check admitted beside it, is
--    dropped). quantity_item_id goes with its foreign key; the checks name
--    asset and consumable.
-- reservation keeps ENABLE + FORCE RLS and the policies of 000258, whose
-- site predicate covers the bin (RuleLocatedScope, WP-393).

-- ─── 1. Columns ────────────────────────────────────────────────────────────────

ALTER TABLE stock_level ADD CONSTRAINT stock_level_consumable_location_organization_key
    UNIQUE (consumable_id, location_id, organization_id);

ALTER TABLE reservation ADD COLUMN consumable_id UUID, ADD COLUMN location_id UUID,
    ADD CONSTRAINT reservation_stock_level_fkey FOREIGN KEY (consumable_id, location_id, organization_id)
        REFERENCES stock_level (consumable_id, location_id, organization_id) ON DELETE RESTRICT;

CREATE INDEX idx_reservation_consumable_bin_active ON reservation (consumable_id, location_id)
    WHERE state = 'active';

-- ─── 2. Scope derivation ───────────────────────────────────────────────────────

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
    ELSIF NEW.consumable_id IS NOT NULL THEN
        SELECT s.client_id, s.site_id INTO NEW.client_id, NEW.site_id
          FROM stock_level s
         WHERE s.consumable_id = NEW.consumable_id AND s.location_id = NEW.location_id
           AND s.organization_id = NEW.organization_id;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER reservation_scope
    BEFORE INSERT OR UPDATE OF organization_id, asset_id, consumable_id, location_id, client_id, site_id ON reservation
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
        UPDATE reservation SET location_id = location_id
         WHERE consumable_id = NEW.consumable_id AND location_id = NEW.location_id
           AND organization_id = NEW.organization_id;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

DROP TRIGGER quantity_item_reservation_scope_propagation ON quantity_item;
CREATE TRIGGER stock_level_reservation_scope_propagation AFTER UPDATE ON stock_level
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_reservation_scope();

-- ─── 3. Search scope ───────────────────────────────────────────────────────────

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
            -- The reservation carries the scope of its asset or bin
            -- (reservation_scope, 000258, 000264).
            SELECT r.client_id, r.site_id INTO NEW.client_id, NEW.site_id
              FROM reservation r WHERE r.id = NEW.entity_id;
        ELSE
            NULL; -- document, compliance: org-wide
    END CASE;
    RETURN NEW;
END
$$;

-- propagate_search_scope re-derives the search rows of the changed entity;
-- for an asset also those of its reservations.
CREATE OR REPLACE FUNCTION propagate_search_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE search_document SET entity_id = entity_id
     WHERE entity_type = TG_ARGV[0] AND entity_id = NEW.id;
    IF TG_ARGV[0] = 'asset' THEN
        UPDATE search_document SET entity_id = entity_id
         WHERE entity_type = 'reservation'
           AND entity_id IN (SELECT r.id FROM reservation r WHERE r.asset_id = NEW.id);
    END IF;
    RETURN NULL;
END
$$;

DROP TRIGGER reservation_search_scope_propagation ON reservation;
CREATE TRIGGER reservation_search_scope_propagation AFTER UPDATE ON reservation
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_search_scope('reservation');

-- ─── 4. Ledger scope ───────────────────────────────────────────────────────────

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
            -- A reservation or release of an article without client (or
            -- one the writer cannot see) takes the client of its bin
            -- (000264, WP-284).
            IF NEW.client_id IS NULL AND NEW.movement_type IN ('reservation', 'release') THEN
                SELECT l.client_id INTO NEW.client_id
                  FROM location l
                 WHERE l.id = COALESCE(NEW.to_location_id, NEW.from_location_id)
                   AND l.organization_id = NEW.organization_id;
            END IF;
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

-- ─── 5. Data ───────────────────────────────────────────────────────────────────

-- The migration runs without row level security; updated_at stays.
ALTER TABLE reservation DISABLE TRIGGER trg_reservation_updated_at;
ALTER TABLE reservation DROP CONSTRAINT reservation_item_check,
    DROP CONSTRAINT reservation_item_kind_check;

CREATE TEMPORARY TABLE reservation_bin_000264 AS
SELECT s.organization_id, s.consumable_id, min(s.location_id::text)::uuid AS location_id
  FROM stock_level s
 GROUP BY s.organization_id, s.consumable_id
HAVING count(*) = 1;

WITH moved AS (
    DELETE FROM reservation r
     WHERE r.asset_id IS NULL AND r.quantity_item_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM reservation_bin_000264 b
                        WHERE b.consumable_id = r.quantity_item_id AND b.organization_id = r.organization_id)
    RETURNING r.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'reservation', to_jsonb(moved), 'reservation_without_bin', '000264'
  FROM moved;

UPDATE reservation r
   SET item_kind = 'consumable', consumable_id = r.quantity_item_id, location_id = b.location_id,
       quantity_item_id = NULL
  FROM reservation_bin_000264 b
 WHERE r.asset_id IS NULL AND r.quantity_item_id IS NOT NULL
   AND b.consumable_id = r.quantity_item_id AND b.organization_id = r.organization_id;

UPDATE reservation SET item_kind = 'asset', quantity_item_id = NULL
 WHERE asset_id IS NOT NULL AND (item_kind <> 'asset' OR quantity_item_id IS NOT NULL);

-- Reservations a down migration of 000264 set aside (their article had no
-- quantity_item row) come back.
INSERT INTO reservation
SELECT (jsonb_populate_record(NULL::reservation, q.row_data)).*
  FROM migration_quarantine q
 WHERE q.migration = '000264' AND q.reason = 'reservation_without_quantity_item';
DELETE FROM migration_quarantine WHERE migration = '000264' AND reason = 'reservation_without_quantity_item';

ALTER TABLE reservation
    ADD CONSTRAINT reservation_item_kind_check CHECK (item_kind IN ('asset', 'consumable')),
    ADD CONSTRAINT reservation_item_check CHECK (
        (item_kind = 'asset' AND asset_id IS NOT NULL AND consumable_id IS NULL AND location_id IS NULL)
        OR (item_kind = 'consumable' AND consumable_id IS NOT NULL AND location_id IS NOT NULL AND asset_id IS NULL));

-- Over-reservation: per stock row the oldest active reservations stay as
-- far as on_hand covers them, the younger ones are released.
CREATE TEMPORARY TABLE reservation_over_000264 AS
SELECT x.id
  FROM (SELECT r.id, s.on_hand,
               sum(r.quantity) OVER (PARTITION BY r.consumable_id, r.location_id
                                     ORDER BY r.created_at, r.id) AS running
          FROM reservation r
          JOIN stock_level s ON s.consumable_id = r.consumable_id AND s.location_id = r.location_id
                            AND s.organization_id = r.organization_id
         WHERE r.item_kind = 'consumable' AND r.state = 'active') x
 WHERE x.running > x.on_hand;

INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT r.organization_id, 'reservation', to_jsonb(r), 'over_reservation', '000264'
  FROM reservation r JOIN reservation_over_000264 o ON o.id = r.id;

UPDATE reservation r SET state = 'released'
  FROM reservation_over_000264 o WHERE o.id = r.id;

UPDATE stock_level s SET reserved = t.total
  FROM (SELECT consumable_id, location_id, sum(quantity) AS total
          FROM reservation WHERE item_kind = 'consumable' AND state = 'active'
         GROUP BY consumable_id, location_id) t
 WHERE s.consumable_id = t.consumable_id AND s.location_id = t.location_id;

ALTER TABLE reservation ENABLE TRIGGER trg_reservation_updated_at;
DROP TABLE reservation_over_000264;
DROP TABLE reservation_bin_000264;

ALTER TABLE reservation DROP COLUMN quantity_item_id;

UPDATE search_document SET entity_id = entity_id WHERE entity_type = 'reservation';
