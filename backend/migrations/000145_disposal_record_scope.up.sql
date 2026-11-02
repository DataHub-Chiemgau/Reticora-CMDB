-- WP-353 (MGT-02, TEN-05; F-186): disposal records carry the client and site
-- of their asset and are scoped in the database.
--
-- Migration 000049 left disposal_record with an organization-only policy and
-- migration 000064 skipped it; the client scope existed only in the Go
-- predicate of the disposal repository. Like the module tables of 000064:
--
-- 1. client_id and site_id are derived by a BEFORE trigger whatever the
--    writer passes: from the asset (client of the asset, site of its
--    location), else from the CI. The trigger runs under the writer's RLS: an
--    asset or CI outside the scope derives NULL and is then rejected by WITH
--    CHECK for scoped principals (E-09).
-- 2. Propagation: when an asset changes client or location, or a CI client or
--    site, the records follow; the propagation runs with the client and site
--    scope widened (app_scope_widen, 000139) like 000141. The append-only
--    guard lets exactly this re-derivation through: a nested UPDATE that
--    changes nothing but client_id and site_id.
-- 3. Policies after TEN-05 with the restrictive UPDATE/DELETE policies of
--    000079 (the guard refuses both anyway).

ALTER TABLE disposal_record ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;

-- ─── 1. Derivation ─────────────────────────────────────────────────────────────

CREATE FUNCTION derive_disposal_record_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    IF NEW.asset_id IS NOT NULL THEN
        SELECT a.client_id, l.site_id INTO NEW.client_id, NEW.site_id
          FROM asset a LEFT JOIN location l ON l.id = a.location_id
         WHERE a.id = NEW.asset_id AND a.organization_id = NEW.organization_id;
    ELSIF NEW.ci_id IS NOT NULL THEN
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
          FROM ci c WHERE c.id = NEW.ci_id AND c.organization_id = NEW.organization_id;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER disposal_record_scope BEFORE INSERT OR UPDATE ON disposal_record
    FOR EACH ROW EXECUTE FUNCTION derive_disposal_record_scope();

-- ─── 2. Propagation ────────────────────────────────────────────────────────────

-- Append-only (000049), except the re-derivation of the scope by the
-- propagation triggers (trigger depth above 1, only client_id and site_id
-- change).
CREATE OR REPLACE FUNCTION disposal_record_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND pg_trigger_depth() > 1
       AND (to_jsonb(NEW) - 'client_id' - 'site_id') = (to_jsonb(OLD) - 'client_id' - 'site_id') THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'disposal_record is append-only (revision-safe)';
END;
$$;

CREATE FUNCTION propagate_disposal_record_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    IF TG_TABLE_NAME = 'asset' THEN
        UPDATE disposal_record SET asset_id = asset_id WHERE asset_id = NEW.id;
    ELSE
        UPDATE disposal_record SET ci_id = ci_id WHERE ci_id = NEW.id AND asset_id IS NULL;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER asset_disposal_scope_propagation AFTER UPDATE ON asset
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.location_id IS DISTINCT FROM NEW.location_id)
    EXECUTE FUNCTION propagate_disposal_record_scope();
CREATE TRIGGER ci_disposal_scope_propagation AFTER UPDATE ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_disposal_record_scope();

-- ─── 3. Backfill ───────────────────────────────────────────────────────────────

ALTER TABLE disposal_record DISABLE TRIGGER disposal_record_no_update;
UPDATE disposal_record SET asset_id = asset_id;
ALTER TABLE disposal_record ENABLE TRIGGER disposal_record_no_update;

CREATE INDEX idx_disposal_record_client ON disposal_record (client_id) WHERE client_id IS NOT NULL;

-- ─── 4. Policies ───────────────────────────────────────────────────────────────

DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    client_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    client_ids CONSTANT text := 'string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_ids CONSTANT text := 'string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[]';
    writable text := format('%1$s AND (%2$s OR client_id = ANY (%3$s)) AND (%4$s OR site_id = ANY (%5$s))',
                            org_match, client_all, client_ids, site_all, site_ids);
BEGIN
    DROP POLICY disposal_record_isolation ON disposal_record;
    EXECUTE format(
        'CREATE POLICY disposal_record_isolation ON disposal_record '
        'USING (%1$s AND (%2$s OR client_id IS NULL OR client_id = ANY (%3$s)) '
            'AND (%4$s OR site_id IS NULL OR site_id = ANY (%5$s))) '
        'WITH CHECK (%6$s)',
        org_match, client_all, client_ids, site_all, site_ids, writable);
    EXECUTE format('CREATE POLICY disposal_record_scoped_update ON disposal_record AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                   writable, writable);
    EXECUTE format('CREATE POLICY disposal_record_scoped_delete ON disposal_record AS RESTRICTIVE FOR DELETE USING (%s)', writable);
END
$$;
