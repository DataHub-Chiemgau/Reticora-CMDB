-- Migration 000247 down: drop the target bin of internal orders (WP-847).

DROP INDEX IF EXISTS idx_internal_order_target_location;
ALTER TABLE internal_order DROP CONSTRAINT IF EXISTS internal_order_target_location_fkey,
    DROP COLUMN IF EXISTS target_location_id;
