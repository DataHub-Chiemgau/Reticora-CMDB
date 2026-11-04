-- Migration 000240: internal orders after STK-05 (WP-286, E-41).
--
-- internal_order:
-- * internal_order_status_check knows the states of the order state
--   machine: draft, submitted, approved, rejected, ordered,
--   partially_received, received, closed and cancelled.
-- * supplier_id references the supplier master data with the organization
--   (K19, key of WP-736). The free text supplier becomes org-wide supplier
--   rows (client_id NULL; a row of the same name is reused, ON CONFLICT
--   like 000229 for asset.supplier), fills supplier_id and is dropped.
-- * currency loses its default EUR: the repository writes the currency of
--   the organization when the request has none.
--
-- internal_order_item:
-- * received_quantity (0 up to quantity) for the goods receipt (WP-760);
--   positions of received orders count as fully received.
-- * A position references a consumable or an asset, never both. Existing
--   positions with both are copied to migration_quarantine unchanged and
--   lose their asset_id.
-- * client_id with the organization (K19) is the client of the order: the
--   trigger internal_order_item_client derives it on every write of a
--   position, internal_order_client_items carries a client change of the
--   order to its positions. The isolation policy gets the client predicate
--   (E-09, pattern 000058), the restrictive policies for UPDATE and DELETE
--   follow 000079.

-- 1. States.
ALTER TABLE internal_order DROP CONSTRAINT internal_order_status_check;
ALTER TABLE internal_order ADD CONSTRAINT internal_order_status_check
    CHECK (status IN ('draft', 'submitted', 'approved', 'rejected', 'ordered', 'partially_received', 'received', 'closed', 'cancelled'));

-- 2. Supplier reference instead of the free text (E-41).
ALTER TABLE internal_order
    ADD COLUMN supplier_id UUID,
    ADD CONSTRAINT internal_order_supplier_id_fkey FOREIGN KEY (supplier_id, organization_id)
        REFERENCES supplier (id, organization_id);
CREATE INDEX IF NOT EXISTS idx_internal_order_supplier ON internal_order (supplier_id, organization_id) WHERE supplier_id IS NOT NULL;

INSERT INTO supplier (organization_id, name)
SELECT DISTINCT organization_id, btrim(supplier)
FROM internal_order
WHERE supplier IS NOT NULL AND btrim(supplier) <> ''
ON CONFLICT (organization_id, name) DO NOTHING;

UPDATE internal_order o SET supplier_id = s.id
FROM supplier s
WHERE s.organization_id = o.organization_id AND o.supplier IS NOT NULL AND btrim(o.supplier) <> ''
  AND s.name = btrim(o.supplier);

ALTER TABLE internal_order DROP COLUMN supplier;

-- 3. Currency of the organization instead of the fixed default.
ALTER TABLE internal_order ALTER COLUMN currency DROP DEFAULT;

-- 4. Received quantity.
ALTER TABLE internal_order_item
    ADD COLUMN received_quantity NUMERIC(12,2) NOT NULL DEFAULT 0,
    ADD CONSTRAINT internal_order_item_received_quantity_check
        CHECK (received_quantity >= 0 AND received_quantity <= quantity);
UPDATE internal_order_item i SET received_quantity = i.quantity
FROM internal_order o
WHERE o.id = i.order_id AND o.status = 'received';

-- 5. A consumable or an asset, not both.
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT i.organization_id, 'internal_order_item', to_jsonb(i),
       'STK-05 (WP-286): position references a consumable and an asset, asset_id set to NULL', '000240'
FROM internal_order_item i
WHERE i.consumable_id IS NOT NULL AND i.asset_id IS NOT NULL;
UPDATE internal_order_item SET asset_id = NULL
WHERE consumable_id IS NOT NULL AND asset_id IS NOT NULL;
ALTER TABLE internal_order_item ADD CONSTRAINT internal_order_item_reference_check
    CHECK (NOT (consumable_id IS NOT NULL AND asset_id IS NOT NULL));

-- 6. Client of the position.
ALTER TABLE internal_order_item
    ADD COLUMN client_id UUID,
    ADD CONSTRAINT internal_order_item_client_id_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id);
CREATE INDEX IF NOT EXISTS idx_internal_order_item_client ON internal_order_item (organization_id, client_id);
UPDATE internal_order_item i SET client_id = o.client_id
FROM internal_order o
WHERE o.id = i.order_id AND o.client_id IS NOT NULL;

CREATE FUNCTION internal_order_item_client() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    SELECT o.client_id INTO NEW.client_id FROM internal_order o WHERE o.id = NEW.order_id;
    RETURN NEW;
END
$$;

CREATE TRIGGER internal_order_item_client
    BEFORE INSERT OR UPDATE ON internal_order_item
    FOR EACH ROW EXECUTE FUNCTION internal_order_item_client();

CREATE FUNCTION internal_order_client_items() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE internal_order_item SET client_id = NEW.client_id WHERE order_id = NEW.id;
    RETURN NULL;
END
$$;

CREATE TRIGGER internal_order_client_items
    AFTER UPDATE OF client_id ON internal_order
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION internal_order_client_items();

-- 7. Client predicate of the positions (E-09, 000058, 000079).
DROP POLICY internal_order_item_isolation ON internal_order_item;
CREATE POLICY internal_order_item_isolation ON internal_order_item
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])));
CREATE POLICY internal_order_item_scoped_update ON internal_order_item AS RESTRICTIVE FOR UPDATE
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])));
CREATE POLICY internal_order_item_scoped_delete ON internal_order_item AS RESTRICTIVE FOR DELETE
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])));
