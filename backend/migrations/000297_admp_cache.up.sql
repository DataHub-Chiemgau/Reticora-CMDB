-- Migration 000297: the read-only cache of ADMP (ADM-03, ADM-04, ADM-07;
-- WP-928, E-222, E-243): connections (admp_connection), services
-- (admp_service) and the assignment of a service to device ports
-- (admp_service_port), each with fetched_at, expires_at and stale. Fields
-- the ADMP documentation defines later (A-55) live in properties; personal
-- values only as pii_subject surrogates (Konvention 16): the holder
-- (holder_subject_id) and the address of a private connection
-- (address_subject_id); a business address is plain text (address_text).
--
-- Scope (Konvention 5, no EXISTS policies):
--   * admp_connection and admp_service take client_id from their account;
--     for an organization-wide account it is derived from the linked ports:
--     when every port with a client lies at the same client, that client,
--     else NULL (shared row, E-09).
--   * admp_service_port takes client_id and site_id from its CI
--     (located-scope over its own columns, pattern 000061), the CI from its
--     interface; without CI the client of the account.
-- Deleting an account cascades through the cache; deleting a connection
-- and replacing its holder or address deletes the pii_subject rows
-- (crypto-shredding). A connection takes only subjects created in the same
-- transaction and used by no other connection, so shredding never hits a
-- subject of a contact or of another object. A service references only a
-- connection of its own account. The derived scope columns have no foreign
-- key (Konvention 19).

-- UNIQUE (id, organization_id) on network_interface and pii_subject
-- (Konvention 19); since migration 000287 they exist, the guards keep older
-- states working.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'network_interface'::regclass
                   AND conname = 'network_interface_id_organization_key') THEN
        ALTER TABLE network_interface ADD CONSTRAINT network_interface_id_organization_key UNIQUE (id, organization_id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'pii_subject'::regclass
                   AND conname IN ('pii_subject_surrogate_id_organization_key', 'pii_subject_id_organization_key')) THEN
        ALTER TABLE pii_subject ADD CONSTRAINT pii_subject_id_organization_key UNIQUE (surrogate_id, organization_id);
    END IF;
END
$$;

CREATE TABLE admp_connection (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id    uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    account_id         uuid NOT NULL,
    external_id        text NOT NULL,
    carrier            text NULL,
    contract_id        text NULL,
    address_subject_id uuid NULL,
    address_text       text NULL,
    holder_subject_id  uuid NULL,
    properties         jsonb NOT NULL DEFAULT '{}',
    fetched_at         timestamptz NOT NULL DEFAULT now(),
    expires_at         timestamptz NOT NULL,
    stale              boolean NOT NULL DEFAULT false,
    last_error_class   text NULL,
    client_id          uuid NULL,
    CONSTRAINT admp_connection_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT admp_connection_id_account_key UNIQUE (id, account_id, organization_id),
    CONSTRAINT admp_connection_external_key UNIQUE (account_id, external_id),
    CONSTRAINT admp_connection_address_check CHECK (address_subject_id IS NULL OR address_text IS NULL),
    CONSTRAINT admp_connection_properties_check CHECK (jsonb_typeof(properties) = 'object'),
    CONSTRAINT admp_connection_account_fkey FOREIGN KEY (account_id, organization_id)
        REFERENCES admp_account (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT admp_connection_holder_fkey FOREIGN KEY (holder_subject_id, organization_id)
        REFERENCES pii_subject (surrogate_id, organization_id) ON DELETE SET NULL (holder_subject_id),
    CONSTRAINT admp_connection_address_fkey FOREIGN KEY (address_subject_id, organization_id)
        REFERENCES pii_subject (surrogate_id, organization_id) ON DELETE SET NULL (address_subject_id)
);

CREATE TABLE admp_service (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id  uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    account_id       uuid NOT NULL,
    external_id      text NOT NULL,
    connection_id    uuid NULL,
    kind             text NOT NULL,
    properties       jsonb NOT NULL DEFAULT '{}',
    fetched_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    stale            boolean NOT NULL DEFAULT false,
    last_error_class text NULL,
    client_id        uuid NULL,
    CONSTRAINT admp_service_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT admp_service_external_key UNIQUE (account_id, external_id),
    CONSTRAINT admp_service_properties_check CHECK (jsonb_typeof(properties) = 'object'),
    CONSTRAINT admp_service_account_fkey FOREIGN KEY (account_id, organization_id)
        REFERENCES admp_account (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT admp_service_connection_fkey FOREIGN KEY (connection_id, account_id, organization_id)
        REFERENCES admp_connection (id, account_id, organization_id) ON DELETE SET NULL (connection_id)
);

CREATE TABLE admp_service_port (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id      uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    service_id           uuid NOT NULL,
    port_key             text NOT NULL,
    network_interface_id uuid NULL,
    ci_id                uuid NULL,
    matched_by           text NOT NULL DEFAULT 'none' CONSTRAINT admp_service_port_matched_by_check
                             CHECK (matched_by IN ('rule', 'manual', 'none')),
    client_id            uuid NULL,
    site_id              uuid NULL,
    CONSTRAINT admp_service_port_key UNIQUE (service_id, port_key),
    CONSTRAINT admp_service_port_service_fkey FOREIGN KEY (service_id, organization_id)
        REFERENCES admp_service (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT admp_service_port_interface_fkey FOREIGN KEY (network_interface_id, organization_id)
        REFERENCES network_interface (id, organization_id) ON DELETE SET NULL (network_interface_id),
    CONSTRAINT admp_service_port_ci_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE SET NULL (ci_id)
);

CREATE INDEX idx_admp_connection_expires ON admp_connection (organization_id, expires_at);
CREATE INDEX idx_admp_connection_holder ON admp_connection (holder_subject_id, organization_id) WHERE holder_subject_id IS NOT NULL;
CREATE INDEX idx_admp_connection_address ON admp_connection (address_subject_id, organization_id) WHERE address_subject_id IS NOT NULL;
CREATE INDEX idx_admp_service_expires ON admp_service (organization_id, expires_at);
CREATE INDEX idx_admp_service_connection ON admp_service (connection_id, account_id, organization_id) WHERE connection_id IS NOT NULL;
CREATE INDEX idx_admp_service_port_interface ON admp_service_port (network_interface_id) WHERE network_interface_id IS NOT NULL;
CREATE INDEX idx_admp_service_port_ci ON admp_service_port (ci_id) WHERE ci_id IS NOT NULL;

-- client_id of a connection or service: the client of its account; for an
-- organization-wide account a new row starts without client and keeps the
-- one admp_cache_derive_client sets from the ports.
CREATE FUNCTION admp_cache_account_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
    account_client uuid;
BEGIN
    SELECT a.client_id INTO account_client
      FROM admp_account a WHERE a.id = NEW.account_id AND a.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    IF account_client IS NOT NULL THEN
        NEW.client_id := account_client;
    ELSIF TG_OP = 'INSERT' THEN
        NEW.client_id := NULL;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_admp_connection_scope
    BEFORE INSERT OR UPDATE ON admp_connection
    FOR EACH ROW EXECUTE FUNCTION admp_cache_account_scope();
CREATE TRIGGER trg_admp_service_scope
    BEFORE INSERT OR UPDATE ON admp_service
    FOR EACH ROW EXECUTE FUNCTION admp_cache_account_scope();

-- A port takes its CI from the interface, client and site from the CI,
-- without CI the client of the account. An unlinked interface (deleted:
-- ON DELETE SET NULL) leaves the port unmatched.
CREATE FUNCTION admp_service_port_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.network_interface_id IS NOT NULL AND NEW.network_interface_id IS NULL THEN
        NEW.matched_by := 'none';
    END IF;
    IF NEW.network_interface_id IS NOT NULL THEN
        SELECT ni.ci_id INTO NEW.ci_id
          FROM network_interface ni WHERE ni.id = NEW.network_interface_id AND ni.organization_id = NEW.organization_id;
    END IF;
    IF NEW.ci_id IS NOT NULL THEN
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
          FROM ci c WHERE c.id = NEW.ci_id AND c.organization_id = NEW.organization_id;
    ELSE
        SELECT a.client_id, NULL INTO NEW.client_id, NEW.site_id
          FROM admp_service s JOIN admp_account a ON a.id = s.account_id AND a.organization_id = s.organization_id
         WHERE s.id = NEW.service_id AND s.organization_id = NEW.organization_id;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_admp_service_port_scope
    BEFORE INSERT OR UPDATE ON admp_service_port
    FOR EACH ROW EXECUTE FUNCTION admp_service_port_scope();

-- admp_cache_refresh_client derives the client of a service of an
-- organization-wide account and of its connection from the ports: one
-- client among the ports with a client, else none.
CREATE FUNCTION admp_cache_refresh_client(org uuid, service uuid) RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
    org_account boolean;
    conn uuid;
    derived uuid;
BEGIN
    SELECT a.client_id IS NULL, s.connection_id INTO org_account, conn
      FROM admp_service s JOIN admp_account a ON a.id = s.account_id AND a.organization_id = s.organization_id
     WHERE s.id = service AND s.organization_id = org;
    IF org_account THEN
        -- The lock serializes the derivations of the service; the count
        -- below runs with a new snapshot (READ COMMITTED) and sees the
        -- ports another transaction committed meanwhile.
        PERFORM 1 FROM admp_service WHERE id = service AND organization_id = org FOR UPDATE;
        SELECT CASE WHEN count(DISTINCT p.client_id) = 1 THEN (array_agg(p.client_id))[1] END INTO derived
          FROM admp_service_port p WHERE p.service_id = service AND p.organization_id = org AND p.client_id IS NOT NULL;
        UPDATE admp_service SET client_id = derived
         WHERE id = service AND organization_id = org AND client_id IS DISTINCT FROM derived;
        IF conn IS NOT NULL THEN
            PERFORM admp_cache_refresh_connection_client(org, conn);
        END IF;
    END IF;
    PERFORM app_scope_restore(previous);
END
$$;

CREATE FUNCTION admp_cache_refresh_connection_client(org uuid, conn uuid) RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
    org_account boolean;
    derived uuid;
BEGIN
    SELECT a.client_id IS NULL INTO org_account
      FROM admp_connection c JOIN admp_account a ON a.id = c.account_id AND a.organization_id = c.organization_id
     WHERE c.id = conn AND c.organization_id = org;
    IF org_account THEN
        -- The lock serializes the derivations of the connection: two
        -- transactions changing the ports of two of its services do not
        -- overwrite each other's result (lost update); the count below runs
        -- with a new snapshot after the lock (READ COMMITTED).
        PERFORM 1 FROM admp_connection WHERE id = conn AND organization_id = org FOR UPDATE;
        SELECT CASE WHEN count(DISTINCT p.client_id) = 1 THEN (array_agg(p.client_id))[1] END INTO derived
          FROM admp_service s JOIN admp_service_port p ON p.service_id = s.id AND p.organization_id = s.organization_id
         WHERE s.connection_id = conn AND s.organization_id = org AND p.client_id IS NOT NULL;
        UPDATE admp_connection SET client_id = derived
         WHERE id = conn AND organization_id = org AND client_id IS DISTINCT FROM derived;
    END IF;
    PERFORM app_scope_restore(previous);
END
$$;

CREATE FUNCTION admp_service_port_derive() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        PERFORM admp_cache_refresh_client(OLD.organization_id, OLD.service_id);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') AND (TG_OP = 'INSERT' OR NEW.service_id IS DISTINCT FROM OLD.service_id
                                         OR NEW.client_id IS DISTINCT FROM OLD.client_id) THEN
        PERFORM admp_cache_refresh_client(NEW.organization_id, NEW.service_id);
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_admp_service_port_derive
    AFTER INSERT OR UPDATE OR DELETE ON admp_service_port
    FOR EACH ROW EXECUTE FUNCTION admp_service_port_derive();

-- A service that moves to another connection re-derives both connections.
CREATE FUNCTION admp_service_connection_moved() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF OLD.connection_id IS NOT NULL THEN
        PERFORM admp_cache_refresh_connection_client(OLD.organization_id, OLD.connection_id);
    END IF;
    IF NEW.connection_id IS NOT NULL THEN
        PERFORM admp_cache_refresh_connection_client(NEW.organization_id, NEW.connection_id);
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_admp_service_connection_moved
    AFTER UPDATE OF connection_id ON admp_service
    FOR EACH ROW WHEN (OLD.connection_id IS DISTINCT FROM NEW.connection_id)
    EXECUTE FUNCTION admp_service_connection_moved();

-- A deleted service (expiry, or directly) re-derives its connection: its
-- ports no longer count. The cascade of its ports cannot do it, the
-- service row is gone when their trigger runs.
CREATE FUNCTION admp_service_deleted() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF OLD.connection_id IS NOT NULL THEN
        PERFORM admp_cache_refresh_connection_client(OLD.organization_id, OLD.connection_id);
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_admp_service_deleted
    AFTER DELETE ON admp_service
    FOR EACH ROW EXECUTE FUNCTION admp_service_deleted();

-- Deleting a connection deletes its personal values (crypto-shredding,
-- E-243): the holder and the address of a private connection.
CREATE FUNCTION admp_connection_shred() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF OLD.holder_subject_id IS NOT NULL OR OLD.address_subject_id IS NOT NULL THEN
        DELETE FROM pii_subject
         WHERE organization_id = OLD.organization_id
           AND surrogate_id IN (OLD.holder_subject_id, OLD.address_subject_id);
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_admp_connection_shred
    AFTER DELETE ON admp_connection
    FOR EACH ROW EXECUTE FUNCTION admp_connection_shred();

-- A replaced holder or address is shredded with the statement that
-- replaces it, also when a concurrent upsert of the same connection won
-- the insert (E-243, Konvention 16).
CREATE FUNCTION admp_connection_shred_replaced() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    DELETE FROM pii_subject
     WHERE organization_id = OLD.organization_id
       AND surrogate_id IN (OLD.holder_subject_id, OLD.address_subject_id)
       AND surrogate_id IS DISTINCT FROM NEW.holder_subject_id
       AND surrogate_id IS DISTINCT FROM NEW.address_subject_id;
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_admp_connection_shred_replaced
    AFTER UPDATE OF holder_subject_id, address_subject_id ON admp_connection
    FOR EACH ROW WHEN (OLD.holder_subject_id IS DISTINCT FROM NEW.holder_subject_id
                       OR OLD.address_subject_id IS DISTINCT FROM NEW.address_subject_id)
    EXECUTE FUNCTION admp_connection_shred_replaced();

-- Only a subject created for the connection may be linked to it: created
-- in the same transaction (the refresh stores the personal value and the
-- connection together) and linked to no other connection. A subject of a
-- contact, of another object or of another connection is refused
-- (check_violation admp_connection_subject_check), so shredding the
-- connection's subjects never deletes foreign personal data. The check runs
-- after the row is written: an upsert that keeps its subjects passes.
CREATE FUNCTION admp_connection_subject_check() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    subject uuid;
BEGIN
    FOREACH subject IN ARRAY ARRAY[NEW.holder_subject_id, NEW.address_subject_id] LOOP
        CONTINUE WHEN subject IS NULL;
        CONTINUE WHEN TG_OP = 'UPDATE' AND subject IN (OLD.holder_subject_id, OLD.address_subject_id);
        previous := app_scope_widen();
        IF NOT EXISTS (SELECT 1 FROM pii_subject p
                        WHERE p.surrogate_id = subject AND p.organization_id = NEW.organization_id AND p.created_at = now())
           OR EXISTS (SELECT 1 FROM admp_connection c
                       WHERE c.organization_id = NEW.organization_id AND c.id <> NEW.id
                         AND subject IN (c.holder_subject_id, c.address_subject_id)) THEN
            PERFORM app_scope_restore(previous);
            RAISE EXCEPTION 'admp_connection: the personal subject % was not created for this connection', subject
                USING ERRCODE = 'check_violation', CONSTRAINT = 'admp_connection_subject_check', TABLE = 'admp_connection';
        END IF;
        PERFORM app_scope_restore(previous);
    END LOOP;
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_admp_connection_subject_check
    AFTER INSERT OR UPDATE OF holder_subject_id, address_subject_id ON admp_connection
    FOR EACH ROW EXECUTE FUNCTION admp_connection_subject_check();

-- A moved CI and an interface moved to another CI re-derive their ports (a
-- no-op UPDATE re-runs the derivation).
CREATE FUNCTION admp_service_port_follow() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    IF TG_TABLE_NAME = 'ci' THEN
        UPDATE admp_service_port SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
    ELSE
        UPDATE admp_service_port SET network_interface_id = network_interface_id
         WHERE network_interface_id = NEW.id AND organization_id = NEW.organization_id;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_ci_admp_service_port_scope
    AFTER UPDATE OF client_id, site_id ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION admp_service_port_follow();
CREATE TRIGGER trg_network_interface_admp_service_port
    AFTER UPDATE OF ci_id ON network_interface
    FOR EACH ROW WHEN (OLD.ci_id IS DISTINCT FROM NEW.ci_id)
    EXECUTE FUNCTION admp_service_port_follow();

CREATE TRIGGER reticora_truncate_guard
    BEFORE TRUNCATE ON admp_connection
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
CREATE TRIGGER reticora_truncate_guard
    BEFORE TRUNCATE ON admp_service
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
CREATE TRIGGER reticora_truncate_guard
    BEFORE TRUNCATE ON admp_service_port
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE admp_connection ENABLE ROW LEVEL SECURITY;
ALTER TABLE admp_connection FORCE ROW LEVEL SECURITY;
ALTER TABLE admp_service ENABLE ROW LEVEL SECURITY;
ALTER TABLE admp_service FORCE ROW LEVEL SECURITY;
ALTER TABLE admp_service_port ENABLE ROW LEVEL SECURITY;
ALTER TABLE admp_service_port FORCE ROW LEVEL SECURITY;

-- Konvention 5 and E-09: a principal restricted to clients (and sites for
-- the ports) reads the rows of its clients and the shared rows without
-- client; it writes only rows of its clients.
DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    client_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    client_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_match CONSTANT text := 'site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[])';
    t text;
    read_rule text;
    write_rule text;
BEGIN
    FOREACH t IN ARRAY ARRAY['admp_connection', 'admp_service', 'admp_service_port'] LOOP
        read_rule := format('%s AND (%s OR client_id IS NULL OR %s)', org_match, client_all, client_match);
        write_rule := format('%s AND (%s OR %s)', org_match, client_all, client_match);
        IF t = 'admp_service_port' THEN
            read_rule := read_rule || format(' AND (%s OR site_id IS NULL OR %s)', site_all, site_match);
            write_rule := write_rule || format(' AND (%s OR %s)', site_all, site_match);
        END IF;
        EXECUTE format('CREATE POLICY %I ON %I USING (%s) WITH CHECK (%s)', t || '_isolation', t, read_rule, write_rule);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
            t || '_scoped_update', t, write_rule, write_rule);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR DELETE USING (%s)', t || '_scoped_delete', t, write_rule);
    END LOOP;
END
$$;
