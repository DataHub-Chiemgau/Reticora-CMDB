-- Migration 000260: area of a stocktake from the location tree, frozen
-- expected stock and the location references of the stocktake (INV-01,
-- API-06, WP-848, E-09, E-38).
--
-- 1. stocktake gets UNIQUE (id, organization_id) for references with the
--    organization (K19) and the area location_id, referenced with the
--    organization, ON DELETE RESTRICT like every reference to location
--    (000157). client_id and site_id are derived from the node, without a
--    foreign key (pattern 000061/000064). Rows of earlier releases keep
--    status and scope; their location_id, client_id and site_id stay NULL
--    (org-wide stocktake, written by org-wide principals only, E-09).
-- 2. stocktake_derive_location (BEFORE INSERT OR UPDATE, pattern
--    ci_derive_location of 000074) reads the node under the row level
--    security of the writer: a node of another organization, an unknown
--    node or one outside the scope is refused with 23503 on
--    stocktake_location_fkey (422 on location_id). location_refresh_stocktake
--    (AFTER UPDATE ON location when site_id or client_id changed, pattern
--    location_refresh_stock_level of 000252) re-derives the stocktakes of
--    the node with the scope widened.
-- 3. stocktake_item is the expected stock frozen by POST …/start: one row
--    per asset (expected_quantity 1) and per stock_level row of a bin
--    (expected_quantity on_hand) in the subtree of the area. client_id and
--    site_id are those of the stocktake (stocktake_item_derive_scope,
--    BEFORE INSERT OR UPDATE); stocktake_refresh_items follows a changed
--    scope of the stocktake. counted_quantity and classification are
--    written by WP-287 and stay NULL until the completion.
-- 4. stock_scan gets location_id (place found), referenced with the
--    organization, ON DELETE RESTRICT; WP-287 fills it. Until then
--    stock_scan stays in rls.DerivedScopeTables.
-- 5. Row level security: stocktake with nullable scope columns gets
--    stocktake_isolation (organization, client and site predicates, NULL
--    visible in USING and refused in WITH CHECK, pattern 000063, E-09) and
--    the restrictive stocktake_scoped_update/stocktake_scoped_delete of
--    000079. stocktake_item (scope columns NOT NULL) gets
--    stocktake_item_isolation with USING equal to WITH CHECK, ENABLE and
--    FORCE and the truncate guard of WP-352.

-- ─── 1. stocktake: key and area ───────────────────────────────────────────────

ALTER TABLE stocktake ADD CONSTRAINT stocktake_id_organization_key UNIQUE (id, organization_id),
    ADD COLUMN location_id UUID,
    ADD COLUMN client_id UUID,
    ADD COLUMN site_id UUID,
    ADD CONSTRAINT stocktake_location_fkey FOREIGN KEY (location_id, organization_id)
        REFERENCES location (id, organization_id) ON DELETE RESTRICT;
CREATE INDEX idx_stocktake_location ON stocktake (location_id);
CREATE INDEX idx_stocktake_client ON stocktake (client_id);

-- ─── 2. Derivation of the area scope ──────────────────────────────────────────

CREATE FUNCTION stocktake_derive_location() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    node_client UUID;
    node_site UUID;
BEGIN
    IF NEW.location_id IS NULL THEN
        NEW.client_id := NULL;
        NEW.site_id := NULL;
        RETURN NEW;
    END IF;
    SELECT l.client_id, l.site_id INTO node_client, node_site
      FROM location l WHERE l.id = NEW.location_id AND l.organization_id = NEW.organization_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'location % not found', NEW.location_id
            USING ERRCODE = '23503', CONSTRAINT = 'stocktake_location_fkey';
    END IF;
    NEW.client_id := node_client;
    NEW.site_id := node_site;
    RETURN NEW;
END
$$;

CREATE TRIGGER stocktake_derive_location BEFORE INSERT OR UPDATE OF location_id, client_id, site_id
    ON stocktake FOR EACH ROW EXECUTE FUNCTION stocktake_derive_location();

CREATE FUNCTION location_refresh_stocktake() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE stocktake SET location_id = location_id WHERE location_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER location_refresh_stocktake AFTER UPDATE ON location
    FOR EACH ROW WHEN (OLD.site_id IS DISTINCT FROM NEW.site_id
                       OR OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION location_refresh_stocktake();

-- ─── 3. stocktake_item: the frozen expected stock ─────────────────────────────

CREATE TABLE stocktake_item (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id   UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id         UUID NOT NULL,
    site_id           UUID NOT NULL,
    stocktake_id      UUID NOT NULL,
    item_kind         TEXT NOT NULL
        CONSTRAINT stocktake_item_kind_check CHECK (item_kind IN ('asset', 'consumable')),
    asset_id          UUID,
    consumable_id     UUID,
    location_id       UUID NOT NULL,
    expected_quantity NUMERIC(12,2) NOT NULL
        CONSTRAINT stocktake_item_expected_check CHECK (expected_quantity >= 0),
    counted_quantity  NUMERIC(12,2)
        CONSTRAINT stocktake_item_counted_check CHECK (counted_quantity >= 0),
    classification    TEXT
        CONSTRAINT stocktake_item_classification_check CHECK (classification IN ('found', 'missing', 'unexpected')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT stocktake_item_object_check CHECK (
        (item_kind = 'asset' AND asset_id IS NOT NULL AND consumable_id IS NULL)
        OR (item_kind = 'consumable' AND consumable_id IS NOT NULL AND asset_id IS NULL)),
    CONSTRAINT stocktake_item_stocktake_fkey FOREIGN KEY (stocktake_id, organization_id)
        REFERENCES stocktake (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT stocktake_item_asset_fkey FOREIGN KEY (asset_id, organization_id)
        REFERENCES asset (id, organization_id) ON DELETE RESTRICT,
    CONSTRAINT stocktake_item_consumable_fkey FOREIGN KEY (consumable_id, organization_id)
        REFERENCES consumable (id, organization_id) ON DELETE RESTRICT,
    CONSTRAINT stocktake_item_location_fkey FOREIGN KEY (location_id, organization_id)
        REFERENCES location (id, organization_id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX stocktake_item_asset_key ON stocktake_item (stocktake_id, asset_id)
    WHERE asset_id IS NOT NULL;
CREATE UNIQUE INDEX stocktake_item_consumable_key ON stocktake_item (stocktake_id, consumable_id, location_id)
    WHERE consumable_id IS NOT NULL;
CREATE INDEX idx_stocktake_item_stocktake ON stocktake_item (stocktake_id, id);
CREATE INDEX idx_stocktake_item_asset ON stocktake_item (asset_id);
CREATE INDEX idx_stocktake_item_consumable ON stocktake_item (consumable_id);
CREATE INDEX idx_stocktake_item_location ON stocktake_item (location_id);

CREATE FUNCTION stocktake_item_derive_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    area_client UUID;
    area_site UUID;
BEGIN
    previous := app_scope_widen();
    SELECT s.client_id, s.site_id INTO area_client, area_site
      FROM stocktake s WHERE s.id = NEW.stocktake_id AND s.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    IF area_client IS NULL OR area_site IS NULL THEN
        RAISE EXCEPTION 'stocktake % has no area', NEW.stocktake_id
            USING ERRCODE = '23514', CONSTRAINT = 'stocktake_item_area';
    END IF;
    NEW.client_id := area_client;
    NEW.site_id := area_site;
    RETURN NEW;
END
$$;

CREATE TRIGGER stocktake_item_derive_scope BEFORE INSERT OR UPDATE OF stocktake_id, client_id, site_id
    ON stocktake_item FOR EACH ROW EXECUTE FUNCTION stocktake_item_derive_scope();

CREATE FUNCTION stocktake_refresh_items() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE stocktake_item SET stocktake_id = stocktake_id WHERE stocktake_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER stocktake_refresh_items AFTER UPDATE OF client_id, site_id, location_id ON stocktake
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id
                       OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION stocktake_refresh_items();

-- ─── 4. stock_scan: place found ───────────────────────────────────────────────

ALTER TABLE stock_scan ADD COLUMN location_id UUID,
    ADD CONSTRAINT stock_scan_location_fkey FOREIGN KEY (location_id, organization_id)
        REFERENCES location (id, organization_id) ON DELETE RESTRICT;
CREATE INDEX idx_stock_scan_location ON stock_scan (location_id);

-- ─── 5. Row level security ────────────────────────────────────────────────────

DROP POLICY stocktake_tenant_isolation ON stocktake;

CREATE POLICY stocktake_isolation ON stocktake
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

CREATE POLICY stocktake_scoped_update ON stocktake AS RESTRICTIVE FOR UPDATE
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

CREATE POLICY stocktake_scoped_delete ON stocktake AS RESTRICTIVE FOR DELETE
    USING (organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));

ALTER TABLE stocktake_item ENABLE ROW LEVEL SECURITY;
ALTER TABLE stocktake_item FORCE ROW LEVEL SECURITY;

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON stocktake_item
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

CREATE POLICY stocktake_item_isolation ON stocktake_item
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
