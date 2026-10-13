-- WP-108 (LOC-04, LOC-05, LOC-06): building names unique per site, room
-- names unique per building, rack names unique per room; the rack height
-- is called height_units (1–60, default 42).

-- Existing duplicates keep the oldest name; later ones get a short id
-- suffix so the unique keys hold.
UPDATE building b SET name = b.name || ' (' || left(b.id::text, 8) || ')'
WHERE EXISTS (SELECT 1 FROM building o WHERE o.site_id = b.site_id AND o.name = b.name
              AND (o.created_at, o.id) < (b.created_at, b.id));
UPDATE room r SET name = r.name || ' (' || left(r.id::text, 8) || ')'
WHERE EXISTS (SELECT 1 FROM room o WHERE o.building_id = r.building_id AND o.name = r.name
              AND (o.created_at, o.id) < (r.created_at, r.id));
UPDATE rack r SET name = r.name || ' (' || left(r.id::text, 8) || ')'
WHERE EXISTS (SELECT 1 FROM rack o WHERE o.room_id = r.room_id AND o.name = r.name
              AND (o.created_at, o.id) < (r.created_at, r.id));

ALTER TABLE building ADD CONSTRAINT building_site_name_key UNIQUE (site_id, name);
ALTER TABLE room ADD CONSTRAINT room_building_name_key UNIQUE (building_id, name);
ALTER TABLE rack ADD CONSTRAINT rack_room_name_key UNIQUE (room_id, name);

ALTER TABLE rack RENAME COLUMN height_u TO height_units;
ALTER TABLE rack RENAME CONSTRAINT rack_height_u_check TO rack_height_units_check;
