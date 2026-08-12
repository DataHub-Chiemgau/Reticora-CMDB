-- Migration 000037: persistent monitoring alert rules with evaluation state
--
-- Alert rules were previously held in memory only, so every restart dropped
-- them and no evaluation state survived. This table stores the rule plus the
-- pending/firing bookkeeping the evaluator needs to enforce durations
-- without flapping.

CREATE TABLE alert_rule (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    metric_name TEXT NOT NULL,
    condition TEXT NOT NULL CHECK (condition IN ('gt', 'lt', 'eq')),
    threshold DOUBLE PRECISION NOT NULL,
    duration INTERVAL NOT NULL DEFAULT INTERVAL '0 seconds',
    severity TEXT NOT NULL DEFAULT 'warning' CHECK (severity IN ('critical', 'warning', 'info')),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    -- pending_since marks when the condition first became true (duration
    -- tracking); last_fired_at suppresses re-notifications while the rule
    -- keeps firing.
    pending_since TIMESTAMPTZ,
    last_fired_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE alert_rule ENABLE ROW LEVEL SECURITY;

CREATE POLICY alert_rule_isolation ON alert_rule
    USING (
        organization_id = NULLIF(current_setting('app.org_id', true), '')::UUID
        OR current_setting('app.system', true) = 'on'
    )
    WITH CHECK (
        organization_id = NULLIF(current_setting('app.org_id', true), '')::UUID
        OR current_setting('app.system', true) = 'on'
    );

CREATE TRIGGER trg_alert_rule_updated_at BEFORE UPDATE ON alert_rule
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX idx_alert_rule_org ON alert_rule (organization_id, name);
CREATE INDEX idx_alert_rule_metric ON alert_rule (organization_id, metric_name) WHERE enabled;
