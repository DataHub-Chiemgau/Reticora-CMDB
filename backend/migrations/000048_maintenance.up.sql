-- Migration 000048: maintenance windows + customer notification
CREATE TABLE IF NOT EXISTS maintenance_window (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    title           TEXT NOT NULL,
    description     TEXT,
    starts_at       TIMESTAMPTZ NOT NULL,
    ends_at         TIMESTAMPTZ NOT NULL,
    status          TEXT NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled','in_progress','completed','cancelled')),
    created_by      UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT maintenance_window_range CHECK (ends_at > starts_at)
);

-- affected CIs (derived clients via ci.client_id at notify time)
CREATE TABLE IF NOT EXISTS maintenance_window_ci (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id      UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    maintenance_window_id UUID NOT NULL REFERENCES maintenance_window(id) ON DELETE CASCADE,
    ci_id                UUID NOT NULL REFERENCES ci(id) ON DELETE CASCADE,
    UNIQUE (maintenance_window_id, ci_id)
);

CREATE TABLE IF NOT EXISTS maintenance_notification (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    maintenance_window_id UUID NOT NULL REFERENCES maintenance_window(id) ON DELETE CASCADE,
    client_id       UUID REFERENCES client(id),
    channel         TEXT NOT NULL DEFAULT 'webhook',
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sent','failed')),
    sent_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE maintenance_window ENABLE ROW LEVEL SECURITY;
ALTER TABLE maintenance_window FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS maintenance_window_isolation ON maintenance_window;
CREATE POLICY maintenance_window_isolation ON maintenance_window
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

ALTER TABLE maintenance_window_ci ENABLE ROW LEVEL SECURITY;
ALTER TABLE maintenance_window_ci FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS maintenance_window_ci_isolation ON maintenance_window_ci;
CREATE POLICY maintenance_window_ci_isolation ON maintenance_window_ci
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

ALTER TABLE maintenance_notification ENABLE ROW LEVEL SECURITY;
ALTER TABLE maintenance_notification FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS maintenance_notification_isolation ON maintenance_notification;
CREATE POLICY maintenance_notification_isolation ON maintenance_notification
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE INDEX IF NOT EXISTS idx_maintenance_window_org ON maintenance_window(organization_id);
CREATE INDEX IF NOT EXISTS idx_maintenance_window_ci_win ON maintenance_window_ci(maintenance_window_id);
CREATE INDEX IF NOT EXISTS idx_maintenance_notification_win ON maintenance_notification(maintenance_window_id);

INSERT INTO permission (key, resource, action, description) VALUES
    ('maintenance:read', 'maintenance', 'read', 'Read maintenance windows'),
    ('maintenance:write', 'maintenance', 'write', 'Manage maintenance windows and notifications')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, p.key
FROM role r
CROSS JOIN (VALUES ('maintenance:read'), ('maintenance:write')) AS p(key)
WHERE r.name IN ('org_admin', 'engineer')
ON CONFLICT DO NOTHING;

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, 'maintenance:read'
FROM role r
WHERE r.name IN ('viewer', 'client_technician')
ON CONFLICT DO NOTHING;
