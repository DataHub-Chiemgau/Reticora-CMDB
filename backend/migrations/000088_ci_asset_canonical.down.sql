DROP TRIGGER trg_asset_serial_mismatch ON asset;
DROP FUNCTION asset_check_serial_mismatch();
DROP TRIGGER trg_ci_serial_mismatch ON ci;
DROP FUNCTION ci_check_serial_mismatch();
DROP FUNCTION review_serial_mismatch(uuid);
DROP TRIGGER trg_asset_sync_primary_ci ON asset;
DROP FUNCTION asset_sync_primary_ci();
DROP TRIGGER trg_asset_propagate_location ON asset;
DROP FUNCTION asset_propagate_location();
DROP TRIGGER trg_ci_derive_location ON ci;
CREATE TRIGGER trg_ci_derive_location BEFORE INSERT OR UPDATE OF location_id, site_id, room_id, client_id ON ci
    FOR EACH ROW EXECUTE FUNCTION ci_derive_location();
DROP TRIGGER trg_ci_asset_location ON ci;
DROP FUNCTION ci_mirror_asset_location();

DROP INDEX review_item_open_serial_mismatch;
DELETE FROM review_item WHERE kind = 'serial_mismatch';
ALTER TABLE review_item DROP CONSTRAINT review_item_kind_check;
ALTER TABLE review_item ADD CONSTRAINT review_item_kind_check
    CHECK (kind = ANY (ARRAY['ambiguous_identity', 'conflicting_values', 'unclassified_device', 'override_conflict',
                             'unlicensed_ci']));

DROP INDEX idx_ci_asset;
ALTER TABLE ci DROP CONSTRAINT ci_asset_id_fkey;
ALTER TABLE ci DROP COLUMN asset_id;
