-- Stage 5: compliance rules and evaluation results.

INSERT INTO permission (key, resource, action, description) VALUES
    ('compliance:read', 'compliance', 'read', 'Read compliance rules, results and scores'),
    ('compliance:write', 'compliance', 'write', 'Manage compliance rules and run evaluations')
ON CONFLICT (key) DO UPDATE SET resource = EXCLUDED.resource, action = EXCLUDED.action, description = EXCLUDED.description, updated_at = now();

CREATE TABLE IF NOT EXISTS compliance_rule (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id  UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    ci_type_id       UUID REFERENCES ci_type(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    severity         TEXT NOT NULL,
    category         TEXT NOT NULL,
    expression       JSONB NOT NULL DEFAULT '{}'::jsonb,
    remediation_hint TEXT NOT NULL DEFAULT '',
    active           BOOLEAN NOT NULL DEFAULT true,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT compliance_rule_severity_check CHECK (severity IN ('low', 'medium', 'high', 'critical')),
    UNIQUE (organization_id, name)
);
CREATE INDEX IF NOT EXISTS idx_compliance_rule_org_active ON compliance_rule(organization_id, active);
CREATE INDEX IF NOT EXISTS idx_compliance_rule_type ON compliance_rule(organization_id, ci_type_id);
CREATE INDEX IF NOT EXISTS idx_compliance_rule_category ON compliance_rule(organization_id, category);

CREATE TABLE IF NOT EXISTS compliance_result (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    rule_id         UUID NOT NULL REFERENCES compliance_rule(id) ON DELETE CASCADE,
    ci_id           UUID NOT NULL REFERENCES ci(id) ON DELETE CASCADE,
    ci_type_id      UUID REFERENCES ci_type(id) ON DELETE SET NULL,
    status          TEXT NOT NULL,
    details         TEXT NOT NULL DEFAULT '',
    evaluated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT compliance_result_status_check CHECK (status IN ('pass', 'fail', 'not_applicable'))
);
CREATE INDEX IF NOT EXISTS idx_compliance_result_type ON compliance_result(organization_id, ci_type_id);
CREATE INDEX IF NOT EXISTS idx_compliance_result_status ON compliance_result(organization_id, status);
CREATE INDEX IF NOT EXISTS idx_compliance_result_ci ON compliance_result(organization_id, ci_id);

ALTER TABLE compliance_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE compliance_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY compliance_rule_tenant_isolation ON compliance_rule USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
ALTER TABLE compliance_result ENABLE ROW LEVEL SECURITY;
ALTER TABLE compliance_result FORCE ROW LEVEL SECURITY;
CREATE POLICY compliance_result_tenant_isolation ON compliance_result USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
