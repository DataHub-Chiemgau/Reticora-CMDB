-- Down of 000271: the triggers and functions of the tag field rows and the
-- user identifiers go, search_index_asset, its trigger and
-- search_identifier_rows are those of 000233 again; the field rows
-- core.tags of ci and asset documents and the user identifiers are
-- deleted.

DROP TRIGGER search_index_tag_update ON tag;
DROP FUNCTION search_index_tag();
DROP TRIGGER search_index_asset_tag_update ON asset_tag_link;
DROP TRIGGER search_index_asset_tag_write ON asset_tag_link;
DROP FUNCTION search_index_asset_tag();
DROP TRIGGER search_index_ci_tag_update ON ci_tag;
DROP TRIGGER search_index_ci_tag_write ON ci_tag;
DROP FUNCTION search_index_ci_tag();
DROP FUNCTION search_refresh_tags(uuid, text[], uuid[]);
DROP FUNCTION search_tag_text(text, uuid);

DROP TRIGGER trg_asset_sync_search_index ON asset;
CREATE OR REPLACE FUNCTION search_index_asset() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    doc_id uuid;
    inv text;
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM search_document
         WHERE organization_id = OLD.organization_id AND entity_type = 'asset' AND entity_id = OLD.id;
        DELETE FROM search_identifier
         WHERE organization_id = OLD.organization_id
           AND ((entity_type = 'asset' AND entity_id = OLD.id) OR (field = 'asset.inventory_no' AND source_id = OLD.id));
        RETURN OLD;
    END IF;
    INSERT INTO search_document (organization_id, entity_type, entity_id, title, summary, url, metadata)
    VALUES (NEW.organization_id, 'asset', NEW.id, NEW.name, search_asset_summary(NEW), '/assets', '{}'::jsonb)
    ON CONFLICT (organization_id, entity_type, entity_id) DO UPDATE
        SET title = EXCLUDED.title, summary = EXCLUDED.summary, url = EXCLUDED.url, updated_at = now()
    RETURNING id INTO doc_id;

    DELETE FROM search_document_field s
     WHERE s.organization_id = NEW.organization_id AND s.document_id = doc_id
       AND NOT EXISTS (SELECT 1 FROM search_asset_fields(NEW) f WHERE f.field = s.field);
    INSERT INTO search_document_field (organization_id, document_id, field, content, source)
    SELECT NEW.organization_id, doc_id, f.field, f.content, 'trigger' FROM search_asset_fields(NEW) f
    ON CONFLICT (document_id, field) DO UPDATE SET content = EXCLUDED.content, source = 'trigger'
        WHERE search_document_field.content <> EXCLUDED.content OR search_document_field.source <> 'trigger';

    inv := lower(btrim(NEW.inventory_no));
    DELETE FROM search_identifier
     WHERE organization_id = NEW.organization_id AND entity_type = 'asset' AND entity_id = NEW.id
       AND field = 'core.inventory_no' AND (inv = '' OR norm <> inv);
    INSERT INTO search_identifier (organization_id, entity_type, entity_id, field, norm)
    SELECT NEW.organization_id, 'asset', NEW.id, 'core.inventory_no', inv
     WHERE inv <> '' AND NOT EXISTS (SELECT 1 FROM search_identifier s
          WHERE s.organization_id = NEW.organization_id AND s.entity_type = 'asset' AND s.entity_id = NEW.id
            AND s.field = 'core.inventory_no');

    IF TG_OP = 'INSERT' OR OLD.inventory_no IS DISTINCT FROM NEW.inventory_no OR OLD.ci_id IS DISTINCT FROM NEW.ci_id THEN
        DELETE FROM search_identifier s
         WHERE s.organization_id = NEW.organization_id AND s.field = 'asset.inventory_no' AND s.source_id = NEW.id
           AND (inv = '' OR NOT EXISTS (SELECT 1 FROM ci c WHERE c.id = s.entity_id AND c.asset_id = NEW.id AND c.deleted_at IS NULL));
        INSERT INTO search_identifier (organization_id, entity_type, entity_id, field, source_id, norm)
        SELECT NEW.organization_id, 'ci', c.id, 'asset.inventory_no', NEW.id, inv
          FROM ci c WHERE c.organization_id = NEW.organization_id AND c.asset_id = NEW.id AND c.deleted_at IS NULL AND inv <> ''
        ON CONFLICT (source_id, field, entity_id) WHERE source_id IS NOT NULL
            DO UPDATE SET norm = EXCLUDED.norm WHERE search_identifier.norm <> EXCLUDED.norm;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_asset_sync_search_index
    AFTER INSERT OR UPDATE OF name, inventory_no, category, status, lifecycle_state, supplier, supplier_id,
        serial_number, location, notes, custom_fields, ci_id OR DELETE ON asset
    FOR EACH ROW EXECUTE FUNCTION search_index_asset();

CREATE OR REPLACE FUNCTION search_identifier_rows(p_org uuid)
RETURNS TABLE (entity_type text, entity_id uuid, field text, source_id uuid, norm text, addr inet)
LANGUAGE sql STABLE AS $$
    SELECT 'ci', c.id, 'core.management_ip', NULL::uuid, host(c.management_ip), host(c.management_ip)::inet
      FROM ci c WHERE c.organization_id = p_org AND c.deleted_at IS NULL AND c.management_ip IS NOT NULL
    UNION ALL
    SELECT 'ci', c.id, 'core.primary_mac', NULL::uuid, search_mac_norm(c.primary_mac), NULL::inet
      FROM ci c WHERE c.organization_id = p_org AND c.deleted_at IS NULL AND c.primary_mac IS NOT NULL
    UNION ALL
    SELECT 'ci', c.id, 'asset.inventory_no', a.id, lower(btrim(a.inventory_no)), NULL::inet
      FROM ci c JOIN asset a ON a.id = c.asset_id AND a.organization_id = c.organization_id
     WHERE c.organization_id = p_org AND c.deleted_at IS NULL AND lower(btrim(a.inventory_no)) <> ''
    UNION ALL
    SELECT 'ci', n.ci_id, 'network.mac', n.id, search_mac_norm(n.mac), NULL::inet
      FROM network_interface n WHERE n.organization_id = p_org AND n.mac IS NOT NULL
    UNION ALL
    SELECT 'ci', n.ci_id, 'network.ip', i.id, host(i.address), host(i.address)::inet
      FROM ip_address i JOIN network_interface n ON n.id = i.interface_id
     WHERE i.organization_id = p_org
    UNION ALL
    SELECT 'asset', a.id, 'core.inventory_no', NULL::uuid, lower(btrim(a.inventory_no)), NULL::inet
      FROM asset a WHERE a.organization_id = p_org AND lower(btrim(a.inventory_no)) <> ''
$$;

DELETE FROM search_document_field f USING search_document d
 WHERE d.id = f.document_id AND d.entity_type IN ('ci', 'asset') AND f.field = 'core.tags';
DELETE FROM search_identifier WHERE field IN ('core.owner_user_id', 'core.assigned_user_id', 'asset.assigned_user_id');
