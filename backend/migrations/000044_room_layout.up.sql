-- Migration 000044: room floor-plan object positions
--
-- The graphical room view (spec §8.5) places CIs/desks on the building's
-- floor plan. Positions are stored per room as a JSONB map of
-- object_id (CI) -> {x, y} in normalized 0..1 coordinates, so the layer is
-- resolution-independent and no schema change is needed per object type.
ALTER TABLE room ADD COLUMN IF NOT EXISTS layout JSONB NOT NULL DEFAULT '{}'::jsonb;
