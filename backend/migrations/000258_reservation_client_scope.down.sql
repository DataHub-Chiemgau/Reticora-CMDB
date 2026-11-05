-- Reverts WP-750: organization-only policy of migration 000055, no derived
-- scope columns. The expires_at set by the backfill stays.

DROP POLICY reservation_scoped_delete ON reservation;
DROP POLICY reservation_scoped_update ON reservation;
DROP POLICY reservation_isolation ON reservation;
CREATE POLICY reservation_isolation ON reservation
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DROP TRIGGER quantity_item_reservation_scope_propagation ON quantity_item;
DROP TRIGGER asset_reservation_scope_propagation ON asset;
DROP FUNCTION propagate_reservation_scope();
DROP TRIGGER reservation_scope ON reservation;
DROP FUNCTION derive_reservation_scope();

DROP INDEX idx_reservation_site;
DROP INDEX idx_reservation_client;
ALTER TABLE reservation DROP COLUMN site_id, DROP COLUMN client_id;
