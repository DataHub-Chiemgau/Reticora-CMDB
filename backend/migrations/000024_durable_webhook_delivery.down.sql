-- Revert migration 000024: durable webhook delivery

DROP INDEX IF EXISTS idx_webhook_delivery_org_sub;
DROP INDEX IF EXISTS idx_webhook_delivery_due;

CREATE INDEX IF NOT EXISTS idx_webhook_delivery_retry
    ON webhook_delivery (next_retry_at)
    WHERE status = 'retrying';

DROP POLICY IF EXISTS webhook_delivery_isolation ON webhook_delivery;
CREATE POLICY webhook_delivery_isolation ON webhook_delivery
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

ALTER TABLE webhook_delivery ALTER COLUMN attempt SET DEFAULT 1;

ALTER TABLE webhook_delivery
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS error,
    DROP COLUMN IF EXISTS max_attempts;
