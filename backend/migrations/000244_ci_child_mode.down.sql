-- Reverts 000244: no child mode, the location trigger of 000088.

DROP TRIGGER trg_ci_propagate_configuration_location ON ci;
DROP FUNCTION ci_propagate_configuration_location();

DROP TRIGGER trg_ci_derive_location ON ci;
CREATE TRIGGER trg_ci_derive_location BEFORE INSERT OR UPDATE OF location_id, site_id, room_id, client_id, asset_id ON ci
    FOR EACH ROW EXECUTE FUNCTION ci_derive_location();

DROP TRIGGER trg_ci_configuration_child ON ci;
DROP FUNCTION ci_configuration_child();

DROP TRIGGER trg_ci_child_mode ON ci;
DROP FUNCTION ci_child_mode_default();

ALTER TABLE ci
    DROP CONSTRAINT ci_configuration_child_asset,
    DROP CONSTRAINT ci_child_mode_parent,
    DROP CONSTRAINT ci_child_mode_check;

ALTER TABLE ci DROP COLUMN child_mode;
