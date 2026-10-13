-- Migration 000112 down: the former exclusion without the both face; no
-- composition check.

DROP TRIGGER trg_ci_mounted_composition_child ON ci;
DROP TRIGGER trg_rack_mount_composition_child ON rack_mount;
DROP FUNCTION rack_mount_not_composition_child();

ALTER TABLE rack_mount DROP CONSTRAINT rack_mount_no_overlap;
ALTER TABLE rack_mount ADD CONSTRAINT rack_mount_no_overlap
    EXCLUDE USING gist (rack_id WITH =, face WITH =, int4range(position_u, position_u + height_u) WITH &&)
    WHERE (face <> 'both');
DROP FUNCTION rack_face_range(text);
