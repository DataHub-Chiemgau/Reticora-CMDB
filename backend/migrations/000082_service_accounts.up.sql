-- Migration 000082: service accounts (RBA-08, WP-069).
--
-- A service account is a non-human principal of an organization with role
-- assignments like a user (scope per assignment, client and site). API keys
-- and webhook subscriptions can be bound to one: a bound key acts with the
-- intersection of its permissions and the account's rights, a bound
-- subscription receives only events whose object the account may read.
-- A bound account cannot be deleted while keys or subscriptions refer to it.

CREATE TABLE service_account (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    is_active       BOOLEAN NOT NULL DEFAULT true,
    created_by      UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT service_account_org_name_key UNIQUE (organization_id, name),
    CONSTRAINT service_account_id_organization_key UNIQUE (id, organization_id)
);

ALTER TABLE service_account ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_account FORCE ROW LEVEL SECURITY;
CREATE POLICY service_account_isolation ON service_account
    USING (organization_id = current_setting('app.org_id')::uuid)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid);

CREATE TRIGGER trg_service_account_updated_at BEFORE UPDATE ON service_account
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE service_account_role (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id    UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    service_account_id UUID NOT NULL,
    role_id            UUID NOT NULL REFERENCES role(id) ON DELETE CASCADE,
    scope_client_id    UUID REFERENCES client(id),
    scope_site_id      UUID REFERENCES site(id),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT service_account_role_account_fkey FOREIGN KEY (service_account_id, organization_id)
        REFERENCES service_account (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT service_account_role_unique UNIQUE NULLS NOT DISTINCT (service_account_id, role_id, scope_client_id, scope_site_id)
);
CREATE INDEX idx_service_account_role_account ON service_account_role (organization_id, service_account_id);

ALTER TABLE service_account_role ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_account_role FORCE ROW LEVEL SECURITY;
CREATE POLICY service_account_role_isolation ON service_account_role
    USING (organization_id = current_setting('app.org_id')::uuid)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid);

ALTER TABLE api_key ADD COLUMN service_account_id UUID;
ALTER TABLE api_key ADD CONSTRAINT api_key_service_account_fkey FOREIGN KEY (service_account_id, organization_id)
    REFERENCES service_account (id, organization_id);

ALTER TABLE webhook_subscription ADD COLUMN service_account_id UUID;
ALTER TABLE webhook_subscription ADD CONSTRAINT webhook_subscription_service_account_fkey FOREIGN KEY (service_account_id, organization_id)
    REFERENCES service_account (id, organization_id);
