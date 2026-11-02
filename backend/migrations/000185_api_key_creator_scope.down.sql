-- Migration 000185 down.

ALTER TABLE api_key DROP CONSTRAINT IF EXISTS api_key_creator_scope_check;
ALTER TABLE api_key DROP COLUMN IF EXISTS creator_scope;
