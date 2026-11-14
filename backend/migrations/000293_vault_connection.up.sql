-- Migration 000293: vault connections (CRD-03, part of COL-03; WP-893,
-- E-220, E-221). A vault_connection names an OpenBao or HashiCorp Vault KV v2
-- engine (address, namespace, mount) and the stored credential it logs in
-- with (auth_method approle or token, auth_credential_id); resolve_at says
-- who reads the references: the collectors of vault_connection_collector
-- (default, E-220) or the server (WP-896). The table holds no secret: the
-- login is itself a stored credential of the new kinds vault_approle
-- (role_id, secret_id) and vault_token (token).
--
-- A connection of a client logs in with a credential of that client or an
-- org-wide one (vault_connection_auth_credential_rule, 23514) whose kind
-- fits auth_method. Its collectors belong to the client or are org-wide
-- (vault_connection_collector_client_rule); an org-wide connection may name
-- any collector of the organization. organization_id and client_id of a
-- collector row come from its connection (BEFORE trigger, no foreign key on
-- the derived client_id; Konvention 19).

-- 1. The credential kinds of the vault logins (pattern 000149).
ALTER TABLE credential DROP CONSTRAINT credential_kind_check;
ALTER TABLE credential ADD CONSTRAINT credential_kind_check
    CHECK (kind IN ('snmp_v2c', 'snmp_v3', 'ssh_password', 'ssh_key', 'redfish', 'ipmi', 'wmi', 'api_token', 'nut', 'nas',
                    'vault_approle', 'vault_token'));

-- 2. UNIQUE (id, organization_id) on the referenced tables (Konvention 19);
-- since migrations 000287/000288 they exist, the guards keep older states
-- working.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'credential'::regclass AND conname = 'credential_id_organization_key') THEN
        ALTER TABLE credential ADD CONSTRAINT credential_id_organization_key UNIQUE (id, organization_id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'collector'::regclass AND conname = 'collector_id_organization_key') THEN
        ALTER TABLE collector ADD CONSTRAINT collector_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;

CREATE TABLE vault_connection (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id    uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id          uuid NULL,
    name               text NOT NULL,
    kind               text NOT NULL CONSTRAINT vault_connection_kind_check
                           CHECK (kind IN ('openbao_kv2', 'hashicorp_kv2')),
    address            text NOT NULL,
    namespace          text NULL,
    mount              text NOT NULL,
    auth_method        text NOT NULL CONSTRAINT vault_connection_auth_method_check
                           CHECK (auth_method IN ('approle', 'token')),
    auth_credential_id uuid NOT NULL,
    ca_pem             text NULL,
    resolve_at         text NOT NULL DEFAULT 'collector' CONSTRAINT vault_connection_resolve_at_check
                           CHECK (resolve_at IN ('collector', 'server')),
    enabled            boolean NOT NULL DEFAULT true,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT vault_connection_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT vault_connection_name_key UNIQUE (organization_id, name),
    CONSTRAINT vault_connection_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id),
    CONSTRAINT vault_connection_auth_credential_fkey FOREIGN KEY (auth_credential_id, organization_id)
        REFERENCES credential (id, organization_id) ON DELETE RESTRICT
);

CREATE INDEX idx_vault_connection_auth_credential ON vault_connection (organization_id, auth_credential_id);
CREATE INDEX idx_vault_connection_client ON vault_connection (client_id, organization_id) WHERE client_id IS NOT NULL;

CREATE TABLE vault_connection_collector (
    organization_id     uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    vault_connection_id uuid NOT NULL,
    collector_id        uuid NOT NULL,
    client_id           uuid NULL,
    PRIMARY KEY (vault_connection_id, collector_id),
    CONSTRAINT vault_connection_collector_connection_fkey FOREIGN KEY (vault_connection_id, organization_id)
        REFERENCES vault_connection (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT vault_connection_collector_collector_fkey FOREIGN KEY (collector_id, organization_id)
        REFERENCES collector (id, organization_id)
);

CREATE INDEX idx_vault_connection_collector_collector ON vault_connection_collector (collector_id, organization_id);

-- The login of a connection: a credential of the connection's client or an
-- org-wide one, of the kind of auth_method. Read with the scope widened
-- (000141) so the rule does not depend on the writer's scope; the policies
-- check the row afterwards.
CREATE FUNCTION check_vault_connection_auth() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
    credential_client uuid;
    credential_kind text;
    wanted_kind text := 'vault_token';
BEGIN
    IF NEW.auth_method = 'approle' THEN
        wanted_kind := 'vault_approle';
    END IF;
    SELECT cr.client_id, cr.kind INTO credential_client, credential_kind
      FROM credential cr WHERE cr.id = NEW.auth_credential_id AND cr.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    IF credential_kind IS NULL THEN
        -- The foreign key reports the unknown credential.
        RETURN NEW;
    END IF;
    IF credential_kind IS DISTINCT FROM wanted_kind
       OR (credential_client IS NOT NULL AND credential_client IS DISTINCT FROM NEW.client_id) THEN
        RAISE EXCEPTION 'the login of a vault connection is a credential of its auth method, of its client or org-wide'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'vault_connection_auth_credential_rule';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_vault_connection_auth
    BEFORE INSERT OR UPDATE OF auth_credential_id, auth_method, client_id ON vault_connection
    FOR EACH ROW EXECUTE FUNCTION check_vault_connection_auth();
CREATE TRIGGER trg_vault_connection_updated_at
    BEFORE UPDATE ON vault_connection
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard
    BEFORE TRUNCATE ON vault_connection
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- organization_id and client_id of a collector row are those of its
-- connection; the collector belongs to the connection's client or is
-- org-wide (an org-wide connection takes any collector).
CREATE FUNCTION derive_vault_connection_collector() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
    connection_org uuid;
    connection_client uuid;
    collector_client uuid;
    collector_found boolean;
BEGIN
    SELECT v.organization_id, v.client_id INTO connection_org, connection_client
      FROM vault_connection v WHERE v.id = NEW.vault_connection_id;
    SELECT true, c.client_id INTO collector_found, collector_client
      FROM collector c WHERE c.id = NEW.collector_id AND c.organization_id = connection_org;
    PERFORM app_scope_restore(previous);
    NEW.organization_id := connection_org;
    NEW.client_id := connection_client;
    IF collector_found AND connection_client IS NOT NULL AND collector_client IS NOT NULL
       AND collector_client IS DISTINCT FROM connection_client THEN
        RAISE EXCEPTION 'a vault connection of a client is resolved only by collectors of that client or org-wide ones'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'vault_connection_collector_client_rule';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_vault_connection_collector_scope
    BEFORE INSERT OR UPDATE ON vault_connection_collector
    FOR EACH ROW EXECUTE FUNCTION derive_vault_connection_collector();
CREATE TRIGGER reticora_truncate_guard
    BEFORE TRUNCATE ON vault_connection_collector
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- A connection that changes its client re-derives its collector rows (a
-- no-op UPDATE re-runs the derivation).
CREATE FUNCTION propagate_vault_connection_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE vault_connection_collector SET collector_id = collector_id
     WHERE vault_connection_id = NEW.id AND organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_vault_connection_scope
    AFTER UPDATE OF client_id ON vault_connection
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION propagate_vault_connection_scope();

ALTER TABLE vault_connection ENABLE ROW LEVEL SECURITY;
ALTER TABLE vault_connection FORCE ROW LEVEL SECURITY;
ALTER TABLE vault_connection_collector ENABLE ROW LEVEL SECURITY;
ALTER TABLE vault_connection_collector FORCE ROW LEVEL SECURITY;

-- Konvention 5: a principal restricted to clients reads the connections of
-- its clients and the org-wide ones; it writes only connections of its
-- clients. A connection of a client is invisible to the other clients.
DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['vault_connection', 'vault_connection_collector'] LOOP
        EXECUTE format('CREATE POLICY %I ON %I USING (%s AND (%s OR client_id IS NULL OR %s)) WITH CHECK (%s AND (%s OR %s))',
            t || '_isolation', t, org_match, scope_all, scope_match, org_match, scope_all, scope_match);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR UPDATE USING (%s AND (%s OR %s)) WITH CHECK (%s AND (%s OR %s))',
            t || '_scoped_update', t, org_match, scope_all, scope_match, org_match, scope_all, scope_match);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR DELETE USING (%s AND (%s OR %s))',
            t || '_scoped_delete', t, org_match, scope_all, scope_match);
    END LOOP;
END
$$;
