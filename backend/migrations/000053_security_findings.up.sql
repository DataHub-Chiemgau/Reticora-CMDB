-- Migration 000053: security findings (patch posture / vulnerability per CI)
--
-- Findings are produced by matching discovered/agent-reported software and
-- firmware versions against version/CVE feeds. They feed tickets, the
-- compliance score and the security report (spec §9.4).
CREATE TABLE IF NOT EXISTS security_finding (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    ci_id           UUID REFERENCES ci(id) ON DELETE CASCADE,
    kind            TEXT NOT NULL CHECK (kind IN ('vulnerability','outdated_software','outdated_firmware','missing_patch')),
    severity        TEXT NOT NULL DEFAULT 'medium' CHECK (severity IN ('low','medium','high','critical')),
    title           TEXT NOT NULL,
    detail          TEXT,
    package_name    TEXT,
    installed_version TEXT,
    fixed_version   TEXT,
    reference       TEXT,
    status          TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','acknowledged','resolved','false_positive')),
    detected_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE security_finding ENABLE ROW LEVEL SECURITY;
ALTER TABLE security_finding FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS security_finding_isolation ON security_finding;
CREATE POLICY security_finding_isolation ON security_finding
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE INDEX IF NOT EXISTS idx_security_finding_org ON security_finding(organization_id);
CREATE INDEX IF NOT EXISTS idx_security_finding_ci ON security_finding(ci_id);
CREATE INDEX IF NOT EXISTS idx_security_finding_status ON security_finding(organization_id, status);

INSERT INTO permission (key, resource, action, description) VALUES
    ('security:read', 'security', 'read', 'Read security findings'),
    ('security:write', 'security', 'write', 'Manage security findings')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, p.key
FROM role r
CROSS JOIN (VALUES ('security:read'), ('security:write')) AS p(key)
WHERE r.name IN ('org_admin', 'engineer')
ON CONFLICT DO NOTHING;

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, 'security:read'
FROM role r
WHERE r.name IN ('viewer')
ON CONFLICT DO NOTHING;
