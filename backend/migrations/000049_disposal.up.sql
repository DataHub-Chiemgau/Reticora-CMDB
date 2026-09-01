-- Migration 000049: revision-safe disposal records (Entsorgungsdoku, BSI/ISO)
CREATE TABLE IF NOT EXISTS disposal_record (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    asset_id        UUID REFERENCES asset(id) ON DELETE SET NULL,
    ci_id           UUID REFERENCES ci(id) ON DELETE SET NULL,
    method          TEXT NOT NULL CHECK (method IN ('reuse','recycling','destruction','secure_erasure','physical_destruction','return_to_vendor')),
    certificate_ref TEXT,
    data_carrier    TEXT,
    performed_by    TEXT,
    performed_at    TIMESTAMPTZ NOT NULL,
    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE disposal_record ENABLE ROW LEVEL SECURITY;
ALTER TABLE disposal_record FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS disposal_record_isolation ON disposal_record;
CREATE POLICY disposal_record_isolation ON disposal_record
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE INDEX IF NOT EXISTS idx_disposal_record_org ON disposal_record(organization_id);

-- Revision-safe: no UPDATE/DELETE on disposal records (append-only).
CREATE OR REPLACE FUNCTION disposal_record_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'disposal_record is append-only (revision-safe)';
END;
$$;
DROP TRIGGER IF EXISTS disposal_record_no_update ON disposal_record;
CREATE TRIGGER disposal_record_no_update
    BEFORE UPDATE OR DELETE ON disposal_record
    FOR EACH ROW EXECUTE FUNCTION disposal_record_immutable();

INSERT INTO permission (key, resource, action, description) VALUES
    ('disposal:read', 'disposal', 'read', 'Read disposal records'),
    ('disposal:write', 'disposal', 'write', 'Create disposal records')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, p.key
FROM role r
CROSS JOIN (VALUES ('disposal:read'), ('disposal:write')) AS p(key)
WHERE r.name IN ('org_admin', 'engineer')
ON CONFLICT DO NOTHING;

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, 'disposal:read'
FROM role r
WHERE r.name IN ('viewer')
ON CONFLICT DO NOTHING;
