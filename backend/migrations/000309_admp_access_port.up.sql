-- Migration 000309: the port inventory of ADMP (ADM-04, E-251, WP-1073).
-- The ADMP documentation 9.14.0 names a port only by AccessPort.id; node
-- name, unit index and port index locate it. admp_access_port keeps the
-- ports of an account from GET /V1/infrastructure/accessnodepicker with
-- their match to a network interface (matched_by rule, manual or none), so
-- a refresh of an interface needs no new inventory and ports without a
-- service are known. Credentials of the network elements are never stored.
--
-- external_id is AccessPort.id (the port_key of admp_service_port);
-- fetched_at is the time of the inventory, expires_at when the services of
-- the port are due again. admp_account.inventory_fetched_at keeps the time
-- of the account's last inventory, also for an empty one.
--
-- Scope (Konvention 5, pattern admp_service_port): client_id and site_id
-- from the CI, the CI from the interface, without CI the client of the
-- account. Deleting an interface unlinks the port (matched_by none).
-- Composite foreign keys (Konvention 19).

CREATE TABLE admp_access_port (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id      uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    account_id           uuid NOT NULL,
    external_id          text NOT NULL,
    node_external_id     text NOT NULL,
    node_name            text NOT NULL,
    unit_index           bigint NOT NULL,
    port_index           bigint NOT NULL,
    protocol             text NULL,
    network_interface_id uuid NULL,
    ci_id                uuid NULL,
    matched_by           text NOT NULL DEFAULT 'none' CONSTRAINT admp_access_port_matched_by_check
                             CHECK (matched_by IN ('rule', 'manual', 'none')),
    client_id            uuid NULL,
    site_id              uuid NULL,
    fetched_at           timestamptz NOT NULL DEFAULT now(),
    expires_at           timestamptz NOT NULL,
    CONSTRAINT admp_access_port_external_key UNIQUE (account_id, external_id),
    CONSTRAINT admp_access_port_account_fkey FOREIGN KEY (account_id, organization_id)
        REFERENCES admp_account (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT admp_access_port_interface_fkey FOREIGN KEY (network_interface_id, organization_id)
        REFERENCES network_interface (id, organization_id) ON DELETE SET NULL (network_interface_id),
    CONSTRAINT admp_access_port_ci_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE SET NULL (ci_id)
);

CREATE INDEX idx_admp_access_port_interface ON admp_access_port (account_id, network_interface_id);
CREATE INDEX idx_admp_access_port_expires ON admp_access_port (organization_id, expires_at);
-- The follow triggers and ON DELETE SET NULL of an interface or CI find
-- their ports by these columns alone (pattern admp_service_port).
CREATE INDEX idx_admp_access_port_ni ON admp_access_port (network_interface_id) WHERE network_interface_id IS NOT NULL;
CREATE INDEX idx_admp_access_port_ci ON admp_access_port (ci_id) WHERE ci_id IS NOT NULL;

ALTER TABLE admp_account ADD COLUMN inventory_fetched_at timestamptz NULL;

-- A port takes its CI from the interface, client and site from the CI,
-- without CI the client of the account. An unlinked interface (deleted:
-- ON DELETE SET NULL) leaves the port unmatched.
CREATE FUNCTION admp_access_port_scope() RETURNS trigger
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
          FROM admp_account a WHERE a.id = NEW.account_id AND a.organization_id = NEW.organization_id;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_admp_access_port_scope
    BEFORE INSERT OR UPDATE ON admp_access_port
    FOR EACH ROW EXECUTE FUNCTION admp_access_port_scope();

-- A moved CI and an interface moved to another CI re-derive their ports.
CREATE FUNCTION admp_access_port_follow() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    IF TG_TABLE_NAME = 'ci' THEN
        UPDATE admp_access_port SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
    ELSE
        UPDATE admp_access_port SET network_interface_id = network_interface_id
         WHERE network_interface_id = NEW.id AND organization_id = NEW.organization_id;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_ci_admp_access_port_scope
    AFTER UPDATE OF client_id, site_id ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION admp_access_port_follow();
CREATE TRIGGER trg_network_interface_admp_access_port
    AFTER UPDATE OF ci_id ON network_interface
    FOR EACH ROW WHEN (OLD.ci_id IS DISTINCT FROM NEW.ci_id)
    EXECUTE FUNCTION admp_access_port_follow();

CREATE TRIGGER reticora_truncate_guard
    BEFORE TRUNCATE ON admp_access_port
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE admp_access_port ENABLE ROW LEVEL SECURITY;
ALTER TABLE admp_access_port FORCE ROW LEVEL SECURITY;

-- Konvention 5 and E-09, as admp_service_port: a principal restricted to
-- clients and sites reads the rows of its clients and sites and the shared
-- rows without client or site; it writes only rows of its clients and
-- sites.
DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    client_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    client_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_match CONSTANT text := 'site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[])';
    read_rule text;
    write_rule text;
BEGIN
    read_rule := format('%s AND (%s OR client_id IS NULL OR %s) AND (%s OR site_id IS NULL OR %s)',
        org_match, client_all, client_match, site_all, site_match);
    write_rule := format('%s AND (%s OR %s) AND (%s OR %s)', org_match, client_all, client_match, site_all, site_match);
    EXECUTE format('CREATE POLICY admp_access_port_isolation ON admp_access_port USING (%s) WITH CHECK (%s)',
        read_rule, write_rule);
    EXECUTE format('CREATE POLICY admp_access_port_scoped_update ON admp_access_port AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
        write_rule, write_rule);
    EXECUTE format('CREATE POLICY admp_access_port_scoped_delete ON admp_access_port AS RESTRICTIVE FOR DELETE USING (%s)',
        write_rule);
END
$$;
