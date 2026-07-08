-- Phase 1: Webhook subscriptions and delivery log

CREATE TABLE webhook_subscription (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    url TEXT NOT NULL,
    secret TEXT NOT NULL, -- HMAC-SHA256 signing key
    events TEXT[] NOT NULL DEFAULT '{}', -- e.g. {'ci.created', 'ci.updated', 'ci.deleted'}
    is_active BOOLEAN NOT NULL DEFAULT true,
    headers JSONB NOT NULL DEFAULT '{}', -- custom headers
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE webhook_delivery (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscription_id UUID NOT NULL REFERENCES webhook_subscription(id) ON DELETE CASCADE,
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    event TEXT NOT NULL,
    payload JSONB NOT NULL,
    response_status INTEGER,
    response_body TEXT,
    duration_ms INTEGER,
    attempt INTEGER NOT NULL DEFAULT 1,
    delivered_at TIMESTAMPTZ,
    next_retry_at TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'success', 'failed', 'retrying')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE webhook_subscription ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_delivery ENABLE ROW LEVEL SECURITY;

CREATE POLICY webhook_sub_isolation ON webhook_subscription
    USING (organization_id = current_setting('app.organization_id')::UUID);

CREATE POLICY webhook_delivery_isolation ON webhook_delivery
    USING (organization_id = current_setting('app.organization_id')::UUID);

CREATE INDEX idx_webhook_sub_org ON webhook_subscription(organization_id);
CREATE INDEX idx_webhook_delivery_sub ON webhook_delivery(subscription_id);
CREATE INDEX idx_webhook_delivery_status ON webhook_delivery(status) WHERE status != 'success';
CREATE INDEX idx_webhook_delivery_retry ON webhook_delivery(next_retry_at) WHERE status = 'retrying';
