-- Migration 000082 down.

ALTER TABLE webhook_subscription DROP CONSTRAINT IF EXISTS webhook_subscription_service_account_fkey;
ALTER TABLE webhook_subscription DROP COLUMN IF EXISTS service_account_id;
ALTER TABLE api_key DROP CONSTRAINT IF EXISTS api_key_service_account_fkey;
ALTER TABLE api_key DROP COLUMN IF EXISTS service_account_id;
DROP TABLE IF EXISTS service_account_role;
DROP TABLE IF EXISTS service_account;
