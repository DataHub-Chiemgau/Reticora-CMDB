DROP TRIGGER trg_location_record_change ON location;
DROP FUNCTION location_record_change();
DROP TABLE location_change;

ALTER TABLE rack_mount DROP CONSTRAINT rack_mount_rack_id_fkey,
    ADD CONSTRAINT rack_mount_rack_id_fkey FOREIGN KEY (rack_id) REFERENCES rack(id) ON DELETE CASCADE;
ALTER TABLE asset_movement DROP CONSTRAINT asset_movement_to_location_id_fkey,
    ADD CONSTRAINT asset_movement_to_location_id_fkey FOREIGN KEY (to_location_id) REFERENCES location(id) ON DELETE SET NULL;
ALTER TABLE asset_movement DROP CONSTRAINT asset_movement_from_location_id_fkey,
    ADD CONSTRAINT asset_movement_from_location_id_fkey FOREIGN KEY (from_location_id) REFERENCES location(id) ON DELETE SET NULL;
ALTER TABLE quantity_item DROP CONSTRAINT quantity_item_location_id_fkey,
    ADD CONSTRAINT quantity_item_location_id_fkey FOREIGN KEY (location_id) REFERENCES location(id) ON DELETE SET NULL;
ALTER TABLE asset DROP CONSTRAINT asset_location_id_fkey,
    ADD CONSTRAINT asset_location_id_fkey FOREIGN KEY (location_id) REFERENCES location(id);
ALTER TABLE ci DROP CONSTRAINT ci_location_id_fkey,
    ADD CONSTRAINT ci_location_id_fkey FOREIGN KEY (location_id) REFERENCES location(id);
