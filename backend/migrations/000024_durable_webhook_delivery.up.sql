-- Migration 000024: durable webhook delivery
--
-- Webhook deliveries were tracked in process memory, so a restart lost every
-- pending retry. The delivery table now records the retry budget and the last
-- error, and the dispatcher persists each attempt.
--
-- The background dispatcher has to find due deliveries across all tenants. It
-- opts into a dedicated session flag (`app.system`) that is only ever set by the
-- delivery store; request-scoped code paths keep setting `app.org_id` and stay
-- restricted to their own organization.

ALTER TABLE webhook_delivery
    ADD COLUMN IF NOT EXISTS max_attempts INTEGER NOT NULL DEFAULT 5,
    ADD COLUMN IF NOT EXISTS error TEXT,
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- Deliveries are queued before the first attempt, so a freshly inserted row is
-- immediately due.
ALTER TABLE webhook_delivery ALTER COLUMN attempt SET DEFAULT 0;

DROP POLICY IF EXISTS webhook_delivery_isolation ON webhook_delivery;
CREATE POLICY webhook_delivery_isolation ON webhook_delivery
    USING (
        organization_id = NULLIF(current_setting('app.org_id', true), '')::UUID
        OR current_setting('app.system', true) = 'on'
    )
    WITH CHECK (
        organization_id = NULLIF(current_setting('app.org_id', true), '')::UUID
        OR current_setting('app.system', true) = 'on'
    );

DROP INDEX IF EXISTS idx_webhook_delivery_retry;
CREATE INDEX idx_webhook_delivery_due
    ON webhook_delivery (next_retry_at)
    WHERE status IN ('pending', 'retrying');

CREATE INDEX IF NOT EXISTS idx_webhook_delivery_org_sub
    ON webhook_delivery (organization_id, subscription_id, created_at DESC);
