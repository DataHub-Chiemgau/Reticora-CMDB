-- API keys and user invitations
-- Corresponds to spec Migration 0007 additions

CREATE TABLE api_key (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    key_hash TEXT NOT NULL,
    key_prefix TEXT NOT NULL,
    permissions TEXT[] NOT NULL DEFAULT '{}',
    created_by UUID NOT NULL,
    expires_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE user_invitation (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    email TEXT NOT NULL,
    role_id UUID NOT NULL REFERENCES role(id) ON DELETE CASCADE,
    scope_type TEXT NOT NULL DEFAULT 'org',
    scope_id UUID,
    invited_by UUID NOT NULL,
    token_hash TEXT NOT NULL,
    accepted_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_invitation_scope CHECK (scope_type IN ('org', 'client', 'site'))
);

-- RLS
ALTER TABLE api_key ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_key FORCE ROW LEVEL SECURITY;
ALTER TABLE user_invitation ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_invitation FORCE ROW LEVEL SECURITY;

CREATE POLICY org_isolation ON api_key
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE POLICY org_isolation ON user_invitation
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

-- Indexes
CREATE INDEX idx_api_key_org ON api_key(organization_id);
CREATE INDEX idx_api_key_prefix ON api_key(key_prefix);
CREATE INDEX idx_user_invitation_org ON user_invitation(organization_id);
CREATE INDEX idx_user_invitation_email ON user_invitation(organization_id, email);
