-- Phase 2: Extended Users, Teams, Permissions & Custom Roles
-- Extends existing app_user/role tables from migration 005 with
-- team membership, custom roles, and granular permissions.

-- Teams table (if not exists from earlier migration)
CREATE TABLE IF NOT EXISTS team (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id),
    name            TEXT NOT NULL,
    description     TEXT,
    lead_id         UUID REFERENCES app_user(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_team_name_org ON team(organization_id, name);

-- Team membership
CREATE TABLE IF NOT EXISTS team_member (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id         UUID NOT NULL REFERENCES team(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    role_in_team    TEXT NOT NULL DEFAULT 'member',
    joined_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT team_member_role_check CHECK (role_in_team IN ('member', 'lead', 'admin')),
    UNIQUE(team_id, user_id)
);

-- Custom roles with granular permissions
CREATE TABLE IF NOT EXISTS custom_role (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id),
    name            TEXT NOT NULL,
    description     TEXT,
    is_system       BOOLEAN NOT NULL DEFAULT FALSE,
    permissions     JSONB NOT NULL DEFAULT '[]',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_custom_role_name_org ON custom_role(organization_id, name);

-- User-to-custom-role assignment
CREATE TABLE IF NOT EXISTS user_custom_role (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    custom_role_id  UUID NOT NULL REFERENCES custom_role(id) ON DELETE CASCADE,
    scope_type      TEXT NOT NULL DEFAULT 'organization',
    scope_id        UUID,
    granted_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    granted_by      UUID REFERENCES app_user(id),
    CONSTRAINT scope_type_check CHECK (scope_type IN (
        'organization', 'client', 'site'
    )),
    UNIQUE(user_id, custom_role_id, scope_type, scope_id)
);

CREATE INDEX idx_team_org ON team(organization_id);
CREATE INDEX idx_team_member_team ON team_member(team_id);
CREATE INDEX idx_team_member_user ON team_member(user_id);
CREATE INDEX idx_custom_role_org ON custom_role(organization_id);
CREATE INDEX idx_user_custom_role_user ON user_custom_role(user_id);

ALTER TABLE team ENABLE ROW LEVEL SECURITY;
CREATE POLICY team_tenant_isolation ON team
    USING (organization_id = current_setting('app.current_org')::UUID);

ALTER TABLE custom_role ENABLE ROW LEVEL SECURITY;
CREATE POLICY custom_role_tenant_isolation ON custom_role
    USING (organization_id = current_setting('app.current_org')::UUID);
