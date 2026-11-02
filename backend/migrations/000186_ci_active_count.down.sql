-- Migration 000186 down: max_cis counts the CIs visible to the caller again
-- (state of WP-670).
DROP FUNCTION IF EXISTS ci_active_count();
