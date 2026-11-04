-- WP-794 (AUD-08, SEC-05, TEN-02, TEN-05): the value history of assets is
-- written by the database for every writer, and entity_change carries the
-- client and site scope of its asset rows.
--
-- Since WP-516 only asset/pg_repository.go wrote the asset rows of
-- entity_change; the writers outside the asset package (stock movements,
-- rack placement, stocktake, CI merge, CI delete and restore, the location
-- adoption of a linked CI, the hard-delete job) left no trail, and
-- entity_change had only the organization policy: the client and site scope
-- of an asset row was checked by the history repository alone.
--
-- 1. entity_change.client_id and entity_change.site_id (uuid, no foreign
--    key, denormalized like ci_change.client_id and ci_change.site_id) with
--    the index idx_entity_change_org_entity (organization_id, entity_type,
--    entity_id, created_at).
-- 2. asset_history (AFTER INSERT OR UPDATE OR DELETE ON asset, FOR EACH
--    ROW) writes the trail of the typed columns in the form of the asset
--    DTO (asset_history_form): the create row carries the whole row, the
--    delete row the whole previous row, an update one row per changed
--    column, each with actor_id from app.user_id, entity_version from
--    version (WP-740) and client_id and site_id of NEW or OLD. Left out
--    everywhere are custom_fields (sealed by the asset repository with
--    pii.SealFieldTx, its only writer), created_at, updated_at, version and
--    the derived columns: the deprecated mirrors asset_tag, purchase_cost,
--    warranty_end and supplier (000231) and serial_number_norm (000241),
--    which would repeat a change of their source column. lifecycle_state is
--    left out of the update rows: lifecycle.Service.Transition writes its
--    lifecycle_transition row with reason (WP-724). ci_id is the DTO member
--    primary_ci_id (E-122), purchase_price its decimal string. The typed
--    columns hold no personal key (pii.IsPersonalKey); a test compares them.
-- 3. entity_change_derive_scope (BEFORE INSERT ON entity_change) sets
--    client_id and site_id of a row of an asset or a CI to those of the
--    entity, read with the scope widened (app_scope_widen, 000139): a row
--    written without them (the path history.PGRepository.Record, the
--    lifecycle transition of an asset or a CI, WP-724) gets them, and a
--    written scope cannot place a row of an existing entity outside its
--    scope. The row of a deleted asset keeps the written scope (the delete
--    row of asset_history). The policies are checked after the scope was
--    restored. The CI rows get the scope too: under the policy of 6. a
--    client- or site-scoped principal writes no row without client (TEN-05),
--    so its CI lifecycle transitions need the scope of the CI.
-- 4. asset_history_scope_propagation and ci_history_scope_propagation
--    (AFTER UPDATE when client_id or site_id changed, for an asset also
--    through trg_asset_derive_site of 000151) re-scope the rows of the
--    entity with the scope widened (propagate_entity_change_scope, pattern
--    propagate_ci_scope of 000141).
-- 5. Backfill of client_id and site_id of the existing rows of assets and
--    CIs: from the entity, for a deleted asset from its last delete row
--    (client_id and the site of its location_id).
-- 6. entity_change_isolation gets the client and site predicate of
--    asset_tenant_isolation (000151) in USING and WITH CHECK, plus the
--    restrictive policies entity_change_scoped_update and
--    entity_change_scoped_delete (000079). Rows of the other entity types
--    carry no scope: every principal reads them, only org-wide principals
--    write them (E-09).
-- ENABLE and FORCE ROW LEVEL SECURITY stay.

-- ─── 1. Columns and index ────────────────────────────────────────────────────

ALTER TABLE entity_change ADD COLUMN IF NOT EXISTS client_id UUID;
ALTER TABLE entity_change ADD COLUMN IF NOT EXISTS site_id UUID;
CREATE INDEX IF NOT EXISTS idx_entity_change_org_entity ON entity_change (organization_id, entity_type, entity_id, created_at);

-- ─── 2. Trail of the typed columns ───────────────────────────────────────────

-- asset_history_form is an asset row in the form of the asset DTO without
-- the columns the trigger leaves out. It reads the row as JSON only, so it
-- names no column (a later migration that renames or drops one keeps it
-- working).
CREATE FUNCTION asset_history_form(a asset) RETURNS jsonb
LANGUAGE sql STABLE
SET search_path = pg_catalog, public, pg_temp
AS $$
    SELECT (r.j - ARRAY['custom_fields', 'created_at', 'updated_at', 'version',
                        'asset_tag', 'purchase_cost', 'warranty_end', 'supplier', 'serial_number_norm',
                        'ci_id', 'purchase_price'])
        || jsonb_build_object('primary_ci_id', r.j -> 'ci_id', 'purchase_price', to_jsonb(r.j ->> 'purchase_price'))
      FROM (SELECT to_jsonb(a) AS j) r
$$;

CREATE FUNCTION asset_history() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    actor UUID;
    user_setting text := current_setting('app.user_id', true);
    old_row jsonb;
    new_row jsonb;
BEGIN
    IF user_setting ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
        actor := user_setting::uuid;
    END IF;
    IF TG_OP = 'INSERT' THEN
        INSERT INTO entity_change (organization_id, entity_type, entity_id, actor_id, change_type,
                                   new_value, entity_version, client_id, site_id)
        VALUES (NEW.organization_id, 'asset', NEW.id, actor, 'create',
                jsonb_strip_nulls(asset_history_form(NEW)), NEW.version, NEW.client_id, NEW.site_id);
        RETURN NULL;
    END IF;
    IF TG_OP = 'DELETE' THEN
        -- The deletion of the organization takes its assets along: no row
        -- of an organization that is gone.
        IF NOT EXISTS (SELECT 1 FROM organization o WHERE o.id = OLD.organization_id) THEN
            RETURN NULL;
        END IF;
        INSERT INTO entity_change (organization_id, entity_type, entity_id, actor_id, change_type,
                                   old_value, entity_version, client_id, site_id)
        VALUES (OLD.organization_id, 'asset', OLD.id, actor, 'delete',
                jsonb_strip_nulls(asset_history_form(OLD)), OLD.version, OLD.client_id, OLD.site_id);
        RETURN NULL;
    END IF;
    old_row := asset_history_form(OLD);
    new_row := asset_history_form(NEW);
    INSERT INTO entity_change (organization_id, entity_type, entity_id, actor_id, change_type, field_name,
                               old_value, new_value, entity_version, client_id, site_id)
    SELECT NEW.organization_id, 'asset', NEW.id, actor, 'update', f.key,
           NULLIF(old_row -> f.key, 'null'::jsonb), NULLIF(f.value, 'null'::jsonb), NEW.version, NEW.client_id, NEW.site_id
      FROM jsonb_each(new_row) f
     WHERE f.key <> 'lifecycle_state' AND (old_row -> f.key) IS DISTINCT FROM f.value
     ORDER BY f.key;
    RETURN NULL;
END
$$;

CREATE TRIGGER asset_history AFTER INSERT OR UPDATE OR DELETE ON asset
    FOR EACH ROW EXECUTE FUNCTION asset_history();

-- ─── 3. Scope of the rows of an asset or CI ─────────────────────────────────

CREATE FUNCTION entity_change_derive_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    entity_client UUID;
    entity_site UUID;
    known boolean;
BEGIN
    IF NEW.entity_type NOT IN ('asset', 'ci') THEN
        RETURN NEW;
    END IF;
    previous := app_scope_widen();
    IF NEW.entity_type = 'asset' THEN
        SELECT a.client_id, a.site_id INTO entity_client, entity_site
          FROM asset a WHERE a.id = NEW.entity_id AND a.organization_id = NEW.organization_id;
    ELSE
        SELECT c.client_id, c.site_id INTO entity_client, entity_site
          FROM ci c WHERE c.id = NEW.entity_id AND c.organization_id = NEW.organization_id;
    END IF;
    known := FOUND;
    PERFORM app_scope_restore(previous);
    IF known THEN
        NEW.client_id := entity_client;
        NEW.site_id := entity_site;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER entity_change_derive_scope BEFORE INSERT ON entity_change
    FOR EACH ROW EXECUTE FUNCTION entity_change_derive_scope();

-- ─── 4. Propagation ──────────────────────────────────────────────────────────

-- propagate_entity_change_scope re-scopes the entity_change rows of the
-- asset or CI (TG_ARGV[0]) whose client or site changed.
CREATE FUNCTION propagate_entity_change_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE entity_change SET client_id = NEW.client_id, site_id = NEW.site_id
     WHERE organization_id = NEW.organization_id AND entity_type = TG_ARGV[0] AND entity_id = NEW.id
       AND (client_id IS DISTINCT FROM NEW.client_id OR site_id IS DISTINCT FROM NEW.site_id);
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER asset_history_scope_propagation AFTER UPDATE ON asset
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_entity_change_scope('asset');
CREATE TRIGGER ci_history_scope_propagation AFTER UPDATE ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_entity_change_scope('ci');

-- ─── 5. Backfill ─────────────────────────────────────────────────────────────

UPDATE entity_change e SET client_id = a.client_id, site_id = a.site_id
  FROM asset a
 WHERE e.entity_type = 'asset' AND a.id = e.entity_id AND a.organization_id = e.organization_id;

UPDATE entity_change e SET client_id = c.client_id, site_id = c.site_id
  FROM ci c
 WHERE e.entity_type = 'ci' AND c.id = e.entity_id AND c.organization_id = e.organization_id;

UPDATE entity_change e SET client_id = d.client_id, site_id = d.site_id
  FROM (SELECT DISTINCT ON (x.organization_id, x.entity_id) x.organization_id, x.entity_id,
               CASE WHEN x.old_value ->> 'client_id' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
                    THEN (x.old_value ->> 'client_id')::uuid END AS client_id,
               (SELECT l.site_id FROM location l
                 WHERE l.organization_id = x.organization_id AND l.id::text = x.old_value ->> 'location_id') AS site_id
          FROM entity_change x
         WHERE x.entity_type = 'asset' AND x.change_type = 'delete' AND jsonb_typeof(x.old_value) = 'object'
         ORDER BY x.organization_id, x.entity_id, x.created_at DESC) d
 WHERE e.entity_type = 'asset' AND e.organization_id = d.organization_id AND e.entity_id = d.entity_id
   AND NOT EXISTS (SELECT 1 FROM asset a WHERE a.id = e.entity_id);

-- ─── 6. Policies ─────────────────────────────────────────────────────────────

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
    DROP POLICY entity_change_isolation ON entity_change;
    EXECUTE format(
        'CREATE POLICY entity_change_isolation ON entity_change '
        'USING (%1$s AND (%2$s OR client_id IS NULL OR client_id = ANY (%3$s)) '
            'AND (%4$s OR site_id IS NULL OR site_id = ANY (%5$s))) '
        'WITH CHECK (%6$s)',
        org_match, client_all, client_ids, site_all, site_ids, writable);
    EXECUTE format('CREATE POLICY entity_change_scoped_update ON entity_change AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                   writable, writable);
    EXECUTE format('CREATE POLICY entity_change_scoped_delete ON entity_change AS RESTRICTIVE FOR DELETE USING (%s)',
                   writable);
END
$$;
