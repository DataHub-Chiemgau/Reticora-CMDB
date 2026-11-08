-- Reverts 000275_p2_reference_integrity: the foreign keys of reservation,
-- the key of internal_order, the delete triggers of the document links and
-- their function are dropped. The quarantined links and references stay in
-- migration_quarantine (they named no row of the organization).

ALTER TABLE reservation DROP CONSTRAINT reservation_order_id_fkey,
    DROP CONSTRAINT reservation_ticket_id_fkey,
    DROP CONSTRAINT reservation_assignee_id_fkey;
ALTER TABLE internal_order DROP CONSTRAINT internal_order_id_organization_key;

DROP TRIGGER stock_movement_document_links ON stock_movement;
DROP TRIGGER stocktake_document_links ON stocktake;
DROP TRIGGER disposal_record_document_links ON disposal_record;
DROP TRIGGER location_document_links ON location;
DROP TRIGGER ticket_document_links ON ticket;
DROP TRIGGER asset_document_links ON asset;
DROP TRIGGER ci_document_links ON ci;
DROP FUNCTION delete_target_document_links();
