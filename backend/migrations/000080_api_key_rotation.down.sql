-- Migration 000080 down.

DROP POLICY IF EXISTS api_key_system_select ON api_key;
DROP INDEX IF EXISTS idx_api_key_prefix_hash;
ALTER TABLE api_key
    DROP CONSTRAINT IF EXISTS api_key_environment_check,
    DROP COLUMN IF EXISTS rotated_from,
    DROP COLUMN IF EXISTS environment;
