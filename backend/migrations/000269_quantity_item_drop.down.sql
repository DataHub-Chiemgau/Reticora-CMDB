-- Reverts 000269_quantity_item_drop (WP-758): quantity_item is rebuilt in
-- its state before the drop (000055 with location_id → location ON DELETE
-- RESTRICT of 000075, client_id → client (id, organization_id) of 000136,
-- site_id with its foreign key, index, derivation trigger and the policies
-- of 000151, quantity_item_search_scope_propagation of 000152, the TRUNCATE
-- guard of 000144 and UNIQUE (id, organization_id) of 000242).
-- location_refresh_asset_site() returns to its version of 000151. The rows
-- kept in migration_quarantine by 000269 come back unchanged (the
-- derivation of the site is off while they are inserted; a client, site or
-- location deleted since is NULL) and their entries are removed, so a
-- following down of 000256 works on the state it expects.

CREATE TABLE quantity_item (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id UUID,
    sku TEXT,
    name TEXT NOT NULL,
    category TEXT NOT NULL DEFAULT 'general',
    unit TEXT NOT NULL DEFAULT 'pcs',
    stock_level NUMERIC(12,2) NOT NULL DEFAULT 0,
    min_level NUMERIC(12,2) NOT NULL DEFAULT 0,
    location_id UUID,
    notes TEXT,
    attributes JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    site_id UUID,
    UNIQUE (organization_id, sku),
    CONSTRAINT quantity_item_client_id_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id) ON DELETE SET NULL (client_id),
    CONSTRAINT quantity_item_location_id_fkey FOREIGN KEY (location_id) REFERENCES location(id) ON DELETE RESTRICT,
    CONSTRAINT quantity_item_site_id_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id),
    CONSTRAINT quantity_item_id_organization_key UNIQUE (id, organization_id)
);

CREATE INDEX idx_quantity_item_site ON quantity_item (site_id) WHERE site_id IS NOT NULL;

ALTER TABLE quantity_item ENABLE ROW LEVEL SECURITY;
ALTER TABLE quantity_item FORCE ROW LEVEL SECURITY;

-- The policies of 000151.
CREATE POLICY quantity_item_isolation ON quantity_item
    USING (organization_id = current_setting('app.org_id')::UUID
           AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL
                OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
           AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id IS NULL
                OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID
           AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
                OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
           AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
                OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));
CREATE POLICY quantity_item_scoped_update ON quantity_item AS RESTRICTIVE FOR UPDATE
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
CREATE POLICY quantity_item_scoped_delete ON quantity_item AS RESTRICTIVE FOR DELETE
    USING (organization_id = current_setting('app.org_id')::UUID
           AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
                OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
           AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
                OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));

CREATE TRIGGER trg_quantity_item_updated_at BEFORE UPDATE ON quantity_item
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_quantity_item_derive_site BEFORE INSERT OR UPDATE OF location_id, site_id ON quantity_item
    FOR EACH ROW EXECUTE FUNCTION derive_location_site();
CREATE TRIGGER quantity_item_search_scope_propagation AFTER UPDATE ON quantity_item
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_search_scope('quantity_item');
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON quantity_item
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- The version of 000151.
CREATE OR REPLACE FUNCTION location_refresh_asset_site() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE asset SET location_id = location_id WHERE location_id = NEW.id;
    UPDATE quantity_item SET location_id = location_id WHERE location_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

-- The kept rows, with the site as kept (the derivation is off for this
-- statement only).
ALTER TABLE quantity_item DISABLE TRIGGER trg_quantity_item_derive_site;
INSERT INTO quantity_item
SELECT r.id, r.organization_id,
       CASE WHEN EXISTS (SELECT 1 FROM client c WHERE c.id = r.client_id AND c.organization_id = r.organization_id) THEN r.client_id END,
       r.sku, r.name, r.category, r.unit, r.stock_level, r.min_level,
       CASE WHEN EXISTS (SELECT 1 FROM location l WHERE l.id = r.location_id) THEN r.location_id END,
       r.notes, r.attributes, r.created_at, r.updated_at,
       CASE WHEN EXISTS (SELECT 1 FROM site s WHERE s.id = r.site_id AND s.organization_id = r.organization_id) THEN r.site_id END
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::quantity_item, q.row_data) r
 WHERE q.migration = '000269' AND q.source_table = 'quantity_item'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = r.organization_id)
 ORDER BY q.id;
ALTER TABLE quantity_item ENABLE TRIGGER trg_quantity_item_derive_site;

DELETE FROM migration_quarantine WHERE migration = '000269' AND source_table = 'quantity_item';
