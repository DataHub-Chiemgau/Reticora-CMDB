-- Migration 000252: stock of consumables per bin (STK-01, STK-02, STK-03,
-- API-06, WP-283, E-40, E-41).
--
-- 1. consumable gets barcode (unique per organization where set, a partial
--    column index, no expression index, K22) and supplier_id, a reference
--    to supplier with the organization (K19, key supplier_id_organization_key
--    of 000223, WP-736). Deleting a supplier a consumable names fails with
--    23503 (422 through api.WriteDBError in the supplier handler).
-- 2. stock_level holds the stock of a consumable in a bin: on_hand and
--    reserved, never negative, reserved never above on_hand (STK-03; the
--    check is the last guard behind the row lock of consumable/stock.go).
--    consumable and location are referenced with the organization (K19,
--    keys of 000194), ON DELETE RESTRICT like every reference to location
--    (000157). client_id and site_id are derived from the bin, without a
--    foreign key (pattern 000061/000064).
-- 3. stock_level_derive_location (BEFORE INSERT OR UPDATE, pattern
--    ci_derive_location of 000074) reads the location with the scope
--    widened (app_scope_widen, 000139, like derive_location_site of 000151)
--    and sets client_id and site_id; a location that is no bin, or a
--    consumable of another client than the bin, is refused with 23514.
--    location_refresh_stock_level (AFTER UPDATE ON location when path,
--    site_id or client_id changed, pattern location_refresh_ci) re-derives
--    the rows of the bin; a client change that brings a consumable of
--    another client into the bin fails with 23514. consumable_client_stock
--    refuses a client of a consumable that does not match the bins of its
--    stock_level rows (23514, PATCH /consumables/{id} answers 422).
-- 4. Row level security with ENABLE and FORCE, stock_level_isolation with
--    the organization, client and site predicates (pattern 000063; both
--    columns are NOT NULL, so USING equals WITH CHECK), truncate guard of
--    WP-352.
-- 5. Data migration (E-40, quarantine like 000056, E-26): a free-text
--    location names no bin. Every consumable with stock_level <> 0 goes to
--    migration_quarantine with its row (reason stock_without_bin, negative
--    values negative_stock); so does a consumable with stock 0 and a
--    location text (reason location_without_bin), so the down migration
--    restores the text. No stock_level row and no ledger booking arises;
--    the operator books the stock as a correction into a bin. Afterwards the
--    columns consumable.stock_level and consumable.location are dropped.

-- ─── 1. consumable: barcode and supplier ──────────────────────────────────────

ALTER TABLE consumable ADD COLUMN barcode TEXT,
    ADD COLUMN supplier_id UUID,
    ADD CONSTRAINT consumable_supplier_id_fkey FOREIGN KEY (supplier_id, organization_id)
        REFERENCES supplier (id, organization_id);
CREATE UNIQUE INDEX consumable_organization_id_barcode_key ON consumable (organization_id, barcode)
    WHERE barcode IS NOT NULL;
CREATE INDEX idx_consumable_supplier ON consumable (supplier_id, organization_id)
    WHERE supplier_id IS NOT NULL;

-- ─── 2. stock_level ───────────────────────────────────────────────────────────

CREATE TABLE stock_level (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       UUID NOT NULL,
    site_id         UUID NOT NULL,
    consumable_id   UUID NOT NULL,
    location_id     UUID NOT NULL,
    on_hand         NUMERIC(12,2) NOT NULL DEFAULT 0
        CONSTRAINT stock_level_on_hand_check CHECK (on_hand >= 0),
    reserved        NUMERIC(12,2) NOT NULL DEFAULT 0
        CONSTRAINT stock_level_reserved_check CHECK (reserved >= 0 AND reserved <= on_hand),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT stock_level_consumable_id_location_id_key UNIQUE (consumable_id, location_id),
    CONSTRAINT stock_level_consumable_fkey FOREIGN KEY (consumable_id, organization_id)
        REFERENCES consumable (id, organization_id) ON DELETE RESTRICT,
    CONSTRAINT stock_level_location_fkey FOREIGN KEY (location_id, organization_id)
        REFERENCES location (id, organization_id) ON DELETE RESTRICT
);
CREATE INDEX idx_stock_level_location ON stock_level (location_id);
CREATE INDEX idx_stock_level_org_consumable ON stock_level (organization_id, consumable_id);

-- ─── 3. Derivation and guards ─────────────────────────────────────────────────

CREATE FUNCTION stock_level_derive_location() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    bin_kind TEXT;
    bin_client UUID;
    bin_site UUID;
    item_client UUID;
BEGIN
    previous := app_scope_widen();
    SELECT l.kind, l.client_id, l.site_id INTO bin_kind, bin_client, bin_site
      FROM location l WHERE l.id = NEW.location_id AND l.organization_id = NEW.organization_id;
    SELECT c.client_id INTO item_client
      FROM consumable c WHERE c.id = NEW.consumable_id AND c.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    IF bin_kind IS NULL THEN
        RAISE EXCEPTION 'location % not found', NEW.location_id
            USING ERRCODE = '23503', CONSTRAINT = 'stock_level_location_fkey';
    END IF;
    IF bin_kind <> 'bin' THEN
        RAISE EXCEPTION 'stock is kept in a bin, location % is a %', NEW.location_id, bin_kind
            USING ERRCODE = '23514', CONSTRAINT = 'stock_level_location_bin';
    END IF;
    IF item_client IS NOT NULL AND item_client <> bin_client THEN
        RAISE EXCEPTION 'consumable % belongs to another client than bin %', NEW.consumable_id, NEW.location_id
            USING ERRCODE = '23514', CONSTRAINT = 'stock_level_location_client';
    END IF;
    NEW.client_id := bin_client;
    NEW.site_id := bin_site;
    RETURN NEW;
END
$$;

CREATE TRIGGER stock_level_derive_location BEFORE INSERT OR UPDATE OF location_id, consumable_id, client_id, site_id
    ON stock_level FOR EACH ROW EXECUTE FUNCTION stock_level_derive_location();

CREATE FUNCTION location_refresh_stock_level() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE stock_level SET location_id = location_id WHERE location_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER location_refresh_stock_level AFTER UPDATE ON location
    FOR EACH ROW WHEN (OLD.path::text IS DISTINCT FROM NEW.path::text
                       OR OLD.site_id IS DISTINCT FROM NEW.site_id
                       OR OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION location_refresh_stock_level();

CREATE FUNCTION consumable_client_stock() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
    foreign_bin BOOLEAN;
BEGIN
    SELECT EXISTS (SELECT 1 FROM stock_level s
                    WHERE s.consumable_id = NEW.id AND s.organization_id = NEW.organization_id
                      AND s.client_id <> NEW.client_id)
      INTO foreign_bin;
    PERFORM app_scope_restore(previous);
    IF foreign_bin THEN
        RAISE EXCEPTION 'consumable % has stock in a bin of another client', NEW.id
            USING ERRCODE = '23514', CONSTRAINT = 'consumable_stock_level_client';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER consumable_client_stock BEFORE UPDATE OF client_id ON consumable
    FOR EACH ROW WHEN (NEW.client_id IS NOT NULL AND NEW.client_id IS DISTINCT FROM OLD.client_id)
    EXECUTE FUNCTION consumable_client_stock();

-- ─── 4. Row level security ────────────────────────────────────────────────────

ALTER TABLE stock_level ENABLE ROW LEVEL SECURITY;
ALTER TABLE stock_level FORCE ROW LEVEL SECURITY;

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON stock_level
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

CREATE POLICY stock_level_isolation ON stock_level
    USING (organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));

-- ─── 5. Data migration of the former stock ────────────────────────────────────

INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT c.organization_id, 'consumable', to_jsonb(c),
       CASE WHEN c.stock_level < 0 THEN 'negative_stock'
            WHEN c.stock_level > 0 THEN 'stock_without_bin'
            ELSE 'location_without_bin' END,
       '000252'
  FROM consumable c
 WHERE c.stock_level <> 0 OR NULLIF(btrim(c.location), '') IS NOT NULL;

ALTER TABLE consumable DROP COLUMN stock_level, DROP COLUMN location;
