-- Migration 000265: scans of a stocktake classified against its frozen
-- expected stock, with the client and site of the stocktake (INV-01,
-- API-06, TEN-06, WP-287, E-09, E-38).
--
-- 1. stock_scan gets consumable_id, referenced with the organization, ON
--    DELETE RESTRICT (K19), the counted quantity of a consumable (set and
--    > 0 exactly when consumable_id is set), the read code (stored, not
--    resolved), legacy_scan_result and the scope columns client_id and
--    site_id without foreign key (pattern of stocktake, 000260). The place
--    found location_id comes from 000260.
-- 2. scan_result is found|missing|unexpected. The server sets found when
--    the object is in the snapshot, else unexpected; missing occurs in rows
--    of earlier releases only. Data migration: surplus and wrong_location
--    become unexpected, damaged becomes found; legacy_scan_result keeps the
--    original value of every earlier row. location_found stays as a read
--    field of those rows.
-- 3. stock_scan_derive_scope (BEFORE INSERT OR UPDATE of the references and
--    the scope columns) sets client_id and site_id from the stocktake, for a
--    stocktake without area (earlier releases) from the scanned asset,
--    consumable or CI, with the client and site scope widened (app_scope_widen,
--    000139). stocktake_refresh_scans follows a changed scope of the
--    stocktake (pattern stocktake_refresh_items, 000260). Earlier rows are
--    derived the same way.
-- 4. Row level security: stock_scan_isolation with the organization, client
--    and site predicates, rows without client or site readable in USING and
--    refused in WITH CHECK (pattern 000063, E-09), plus the restrictive
--    stock_scan_scoped_update and stock_scan_scoped_delete of 000079. The
--    entry stock_scan of rls.DerivedScopeTables (stocktake.scanVisible) goes.

-- ─── 1. Columns ────────────────────────────────────────────────────────────────

ALTER TABLE stock_scan ADD COLUMN consumable_id UUID,
    ADD COLUMN quantity NUMERIC(12,2),
    ADD COLUMN code TEXT,
    ADD COLUMN legacy_scan_result TEXT,
    ADD COLUMN client_id UUID,
    ADD COLUMN site_id UUID,
    ADD CONSTRAINT stock_scan_consumable_fkey FOREIGN KEY (consumable_id, organization_id)
        REFERENCES consumable (id, organization_id) ON DELETE RESTRICT,
    ADD CONSTRAINT stock_scan_quantity_check CHECK (
        (consumable_id IS NULL AND quantity IS NULL) OR (consumable_id IS NOT NULL AND quantity > 0));

CREATE INDEX idx_stock_scan_consumable ON stock_scan (consumable_id);
CREATE INDEX idx_stock_scan_client ON stock_scan (client_id);
CREATE INDEX idx_stock_scan_stocktake_scanned ON stock_scan (stocktake_id, scanned_at, id);

-- ─── 2. Classification ─────────────────────────────────────────────────────────

UPDATE stock_scan SET legacy_scan_result = scan_result;
ALTER TABLE stock_scan DROP CONSTRAINT scan_result_check;
UPDATE stock_scan SET scan_result = CASE scan_result
        WHEN 'surplus' THEN 'unexpected'
        WHEN 'wrong_location' THEN 'unexpected'
        WHEN 'damaged' THEN 'found'
        ELSE scan_result
    END
 WHERE scan_result IN ('surplus', 'wrong_location', 'damaged');
ALTER TABLE stock_scan ADD CONSTRAINT scan_result_check CHECK (scan_result IN ('found', 'missing', 'unexpected'));

-- ─── 3. Derivation of the scope ────────────────────────────────────────────────

CREATE FUNCTION stock_scan_derive_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
    area_client UUID;
    area_site UUID;
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    SELECT s.client_id, s.site_id INTO area_client, area_site
      FROM stocktake s WHERE s.id = NEW.stocktake_id AND s.organization_id = NEW.organization_id;
    IF area_client IS NOT NULL THEN
        NEW.client_id := area_client;
        NEW.site_id := area_site;
    ELSIF NEW.asset_id IS NOT NULL THEN
        SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id
          FROM asset a WHERE a.id = NEW.asset_id AND a.organization_id = NEW.organization_id;
    ELSIF NEW.consumable_id IS NOT NULL THEN
        SELECT c.client_id INTO NEW.client_id
          FROM consumable c WHERE c.id = NEW.consumable_id AND c.organization_id = NEW.organization_id;
    ELSIF NEW.ci_id IS NOT NULL THEN
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
          FROM ci c WHERE c.id = NEW.ci_id AND c.organization_id = NEW.organization_id;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NEW;
END
$$;

-- Releasing the CI of a scan (jobs.HardDeleteCI) does not re-derive.
CREATE TRIGGER stock_scan_derive_scope
    BEFORE INSERT OR UPDATE OF stocktake_id, asset_id, consumable_id, client_id, site_id ON stock_scan
    FOR EACH ROW EXECUTE FUNCTION stock_scan_derive_scope();

CREATE FUNCTION stocktake_refresh_scans() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE stock_scan SET stocktake_id = stocktake_id WHERE stocktake_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER stocktake_refresh_scans AFTER UPDATE OF client_id, site_id, location_id ON stocktake
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id
                       OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION stocktake_refresh_scans();

-- The migration runs without row level security.
UPDATE stock_scan SET stocktake_id = stocktake_id;

-- ─── 4. Row level security ─────────────────────────────────────────────────────

ALTER TABLE stock_scan ENABLE ROW LEVEL SECURITY;
ALTER TABLE stock_scan FORCE ROW LEVEL SECURITY;

DROP POLICY stock_scan_tenant_isolation ON stock_scan;

CREATE POLICY stock_scan_isolation ON stock_scan
    USING (organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));

CREATE POLICY stock_scan_scoped_update ON stock_scan AS RESTRICTIVE FOR UPDATE
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

CREATE POLICY stock_scan_scoped_delete ON stock_scan AS RESTRICTIVE FOR DELETE
    USING (organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));
