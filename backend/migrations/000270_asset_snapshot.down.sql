-- Migration 000270 down: the asset snapshots go; propagate_entity_change_scope
-- returns to its version of 000250. The policies, the index, the scope
-- trigger and the TRUNCATE guard go with the table.
CREATE OR REPLACE FUNCTION propagate_entity_change_scope() RETURNS trigger
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

DROP TABLE IF EXISTS asset_snapshot;
DROP FUNCTION IF EXISTS asset_snapshot_derive_scope();
