-- Stage 5: permission catalogue and SLA policies.

CREATE TABLE IF NOT EXISTS permission (
    key         TEXT PRIMARY KEY,
    resource    TEXT NOT NULL,
    action      TEXT NOT NULL,
    description TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (key = resource || ':' || action OR position(':' in key) > 0)
);

INSERT INTO permission (key, resource, action, description) VALUES
    ('ci:read', 'ci', 'read', 'Read configuration items'),
    ('ci:write', 'ci', 'write', 'Create and update configuration items'),
    ('ci:delete', 'ci', 'delete', 'Delete configuration items'),
    ('topology:read', 'topology', 'read', 'Read CI topology graphs'),
    ('discovery:read', 'discovery', 'read', 'Read collectors and discovery jobs'),
    ('discovery:write', 'discovery', 'write', 'Manage discovery jobs and imports'),
    ('asset:read', 'asset', 'read', 'Read assets'),
    ('asset:write', 'asset', 'write', 'Manage assets'),
    ('assignment:read', 'assignment', 'read', 'Read assignments'),
    ('assignment:write', 'assignment', 'write', 'Manage assignments'),
    ('document:read', 'document', 'read', 'Read documents'),
    ('document:write', 'document', 'write', 'Manage documents'),
    ('stocktake:read', 'stocktake', 'read', 'Read stocktakes'),
    ('stocktake:write', 'stocktake', 'write', 'Manage stocktakes'),
    ('ticket:read', 'ticket', 'read', 'Read tickets'),
    ('ticket:write', 'ticket', 'write', 'Manage tickets and comments'),
    ('user:read', 'user', 'read', 'Read users and teams'),
    ('user:write', 'user', 'write', 'Manage users and teams'),
    ('role:read', 'role', 'read', 'Read roles'),
    ('role:write', 'role', 'write', 'Manage roles'),
    ('permission:read', 'permission', 'read', 'Read permission catalogue and grants'),
    ('permission:write', 'permission', 'write', 'Replace role permission grants'),
    ('sla:read', 'sla', 'read', 'Read SLA policies and ticket state'),
    ('sla:write', 'sla', 'write', 'Manage SLA policies and ticket state'),
    ('webhook:read', 'webhook', 'read', 'Read webhooks'),
    ('webhook:write', 'webhook', 'write', 'Manage webhooks'),
    ('credential:read', 'credential', 'read', 'Read credential metadata'),
    ('credential:write', 'credential', 'write', 'Manage credentials'),
    ('audit:read', 'audit', 'read', 'Read audit trails'),
    ('ipam:read', 'ipam', 'read', 'Read IPAM data'),
    ('ipam:write', 'ipam', 'write', 'Manage IPAM data'),
    ('rack:read', 'rack', 'read', 'Read racks'),
    ('rack:write', 'rack', 'write', 'Manage racks'),
    ('contact:read', 'contact', 'read', 'Read contacts'),
    ('contact:write', 'contact', 'write', 'Manage contacts')
ON CONFLICT (key) DO UPDATE SET
    resource = EXCLUDED.resource,
    action = EXCLUDED.action,
    description = EXCLUDED.description,
    updated_at = now();

CREATE TABLE IF NOT EXISTS role_permission (
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    role_id         UUID NOT NULL REFERENCES role(id) ON DELETE CASCADE,
    permission_key  TEXT NOT NULL REFERENCES permission(key) ON DELETE CASCADE,
    granted_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    granted_by      UUID REFERENCES app_user(id) ON DELETE SET NULL,
    PRIMARY KEY (organization_id, role_id, permission_key)
);

CREATE INDEX IF NOT EXISTS idx_role_permission_role ON role_permission(organization_id, role_id);
CREATE INDEX IF NOT EXISTS idx_role_permission_permission ON role_permission(permission_key);

CREATE TABLE IF NOT EXISTS sla (
    id                        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id           UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id                 UUID REFERENCES client(id) ON DELETE SET NULL,
    name                      TEXT NOT NULL,
    priority                  TEXT NOT NULL,
    response_target_minutes   INTEGER NOT NULL CHECK (response_target_minutes > 0),
    resolution_target_minutes INTEGER NOT NULL CHECK (resolution_target_minutes > 0),
    business_calendar         BOOLEAN NOT NULL DEFAULT false,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT sla_priority_check CHECK (priority IN ('low', 'medium', 'high', 'critical')),
    UNIQUE (organization_id, client_id, priority, name)
);

CREATE INDEX IF NOT EXISTS idx_sla_org_priority ON sla(organization_id, priority);
CREATE INDEX IF NOT EXISTS idx_sla_client ON sla(organization_id, client_id);

CREATE TABLE IF NOT EXISTS ticket_sla (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id     UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    ticket_id           UUID NOT NULL REFERENCES ticket(id) ON DELETE CASCADE,
    sla_id              UUID NOT NULL REFERENCES sla(id) ON DELETE RESTRICT,
    response_due_at     TIMESTAMPTZ NOT NULL,
    resolution_due_at   TIMESTAMPTZ NOT NULL,
    first_response_at   TIMESTAMPTZ,
    resolved_at         TIMESTAMPTZ,
    response_breached   BOOLEAN NOT NULL DEFAULT false,
    resolution_breached BOOLEAN NOT NULL DEFAULT false,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, ticket_id)
);

CREATE INDEX IF NOT EXISTS idx_ticket_sla_ticket ON ticket_sla(organization_id, ticket_id);
CREATE INDEX IF NOT EXISTS idx_ticket_sla_policy ON ticket_sla(organization_id, sla_id);
CREATE INDEX IF NOT EXISTS idx_ticket_sla_due ON ticket_sla(organization_id, resolution_due_at);
CREATE INDEX IF NOT EXISTS idx_ticket_sla_breach ON ticket_sla(organization_id, response_breached, resolution_breached);

ALTER TABLE role_permission ENABLE ROW LEVEL SECURITY;
ALTER TABLE role_permission FORCE ROW LEVEL SECURITY;
CREATE POLICY role_permission_tenant_isolation ON role_permission
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

ALTER TABLE sla ENABLE ROW LEVEL SECURITY;
ALTER TABLE sla FORCE ROW LEVEL SECURITY;
CREATE POLICY sla_tenant_isolation ON sla
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

ALTER TABLE ticket_sla ENABLE ROW LEVEL SECURITY;
ALTER TABLE ticket_sla FORCE ROW LEVEL SECURITY;
CREATE POLICY ticket_sla_tenant_isolation ON ticket_sla
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
