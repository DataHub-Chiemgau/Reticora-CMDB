-- Reverts 000240_internal_order_states: the client column, triggers and
-- policies of the positions, the reference check (the quarantined asset_id
-- comes back), received_quantity, the currency default, the free text
-- supplier (filled from supplier.name) and the old status check
-- (partially_received → ordered, closed → received). The supplier rows the
-- up migration created stay: they are master data, and a repeated up reuses
-- them through ON CONFLICT.

-- 7. and 6. Client of the positions.
DROP POLICY internal_order_item_scoped_delete ON internal_order_item;
DROP POLICY internal_order_item_scoped_update ON internal_order_item;
DROP POLICY internal_order_item_isolation ON internal_order_item;
CREATE POLICY internal_order_item_isolation ON internal_order_item
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DROP TRIGGER internal_order_client_items ON internal_order;
DROP FUNCTION internal_order_client_items();
DROP TRIGGER internal_order_item_client ON internal_order_item;
DROP FUNCTION internal_order_item_client();
DROP INDEX IF EXISTS idx_internal_order_item_client;
ALTER TABLE internal_order_item
    DROP CONSTRAINT internal_order_item_client_id_fkey,
    DROP COLUMN client_id;

-- 5. The quarantined asset references come back.
ALTER TABLE internal_order_item DROP CONSTRAINT internal_order_item_reference_check;
UPDATE internal_order_item t SET asset_id = (q.row_data ->> 'asset_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000240' AND q.source_table = 'internal_order_item'
   AND t.id = (q.row_data ->> 'id')::uuid
   AND EXISTS (SELECT 1 FROM asset x WHERE x.id = (q.row_data ->> 'asset_id')::uuid AND x.organization_id = t.organization_id);
DELETE FROM migration_quarantine WHERE migration = '000240';

-- 4. Received quantity.
ALTER TABLE internal_order_item
    DROP CONSTRAINT internal_order_item_received_quantity_check,
    DROP COLUMN received_quantity;

-- 3. Currency default.
ALTER TABLE internal_order ALTER COLUMN currency SET DEFAULT 'EUR';

-- 2. Free text supplier.
ALTER TABLE internal_order ADD COLUMN supplier TEXT;
UPDATE internal_order o SET supplier = s.name
FROM supplier s
WHERE s.id = o.supplier_id AND s.organization_id = o.organization_id;
DROP INDEX IF EXISTS idx_internal_order_supplier;
ALTER TABLE internal_order
    DROP CONSTRAINT internal_order_supplier_id_fkey,
    DROP COLUMN supplier_id;

-- 1. States.
UPDATE internal_order SET status = 'ordered' WHERE status = 'partially_received';
UPDATE internal_order SET status = 'received' WHERE status = 'closed';
ALTER TABLE internal_order DROP CONSTRAINT internal_order_status_check;
ALTER TABLE internal_order ADD CONSTRAINT internal_order_status_check
    CHECK (status IN ('draft','submitted','approved','rejected','ordered','received','cancelled'));
