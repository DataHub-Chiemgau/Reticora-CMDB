-- Migration 000052: endpoint agents (registered endpoints with policy + kill-switch)
CREATE TABLE IF NOT EXISTS endpoint_agent (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    agent_id        TEXT NOT NULL,
    hostname        TEXT NOT NULL DEFAULT '',
    version         TEXT,
    os              TEXT,
    arch            TEXT,
    ci_id           UUID REFERENCES ci(id) ON DELETE SET NULL,
    status          TEXT NOT NULL DEFAULT 'online' CHECK (status IN ('online','offline','disabled')),
    last_heartbeat  TIMESTAMPTZ,
    policy          JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, agent_id)
);

ALTER TABLE endpoint_agent ENABLE ROW LEVEL SECURITY;
ALTER TABLE endpoint_agent FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS endpoint_agent_isolation ON endpoint_agent;
CREATE POLICY endpoint_agent_isolation ON endpoint_agent
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE INDEX IF NOT EXISTS idx_endpoint_agent_org ON endpoint_agent(organization_id);

INSERT INTO permission (key, resource, action, description) VALUES
    ('agent:read', 'agent', 'read', 'Read endpoint agents'),
    ('agent:manage', 'agent', 'manage', 'Manage endpoint agents and policies'),
    ('agent:ingest', 'agent', 'ingest', 'Ingest endpoint agent telemetry')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, p.key
FROM role r
CROSS JOIN (VALUES ('agent:read'), ('agent:manage')) AS p(key)
WHERE r.name IN ('org_admin', 'engineer')
ON CONFLICT DO NOTHING;

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, 'agent:read'
FROM role r
WHERE r.name IN ('viewer')
ON CONFLICT DO NOTHING;
