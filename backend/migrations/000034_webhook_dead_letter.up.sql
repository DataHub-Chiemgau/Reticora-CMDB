-- Migration 000034: webhook dead-letter queue
--
-- Deliveries that exhaust their retry budget were only marked 'failed' on
-- webhook_delivery, which mixed them with transient history and made them
-- hard to surface. A dedicated dead-letter table now holds every exhausted
-- delivery so operators can inspect and replay them. The delivery status set
-- gains 'dead' for deliveries that were moved to the dead-letter queue.

ALTER TABLE webhook_delivery DROP CONSTRAINT IF EXISTS webhook_delivery_status_check;
ALTER TABLE webhook_delivery
    ADD CONSTRAINT webhook_delivery_status_check
    CHECK (status IN ('pending', 'success', 'failed', 'retrying', 'dead'));

CREATE TABLE webhook_dead_letter (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    delivery_id UUID NOT NULL REFERENCES webhook_delivery(id) ON DELETE CASCADE,
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    subscription_id UUID NOT NULL REFERENCES webhook_subscription(id) ON DELETE CASCADE,
    event TEXT NOT NULL,
    payload JSONB NOT NULL,
    attempts INTEGER NOT NULL,
    last_status_code INTEGER,
    last_error TEXT,
    first_attempt_at TIMESTAMPTZ,
    dead_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE webhook_dead_letter ENABLE ROW LEVEL SECURITY;

-- Dead letters are read through the tenant API (organization scope) and
-- written by the dispatcher, which runs with the app.system flag.
CREATE POLICY webhook_dead_letter_isolation ON webhook_dead_letter
    USING (
        organization_id = NULLIF(current_setting('app.org_id', true), '')::UUID
        OR current_setting('app.system', true) = 'on'
    )
    WITH CHECK (
        organization_id = NULLIF(current_setting('app.org_id', true), '')::UUID
        OR current_setting('app.system', true) = 'on'
    );

CREATE INDEX idx_webhook_dead_letter_org
    ON webhook_dead_letter (organization_id, dead_at DESC);
CREATE UNIQUE INDEX idx_webhook_dead_letter_delivery
    ON webhook_dead_letter (delivery_id);
