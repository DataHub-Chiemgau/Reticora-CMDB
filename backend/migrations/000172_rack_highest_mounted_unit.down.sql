-- Migration 000172 down: the height check of PATCH /racks/{id} falls back to
-- the mounts visible to the caller (state of WP-500).
DROP FUNCTION IF EXISTS rack_highest_mounted_unit(uuid);
