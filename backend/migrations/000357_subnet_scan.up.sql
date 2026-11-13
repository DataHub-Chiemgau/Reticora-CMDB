-- Migration 000357 (provisional number): the subnet scan as managed ICMP
-- probes of the probe engine (IPM-11, WP-1062).
--
-- 1. subnet: scan_enabled, collector_group_id (→ collector_group, the
--    redundancy after PRB-03), scan_interval_s (NULL: the effective value
--    of the type icmp), discover_new_hosts, offline_after_s (60 s to 30
--    days, default 1 h) and scan_synced_at; a subnet with scan_enabled
--    needs a collector group.
-- 2. subnet_scan_target(subnet, address, probe, owned): one row per scanned
--    address with the probe that checks it; owned is false for a probe of
--    the organization the scan reuses (e.g. an auto probe, WP-949). Client
--    and site come from the subnet (BEFORE trigger, read with the scope
--    widened; no foreign key on them, Konvention 19) and follow a moved
--    subnet. Deleting a row with owned deletes its probe: the probes of the
--    scan go with the subnet and with switching the scan off. ENABLE and
--    FORCE RLS with the policies of Konvention 5, truncate guard.
-- 3. probe: managed_by (ipam_scan, ipam_ptr) and managed_key, both or
--    neither. The key probe_target_key of WP-935 holds only for the probes
--    without managed_by (a partial unique index of the same name, NULLS NOT
--    DISTINCT); a managed probe is unique per (organization, managed_by,
--    managed_key).
-- 4. subnet_id_organization_key exists since 000287; the DO block creates
--    it only when it is missing.

-- ─── 1. subnet ───────────────────────────────────────────────────────────────

ALTER TABLE subnet
    ADD COLUMN scan_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN collector_group_id uuid NULL,
    ADD COLUMN scan_interval_s integer NULL CONSTRAINT subnet_scan_interval_s_check
        CHECK (scan_interval_s IS NULL OR scan_interval_s BETWEEN 1 AND 86400),
    ADD COLUMN discover_new_hosts boolean NOT NULL DEFAULT false,
    ADD COLUMN offline_after_s integer NOT NULL DEFAULT 3600 CONSTRAINT subnet_offline_after_s_check
        CHECK (offline_after_s BETWEEN 60 AND 2592000),
    ADD COLUMN scan_synced_at timestamptz NULL,
    ADD CONSTRAINT subnet_scan_collector_group_required CHECK (NOT scan_enabled OR collector_group_id IS NOT NULL),
    ADD CONSTRAINT subnet_collector_group_fkey FOREIGN KEY (collector_group_id, organization_id)
        REFERENCES collector_group (id, organization_id) ON DELETE RESTRICT;
CREATE INDEX idx_subnet_collector_group ON subnet (organization_id, collector_group_id) WHERE collector_group_id IS NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'subnet'::regclass AND conname = 'subnet_id_organization_key') THEN
        ALTER TABLE subnet ADD CONSTRAINT subnet_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;

-- ─── 3. probe: managed probes ─────────────────────────────────────────────────

ALTER TABLE probe
    ADD COLUMN managed_by text NULL CONSTRAINT probe_managed_by_check CHECK (managed_by IN ('ipam_scan', 'ipam_ptr')),
    ADD COLUMN managed_key text NULL CONSTRAINT probe_managed_key_check CHECK (length(managed_key) BETWEEN 1 AND 300),
    ADD CONSTRAINT probe_managed_pair CHECK ((managed_by IS NULL) = (managed_key IS NULL));
ALTER TABLE probe DROP CONSTRAINT probe_target_key;
CREATE UNIQUE INDEX probe_target_key ON probe (organization_id, client_id, ci_id, type, target) NULLS NOT DISTINCT
    WHERE managed_by IS NULL;
CREATE UNIQUE INDEX probe_managed_key ON probe (organization_id, managed_by, managed_key) WHERE managed_by IS NOT NULL;

-- ─── 2. subnet_scan_target ───────────────────────────────────────────────────

CREATE TABLE subnet_scan_target (
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       uuid NULL,
    site_id         uuid NULL,
    subnet_id       uuid NOT NULL,
    address         inet NOT NULL,
    probe_id        uuid NOT NULL,
    owned           boolean NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (subnet_id, address),
    CONSTRAINT subnet_scan_target_subnet_fkey FOREIGN KEY (subnet_id, organization_id)
        REFERENCES subnet (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT subnet_scan_target_probe_fkey FOREIGN KEY (probe_id, organization_id)
        REFERENCES probe (id, organization_id) ON DELETE CASCADE
);
CREATE INDEX idx_subnet_scan_target_probe ON subnet_scan_target (organization_id, probe_id);
CREATE INDEX idx_subnet_scan_target_org_subnet ON subnet_scan_target (organization_id, subnet_id);

-- Client and site of a target are those of its subnet, read with the scope
-- widened (000139); the policies check the row afterwards.
CREATE FUNCTION subnet_scan_target_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
BEGIN
    previous := app_scope_widen();
    SELECT s.client_id, s.site_id INTO NEW.client_id, NEW.site_id
      FROM subnet s WHERE s.id = NEW.subnet_id AND s.organization_id = NEW.organization_id FOR SHARE;
    PERFORM app_scope_restore(previous);
    RETURN NEW;
END
$$;
CREATE TRIGGER subnet_scan_target_scope BEFORE INSERT OR UPDATE OF subnet_id, organization_id, client_id, site_id
    ON subnet_scan_target FOR EACH ROW EXECUTE FUNCTION subnet_scan_target_scope();

-- A target with owned takes its probe along (the probes of the scan go with
-- the subnet and with switching the scan off). The probe of a reused
-- target stays.
CREATE FUNCTION subnet_scan_target_release() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF OLD.owned THEN
        DELETE FROM probe WHERE id = OLD.probe_id AND organization_id = OLD.organization_id AND managed_by IS NOT NULL;
    END IF;
    RETURN NULL;
END
$$;
CREATE TRIGGER subnet_scan_target_release AFTER DELETE ON subnet_scan_target
    FOR EACH ROW EXECUTE FUNCTION subnet_scan_target_release();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON subnet_scan_target
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- A moved subnet re-derives client and site of its targets.
CREATE FUNCTION propagate_subnet_scan_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE subnet_scan_target SET subnet_id = subnet_id WHERE subnet_id = NEW.id AND organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER subnet_scan_scope_propagation
    AFTER UPDATE ON subnet
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_subnet_scan_scope();

ALTER TABLE subnet_scan_target ENABLE ROW LEVEL SECURITY;
ALTER TABLE subnet_scan_target FORCE ROW LEVEL SECURITY;

-- Konvention 5 (pattern 000307): a restricted principal reads the targets
-- of its clients and sites and those without; it writes only targets
-- inside its scope.
DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_match CONSTANT text := 'site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[])';
    t CONSTANT text := 'subnet_scan_target';
BEGIN
    EXECUTE format('CREATE POLICY %I ON %I USING (%s AND (%s OR client_id IS NULL OR %s) AND (%s OR site_id IS NULL OR %s)) '
        || 'WITH CHECK (%s AND (%s OR %s) AND (%s OR %s))',
        t || '_isolation', t, org_match, scope_all, scope_match, site_all, site_match,
        org_match, scope_all, scope_match, site_all, site_match);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR UPDATE USING (%s AND (%s OR %s) AND (%s OR %s)) '
        || 'WITH CHECK (%s AND (%s OR %s) AND (%s OR %s))',
        t || '_scoped_update', t, org_match, scope_all, scope_match, site_all, site_match,
        org_match, scope_all, scope_match, site_all, site_match);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR DELETE USING (%s AND (%s OR %s) AND (%s OR %s))',
        t || '_scoped_delete', t, org_match, scope_all, scope_match, site_all, site_match);
END
$$;
