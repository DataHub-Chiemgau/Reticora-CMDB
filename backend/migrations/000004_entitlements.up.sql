-- Phase 0: Entitlement / licensing system

CREATE TABLE entitlement (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    feature_key TEXT NOT NULL,
    plan TEXT NOT NULL DEFAULT 'essential',
    enabled BOOLEAN NOT NULL DEFAULT true,
    limit_value BIGINT, -- NULL = unlimited
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, feature_key)
);

ALTER TABLE entitlement ENABLE ROW LEVEL SECURITY;

CREATE POLICY entitlement_isolation ON entitlement
    USING (organization_id = current_setting('app.organization_id')::UUID);

CREATE INDEX idx_entitlement_org ON entitlement(organization_id);
CREATE INDEX idx_entitlement_feature ON entitlement(feature_key);
