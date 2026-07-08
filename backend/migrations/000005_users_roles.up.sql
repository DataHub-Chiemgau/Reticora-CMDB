-- Phase 0: Users, roles and RBAC

CREATE TABLE app_user (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    oidc_subject TEXT UNIQUE,
    email TEXT NOT NULL,
    display_name TEXT NOT NULL,
    avatar_url TEXT,
    locale TEXT NOT NULL DEFAULT 'de',
    is_active BOOLEAN NOT NULL DEFAULT true,
    last_login TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE role (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    description TEXT,
    permissions JSONB NOT NULL DEFAULT '[]',
    is_builtin BOOLEAN NOT NULL DEFAULT false,
    scope TEXT NOT NULL DEFAULT 'org' CHECK (scope IN ('org', 'client', 'site')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

CREATE TABLE role_assignment (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    role_id UUID NOT NULL REFERENCES role(id) ON DELETE CASCADE,
    scope_client_id UUID REFERENCES client(id),
    scope_site_id UUID REFERENCES site(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, role_id, scope_client_id, scope_site_id)
);

-- RLS
ALTER TABLE app_user ENABLE ROW LEVEL SECURITY;
ALTER TABLE role ENABLE ROW LEVEL SECURITY;
ALTER TABLE role_assignment ENABLE ROW LEVEL SECURITY;

CREATE POLICY user_isolation ON app_user
    USING (organization_id = current_setting('app.organization_id')::UUID);

CREATE POLICY role_isolation ON role
    USING (organization_id = current_setting('app.organization_id')::UUID);

CREATE POLICY role_assignment_isolation ON role_assignment
    USING (organization_id = current_setting('app.organization_id')::UUID);

CREATE INDEX idx_user_org ON app_user(organization_id);
CREATE INDEX idx_user_email ON app_user(email);
CREATE INDEX idx_role_org ON role(organization_id);
CREATE INDEX idx_role_assignment_user ON role_assignment(user_id);
