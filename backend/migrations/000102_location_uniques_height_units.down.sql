ALTER TABLE rack RENAME CONSTRAINT rack_height_units_check TO rack_height_u_check;
ALTER TABLE rack RENAME COLUMN height_units TO height_u;

ALTER TABLE rack DROP CONSTRAINT rack_room_name_key;
ALTER TABLE room DROP CONSTRAINT room_building_name_key;
ALTER TABLE building DROP CONSTRAINT building_site_name_key;
