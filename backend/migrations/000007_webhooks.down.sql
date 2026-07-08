ALTER TABLE IF EXISTS webhook_delivery DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS webhook_subscription DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS webhook_delivery_isolation ON webhook_delivery;
DROP POLICY IF EXISTS webhook_sub_isolation ON webhook_subscription;

DROP INDEX IF EXISTS idx_webhook_delivery_retry;
DROP INDEX IF EXISTS idx_webhook_delivery_status;
DROP INDEX IF EXISTS idx_webhook_delivery_sub;
DROP INDEX IF EXISTS idx_webhook_sub_org;

DROP TABLE IF EXISTS webhook_delivery;
DROP TABLE IF EXISTS webhook_subscription;
