-- Down of migration 000249: the tag assignments of assets go back into
-- asset.custom_fields.tags (the German display names, sorted; without updated_at
-- or version change); tags the up created in the catalog stay. The saved
-- views keep the normalized keys. propagate_asset_module_scope and
-- asset_version_bump get their versions of 000217 and 000241 back.

ALTER TABLE asset DISABLE TRIGGER trg_asset_updated_at;
ALTER TABLE asset DISABLE TRIGGER asset_version_bump;
UPDATE asset a SET custom_fields = COALESCE(a.custom_fields, '{}'::jsonb) || jsonb_build_object('tags', l.names)
FROM (SELECT x.asset_id, jsonb_agg(n.name ORDER BY n.name) AS names
      FROM asset_tag_link x
      JOIN tag t ON t.id = x.tag_id
      CROSS JOIN LATERAL (SELECT COALESCE(t.display_name ->> 'de', t.display_name ->> 'en', t.key) AS name) n
      GROUP BY x.asset_id) l
WHERE a.id = l.asset_id;
ALTER TABLE asset ENABLE TRIGGER asset_version_bump;
ALTER TABLE asset ENABLE TRIGGER trg_asset_updated_at;

CREATE OR REPLACE FUNCTION asset_version_bump() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF (to_jsonb(NEW) - 'updated_at' - 'version' - 'serial_number_norm')
       IS DISTINCT FROM (to_jsonb(OLD) - 'updated_at' - 'version' - 'serial_number_norm') THEN
        NEW.version := OLD.version + 1;
    ELSE
        NEW.version := OLD.version;
    END IF;
    RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION propagate_asset_module_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE ticket SET related_asset_id = related_asset_id WHERE related_asset_id = NEW.id;
    UPDATE document_link SET entity_id = entity_id WHERE entity_type = 'asset' AND entity_id = NEW.id;
    UPDATE document_link SET entity_id = entity_id
     WHERE entity_type = 'assignment'
       AND entity_id IN (SELECT s.id FROM assignment s WHERE s.asset_id = NEW.id);
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

DROP TABLE asset_tag_link;
DROP FUNCTION derive_asset_tag_link_scope();
