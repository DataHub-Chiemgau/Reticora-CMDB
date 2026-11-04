-- WP-740 (AST-01, API-07, EVT-03): the asset carries a version for its ETag
-- and If-Match, and the asset rows of entity_change carry the version they
-- produced, the base of the field-granular 409.
--
-- * asset.version starts at 1. The trigger asset_version_bump (BEFORE
--   UPDATE) raises it whenever a column other than updated_at and version
--   changes, so the writers outside the asset package (stock movements,
--   rack placement, lifecycle transitions, stocktake, CI merge, CI delete
--   and restore, the location adoption of a linked CI, the hard-delete job)
--   count without changes of their own. BEFORE triggers run in the order of
--   their names: asset_version_bump runs after asset_default_currency
--   (000229), asset_mirror_legacy_columns (000231) and
--   asset_status_from_lifecycle, so an empty currency that the currency
--   trigger resets to the old value raises no version. An UPDATE that
--   changes nothing keeps the version; a version the statement sets itself
--   is ignored.
-- * entity_change.entity_version (nullable): the version an asset write
--   left; rows of other entity types and older rows have none.
--   idx_entity_change_version serves the lookup of the fields changed
--   since an If-Match version.

ALTER TABLE asset ADD COLUMN version BIGINT NOT NULL DEFAULT 1;

CREATE FUNCTION asset_version_bump() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF (to_jsonb(NEW) - 'updated_at' - 'version') IS DISTINCT FROM (to_jsonb(OLD) - 'updated_at' - 'version') THEN
        NEW.version := OLD.version + 1;
    ELSE
        NEW.version := OLD.version;
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER asset_version_bump BEFORE UPDATE ON asset
    FOR EACH ROW EXECUTE FUNCTION asset_version_bump();

ALTER TABLE entity_change ADD COLUMN IF NOT EXISTS entity_version BIGINT;
CREATE INDEX IF NOT EXISTS idx_entity_change_version ON entity_change (entity_type, entity_id, entity_version);
