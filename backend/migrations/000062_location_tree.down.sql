-- Reverts WP-026: removes the location tree; site, building, room, rack and
-- location_node keep their data.

DROP TRIGGER IF EXISTS trg_site_location_insert ON site;
DROP TRIGGER IF EXISTS trg_site_location_update ON site;
DROP TRIGGER IF EXISTS trg_site_location_delete ON site;
DROP TRIGGER IF EXISTS trg_building_location_insert ON building;
DROP TRIGGER IF EXISTS trg_building_location_update ON building;
DROP TRIGGER IF EXISTS trg_building_location_delete ON building;
DROP TRIGGER IF EXISTS trg_room_location_insert ON room;
DROP TRIGGER IF EXISTS trg_room_location_update ON room;
DROP TRIGGER IF EXISTS trg_room_location_delete ON room;
DROP TRIGGER IF EXISTS trg_rack_location_insert ON rack;
DROP TRIGGER IF EXISTS trg_rack_location_update ON rack;
DROP TRIGGER IF EXISTS trg_rack_location_delete ON rack;

ALTER TABLE site DROP CONSTRAINT IF EXISTS site_location_fkey;
ALTER TABLE building DROP CONSTRAINT IF EXISTS building_location_fkey;
ALTER TABLE room DROP CONSTRAINT IF EXISTS room_location_fkey;
ALTER TABLE rack DROP CONSTRAINT IF EXISTS rack_location_fkey;

DROP TABLE IF EXISTS location;

DROP FUNCTION IF EXISTS location_import_nodes(UUID);
DROP FUNCTION IF EXISTS location_specialist_exists();
DROP FUNCTION IF EXISTS location_specialist_sync();
DROP FUNCTION IF EXISTS location_propagate();
DROP FUNCTION IF EXISTS location_maintain();
DROP FUNCTION IF EXISTS location_label(UUID);
DROP FUNCTION IF EXISTS location_parent_kind(TEXT);

DROP EXTENSION IF EXISTS ltree;
