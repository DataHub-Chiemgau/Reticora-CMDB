-- WP-392 (TEN-01, TEN-02, TEN-05; F-014, F-141): assets and quantity items
-- carry the site of their location and are confined to the site scope.
--
-- asset and quantity_item have location_id and client_id but no site_id, and
-- their policies (000058, 000079) check app.client_scope only. A principal
-- restricted to site 1 of client 1 therefore saw the assets and quantity
-- items of every site of client 1 (TEN-02), also after WP-390 confined it to
-- the client of its site (F-014).
--
-- 1. site_id (foreign key to the site of the organization), derived by a
--    BEFORE trigger from location_id like ci_derive_location (000074): a
--    written value is overwritten, without location the row has no site. The
--    location is read with the client and site scope widened
--    (app_scope_widen, 000139, like derive_edge_scope in 000141), so the site
--    is derived even when the location lies outside the writer's scope; the
--    policies of the row are checked after the trigger restored the scope,
--    so WITH CHECK refuses such a row. A location of another organization is
--    not found (23503, like ci_derive_location).
-- 2. When a location changes its site (moved or re-parented, the
--    descendants through location_propagate), the assets and quantity items
--    at it are re-derived like location_refresh_ci does for CIs, with the
--    scope widened so that rows the mover cannot see follow as well.
-- 3. Existing rows are backfilled.
-- 4. The policies get the site predicate of ci (000063): USING keeps rows
--    without site readable, WITH CHECK requires a site of the scope unless
--    the scope is org-wide (E-09), likewise the restrictive UPDATE and
--    DELETE policies of 000079. ENABLE and FORCE ROW LEVEL SECURITY stay.

-- ─── 1. Columns and derivation ────────────────────────────────────────────────

ALTER TABLE asset ADD COLUMN site_id UUID,
    ADD CONSTRAINT asset_site_id_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id);
ALTER TABLE quantity_item ADD COLUMN site_id UUID,
    ADD CONSTRAINT quantity_item_site_id_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id);

CREATE INDEX idx_asset_site ON asset (site_id) WHERE site_id IS NOT NULL;
CREATE INDEX idx_quantity_item_site ON quantity_item (site_id) WHERE site_id IS NOT NULL;

-- derive_location_site sets site_id of an asset or quantity item to the site
-- of its location.
CREATE FUNCTION derive_location_site() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    location_site UUID;
BEGIN
    NEW.site_id := NULL;
    IF NEW.location_id IS NULL THEN
        RETURN NEW;
    END IF;
    previous := app_scope_widen();
    SELECT l.site_id INTO location_site
      FROM location l WHERE l.id = NEW.location_id AND l.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    IF location_site IS NULL THEN
        RAISE EXCEPTION 'location % not found', NEW.location_id
            USING ERRCODE = '23503', CONSTRAINT = TG_TABLE_NAME || '_location_id_fkey';
    END IF;
    NEW.site_id := location_site;
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_asset_derive_site BEFORE INSERT OR UPDATE OF location_id, site_id ON asset
    FOR EACH ROW EXECUTE FUNCTION derive_location_site();
CREATE TRIGGER trg_quantity_item_derive_site BEFORE INSERT OR UPDATE OF location_id, site_id ON quantity_item
    FOR EACH ROW EXECUTE FUNCTION derive_location_site();

-- ─── 2. Propagation ────────────────────────────────────────────────────────────

-- location_refresh_asset_site re-derives the assets and quantity items of a
-- location whose site changed.
CREATE FUNCTION location_refresh_asset_site() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE asset SET location_id = location_id WHERE location_id = NEW.id;
    UPDATE quantity_item SET location_id = location_id WHERE location_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_location_refresh_asset_site AFTER UPDATE ON location
    FOR EACH ROW WHEN (OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION location_refresh_asset_site();

-- ─── 3. Backfill ───────────────────────────────────────────────────────────────

-- The foreign keys on location_id do not include the organization: a row
-- whose location belongs to another organization keeps no site (and is
-- reported) instead of failing the migration; the derivation refuses it at
-- its next change of location_id.
DO $$
DECLARE
    foreign_location bigint;
BEGIN
    UPDATE asset a SET site_id = l.site_id
      FROM location l WHERE l.id = a.location_id AND l.organization_id = a.organization_id;
    UPDATE quantity_item q SET site_id = l.site_id
      FROM location l WHERE l.id = q.location_id AND l.organization_id = q.organization_id;
    SELECT (SELECT count(*) FROM asset WHERE location_id IS NOT NULL AND site_id IS NULL)
         + (SELECT count(*) FROM quantity_item WHERE location_id IS NOT NULL AND site_id IS NULL)
      INTO foreign_location;
    IF foreign_location > 0 THEN
        RAISE NOTICE '% assets and quantity items reference a location of another organization and have no site',
            foreign_location;
    END IF;
END
$$;

-- ─── 4. Policies ───────────────────────────────────────────────────────────────

DO $$
DECLARE
    entry RECORD;
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    client_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    client_ids CONSTANT text := 'string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_ids CONSTANT text := 'string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[]';
    writable text := format('%1$s AND (%2$s OR client_id = ANY (%3$s)) AND (%4$s OR site_id = ANY (%5$s))',
                            org_match, client_all, client_ids, site_all, site_ids);
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('asset', 'asset_tenant_isolation'),
            ('quantity_item', 'quantity_item_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('DROP POLICY %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format('DROP POLICY %I ON %I', entry.table_name || '_scoped_update', entry.table_name);
        EXECUTE format('DROP POLICY %I ON %I', entry.table_name || '_scoped_delete', entry.table_name);
        EXECUTE format(
            'CREATE POLICY %1$I ON %2$I '
            'USING (%3$s AND (%4$s OR client_id IS NULL OR client_id = ANY (%5$s)) '
                'AND (%6$s OR site_id IS NULL OR site_id = ANY (%7$s))) '
            'WITH CHECK (%8$s)',
            entry.policy_name, entry.table_name,
            org_match, client_all, client_ids, site_all, site_ids, writable);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                       entry.table_name || '_scoped_update', entry.table_name, writable, writable);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR DELETE USING (%s)',
                       entry.table_name || '_scoped_delete', entry.table_name, writable);
    END LOOP;
END
$$;
