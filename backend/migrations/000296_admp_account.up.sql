-- Migration 000296: admp_account holds the ADMP accounts of an organization
-- or of one of its clients (ADM-01, WP-925, E-222). An account stores no
-- secret: its login is a credential of kind api_token (stored or vault_ref
-- with resolve_at server), referenced together with organization_id. When
-- the credential is deleted (or the reference removed) the reference
-- becomes NULL and the account reports last_error_class
-- credential_missing until a credential is attached again. last_ok_at and
-- last_error_class are written by the refresh jobs (WP-929).
--
-- At most one enabled account per client and one enabled organization-wide
-- account (client_id NULL) per organization. The cache tables of WP-928
-- reference the account with (id, organization_id).

-- UNIQUE (id, organization_id) on credential (Konvention 19); since
-- migration 000287 it exists, the guard keeps older states working.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'credential'::regclass AND conname = 'credential_id_organization_key') THEN
        ALTER TABLE credential ADD CONSTRAINT credential_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;

CREATE TABLE admp_account (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id  uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id        uuid NULL,
    name             text NOT NULL CONSTRAINT admp_account_name_check CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    base_url         text NOT NULL,
    auth_url         text NULL,
    credential_id    uuid NULL,
    rate_limit_rps   numeric NOT NULL DEFAULT 2 CONSTRAINT admp_account_rate_limit_check
                         CHECK (rate_limit_rps > 0 AND rate_limit_rps <= 50),
    timeout_ms       integer NOT NULL DEFAULT 10000 CONSTRAINT admp_account_timeout_check
                         CHECK (timeout_ms BETWEEN 1000 AND 60000),
    enabled          boolean NOT NULL DEFAULT true,
    last_ok_at       timestamptz NULL,
    last_error_class text NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT admp_account_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT admp_account_name_key UNIQUE (organization_id, name),
    CONSTRAINT admp_account_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT admp_account_credential_fkey FOREIGN KEY (credential_id, organization_id)
        REFERENCES credential (id, organization_id) ON DELETE SET NULL (credential_id)
);

CREATE UNIQUE INDEX uq_admp_account_client_active ON admp_account (organization_id, client_id)
    WHERE enabled AND client_id IS NOT NULL;
CREATE UNIQUE INDEX uq_admp_account_org_active ON admp_account (organization_id)
    WHERE enabled AND client_id IS NULL;
CREATE INDEX idx_admp_account_credential ON admp_account (organization_id, credential_id) WHERE credential_id IS NOT NULL;
CREATE INDEX idx_admp_account_client ON admp_account (client_id, organization_id) WHERE client_id IS NOT NULL;

-- A credential that goes away (ON DELETE SET NULL, or a reference
-- removed) leaves the account without login: it reports
-- credential_missing. Attaching a credential again clears that mark; the
-- other error classes stay for the refresh (WP-929).
CREATE FUNCTION admp_account_credential_missing() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF OLD.credential_id IS NOT NULL AND NEW.credential_id IS NULL THEN
        NEW.last_error_class := 'credential_missing';
    ELSIF NEW.credential_id IS NOT NULL AND OLD.last_error_class = 'credential_missing' THEN
        NEW.last_error_class := NULL;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_admp_account_credential_missing
    BEFORE UPDATE OF credential_id ON admp_account
    FOR EACH ROW EXECUTE FUNCTION admp_account_credential_missing();
CREATE TRIGGER trg_admp_account_updated_at
    BEFORE UPDATE ON admp_account
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard
    BEFORE TRUNCATE ON admp_account
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE admp_account ENABLE ROW LEVEL SECURITY;
ALTER TABLE admp_account FORCE ROW LEVEL SECURITY;

-- E-09 and AK6 of WP-925: a principal restricted to clients reads and
-- writes only the accounts of its clients; the organization-wide accounts
-- (configuration of the organization: URLs, the org credential) are only
-- for organization-wide principals. An account of a client is invisible to
-- the other clients. The fallback of admp.AccountFor to the
-- organization-wide account reads in an organization-wide scope.
DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
BEGIN
    EXECUTE format('CREATE POLICY admp_account_isolation ON admp_account USING (%s AND (%s OR %s)) WITH CHECK (%s AND (%s OR %s))',
        org_match, scope_all, scope_match, org_match, scope_all, scope_match);
    EXECUTE format('CREATE POLICY admp_account_scoped_update ON admp_account AS RESTRICTIVE FOR UPDATE USING (%s AND (%s OR %s)) WITH CHECK (%s AND (%s OR %s))',
        org_match, scope_all, scope_match, org_match, scope_all, scope_match);
    EXECUTE format('CREATE POLICY admp_account_scoped_delete ON admp_account AS RESTRICTIVE FOR DELETE USING (%s AND (%s OR %s))',
        org_match, scope_all, scope_match);
END
$$;
