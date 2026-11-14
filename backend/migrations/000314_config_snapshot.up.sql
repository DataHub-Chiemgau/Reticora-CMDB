-- WP-1052 (ALR-07, E-207 option 2): the versions of the device
-- configurations.
--
-- config_snapshot holds one row per changed configuration of a CI: hash
-- (SHA-256 over the normalized, masked text), uncompressed size (at most
-- 2 MiB), the masked text gzip-compressed (at most 2 MiB as well) and the
-- number of masked values; the collector's id carries no foreign key (the
-- version outlives a deleted collector). One version per CI and
-- collection time; the store (configsnap.PGStore) writes a row only when
-- the hash differs from the latest version of the CI, so a return to an
-- earlier state (A -> B -> A) is stored again. client_id and site_id are
-- those of the CI: the trigger config_snapshot_scope derives them on every
-- write (whatever the writer passes), and a CI moved to another client or
-- site carries its versions along (config_snapshot_scope_propagation on
-- ci, pattern 000295/000307). The retention run
-- retention.config_snapshot deletes versions older than 400 days except
-- the latest per CI.
--
-- ENABLE and FORCE RLS with policies after Konvention 5 (E-09), truncate
-- guard, composite foreign keys with organization_id (Konvention 19).

CREATE TABLE config_snapshot (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       uuid NULL,
    site_id         uuid NULL,
    ci_id           uuid NOT NULL,
    collector_id    uuid NULL,
    collected_at    timestamptz NOT NULL,
    received_at     timestamptz NOT NULL DEFAULT now(),
    source          text NOT NULL CONSTRAINT config_snapshot_source_check CHECK (source IN ('ssh', 'gnmi')),
    trigger         text NOT NULL CONSTRAINT config_snapshot_trigger_check CHECK (trigger IN ('schedule', 'snmp', 'trap', 'gnmi')),
    platform        text NOT NULL CONSTRAINT config_snapshot_platform_check CHECK (platform ~ '^[a-z][a-z0-9_]{0,39}$'),
    sha256          text NOT NULL CONSTRAINT config_snapshot_sha256_check CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size_bytes      integer NOT NULL CONSTRAINT config_snapshot_size_bytes_check CHECK (size_bytes BETWEEN 1 AND 2097152),
    content         bytea NOT NULL CONSTRAINT config_snapshot_content_check CHECK (octet_length(content) <= 2097152),
    masked_count    integer NOT NULL DEFAULT 0 CONSTRAINT config_snapshot_masked_count_check CHECK (masked_count >= 0),
    CONSTRAINT config_snapshot_ci_collected_key UNIQUE (ci_id, collected_at),
    CONSTRAINT config_snapshot_ci_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT config_snapshot_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id)
);
CREATE INDEX idx_config_snapshot_ci ON config_snapshot (organization_id, ci_id, collected_at DESC);

-- Client and site of a version are those of its CI, read with the scope
-- widened (000139) so a scoped writer cannot set or keep a stale scope;
-- the policies check the row afterwards.
CREATE FUNCTION config_snapshot_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
      FROM ci c WHERE c.id = NEW.ci_id AND c.organization_id = NEW.organization_id FOR SHARE;
    PERFORM app_scope_restore(previous);
    RETURN NEW;
END
$$;
CREATE TRIGGER config_snapshot_scope BEFORE INSERT OR UPDATE OF ci_id, client_id, site_id, organization_id ON config_snapshot
    FOR EACH ROW EXECUTE FUNCTION config_snapshot_scope();

-- A CI moved to another client or site re-derives the scope of its
-- versions (a no-op UPDATE of ci_id re-runs config_snapshot_scope).
CREATE FUNCTION propagate_config_snapshot_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE config_snapshot SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER config_snapshot_scope_propagation AFTER UPDATE ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_config_snapshot_scope();

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON config_snapshot
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE config_snapshot ENABLE ROW LEVEL SECURITY;
ALTER TABLE config_snapshot FORCE ROW LEVEL SECURITY;

-- Konvention 5 (E-09): a restricted principal reads the versions of its
-- clients and sites and those without; it writes only inside its scope.
DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_match CONSTANT text := 'site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[])';
BEGIN
    EXECUTE format('CREATE POLICY config_snapshot_isolation ON config_snapshot USING (%s AND (%s OR client_id IS NULL OR %s) AND (%s OR site_id IS NULL OR %s)) '
        || 'WITH CHECK (%s AND (%s OR %s) AND (%s OR %s))',
        org_match, scope_all, scope_match, site_all, site_match,
        org_match, scope_all, scope_match, site_all, site_match);
    EXECUTE format('CREATE POLICY config_snapshot_scoped_update ON config_snapshot AS RESTRICTIVE FOR UPDATE USING (%s AND (%s OR %s) AND (%s OR %s)) '
        || 'WITH CHECK (%s AND (%s OR %s) AND (%s OR %s))',
        org_match, scope_all, scope_match, site_all, site_match,
        org_match, scope_all, scope_match, site_all, site_match);
    EXECUTE format('CREATE POLICY config_snapshot_scoped_delete ON config_snapshot AS RESTRICTIVE FOR DELETE USING (%s AND (%s OR %s) AND (%s OR %s))',
        org_match, scope_all, scope_match, site_all, site_match);
END
$$;
