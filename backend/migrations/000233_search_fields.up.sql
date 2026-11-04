-- Migration 000233: search over inventory number, IP, MAC and custom
-- fields (SRC-02, WP-308).
--
-- 1. search_identifier holds the identifiers of the ci and asset documents
--    in a normal form a B-tree answers under FORCE ROW LEVEL SECURITY: the
--    operators of @@ (ts_match_vq) and <<= (network_subeq) are not
--    leakproof, the ones of =, ~>=~/~<~ on text and of the comparisons on
--    inet are (SR-04, E-133, K22). norm is a MAC as twelve lower-case hex
--    digits without separators, an IP as host() text, an inventory number
--    as lower(btrim()); addr is the host address of an IP (/32, /128).
--    Fields: core.management_ip, core.primary_mac, asset.inventory_no (the
--    inventory number of the linked asset; source_id = asset), network.ip
--    and network.mac (the addresses and MACs of the interfaces of the CI;
--    source_id = ip_address or network_interface) at a CI, core.inventory_no
--    at an asset. The policy checks the organization only: the table is read
--    only joined with search_document, whose policy (000067) applies client
--    and site, so a write from a trigger never fails for a missing document.
--    No expression index (K22).
-- 2. search_document_field.source: 'indexer' rows are replaced by
--    PGRepository.IndexDocument(s), 'trigger' rows (the fields of an asset)
--    by the trigger of their entity only.
-- 3. Helper functions shared by the triggers, the backfill and
--    PGRepository.ReindexTenant: the text of a custom field value (scalars,
--    arrays element by element), whether an attribute is searchable for a
--    CI type (definitions of the type, inheritable ones of its ancestors
--    (ci_type_ancestors, WP-248), org-global and system ones), the summary
--    and the field rows of an asset, the identifier rows of an
--    organization.
-- 4. search_index_asset keeps the document of an asset, its field rows, its
--    inventory number and the inventory number at the CIs linked to it
--    (ci.asset_id) on every write (pattern of 000132/000182). Its trigger
--    sorts after trg_asset_sync_primary_ci (000088): AFTER triggers of one
--    event fire in name order, and ci.asset_id follows asset.ci_id there.
-- 5. search_identifier_interface and search_identifier_ip keep network.mac
--    and network.ip, one row per source row. The UPDATE triggers fire only
--    on a changed MAC/address or owner, so the empty updates of the scope
--    propagation (000061) and an unchanged discovery cycle change nothing.
-- 6. Backfill: asset documents and their field rows, the searchable
--    attributes of the CIs and all identifiers, without a reindex job.

CREATE TABLE search_identifier (
    id              UUID NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    entity_type     TEXT NOT NULL CHECK (entity_type IN ('ci', 'asset')),
    entity_id       UUID NOT NULL,
    field           TEXT NOT NULL,
    source_id       UUID NULL,
    norm            TEXT NOT NULL,
    addr            INET NULL
);

ALTER TABLE search_identifier ENABLE ROW LEVEL SECURITY;
ALTER TABLE search_identifier FORCE ROW LEVEL SECURITY;
CREATE POLICY search_identifier_tenant_isolation ON search_identifier FOR ALL
    USING (organization_id = current_setting('app.org_id')::uuid)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON search_identifier
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

CREATE INDEX IF NOT EXISTS idx_search_identifier_norm ON search_identifier (norm text_pattern_ops);
CREATE INDEX IF NOT EXISTS idx_search_identifier_addr ON search_identifier (addr) WHERE addr IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_search_identifier_entity ON search_identifier (organization_id, entity_type, entity_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_search_identifier_source ON search_identifier (source_id, field, entity_id) WHERE source_id IS NOT NULL;

ALTER TABLE search_document_field ADD COLUMN source TEXT NOT NULL DEFAULT 'indexer'
    CONSTRAINT search_document_field_source_check CHECK (source IN ('indexer', 'trigger'));

-- search_field_text is the searchable text of a custom field value: a
-- string, number or boolean as its text, an array its scalar elements
-- separated by blanks; anything else none. ci.IndexDocumentFor renders the
-- attributes of a CI alike.
CREATE FUNCTION search_field_text(v jsonb) RETURNS text
LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE jsonb_typeof(v)
        WHEN 'string' THEN v #>> '{}'
        WHEN 'number' THEN v #>> '{}'
        WHEN 'boolean' THEN v #>> '{}'
        WHEN 'array' THEN (SELECT string_agg(e.value #>> '{}', ' ' ORDER BY e.ord)
                             FROM jsonb_array_elements(v) WITH ORDINALITY AS e(value, ord)
                            WHERE jsonb_typeof(e.value) IN ('string', 'number', 'boolean') AND e.value #>> '{}' <> '')
    END
$$;

-- search_attribute_searchable reports whether the attribute key is
-- searchable for a CI of the type in the organization: a searchable
-- definition of the type, an inheritable one of an ancestor, an org-global
-- or a system one (the rule of attributeFilterOf, WP-566, with WP-248).
CREATE FUNCTION search_attribute_searchable(p_org uuid, p_type uuid, p_key text) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT EXISTS (
        SELECT 1 FROM attribute_definition d
         WHERE d.key = p_key AND d.searchable
           AND (d.organization_id IS NULL OR d.organization_id = p_org)
           AND (d.scope = 'global'
                OR d.ci_type_id = p_type
                OR (d.inheritable AND d.ci_type_id IN (SELECT a.id FROM ci_type_ancestors(p_type) a))))
$$;

-- search_ci_attribute_fields are the field rows attributes.<key> of the
-- searchable attributes of a CI with a text.
CREATE FUNCTION search_ci_attribute_fields(c ci) RETURNS TABLE (field text, content text)
LANGUAGE sql STABLE AS $$
    SELECT 'attributes.' || a.key, search_field_text(a.value)
      FROM jsonb_each(CASE WHEN jsonb_typeof(c.attributes) = 'object' THEN c.attributes ELSE '{}'::jsonb END) AS a(key, value)
     WHERE search_field_text(a.value) <> '' AND search_attribute_searchable(c.organization_id, c.ci_type_id, a.key)
$$;

-- search_asset_summary is the summary of the document of an asset.
CREATE FUNCTION search_asset_summary(a asset) RETURNS text
LANGUAGE sql STABLE AS $$
    SELECT concat_ws(' ', a.inventory_no, a.category, a.status, a.supplier, a.serial_number, a.location, a.notes)
$$;

-- search_asset_fields are the field rows of an asset: its summary columns
-- (field keys of WP-266, the inventory number under the key of
-- permission.FieldKeyOf) and every custom field with a text (E-134 (a)).
CREATE FUNCTION search_asset_fields(a asset) RETURNS TABLE (field text, content text)
LANGUAGE sql STABLE AS $$
    SELECT f.field, f.content
      FROM (VALUES ('core.asset_tag', a.inventory_no), ('core.category', a.category), ('core.status', a.status),
                   ('core.supplier', a.supplier), ('core.serial_number', a.serial_number), ('core.location', a.location),
                   ('core.notes', a.notes)) AS f(field, content)
     WHERE f.content <> ''
    UNION ALL
    SELECT 'attributes.' || c.key, search_field_text(c.value)
      FROM jsonb_each(CASE WHEN jsonb_typeof(a.custom_fields) = 'object' THEN a.custom_fields ELSE '{}'::jsonb END) AS c(key, value)
     WHERE search_field_text(c.value) <> ''
$$;

-- search_mac_norm is the normal form of a MAC.
CREATE FUNCTION search_mac_norm(m macaddr) RETURNS text
LANGUAGE sql IMMUTABLE AS $$
    SELECT replace(m::text, ':', '')
$$;

-- search_identifier_rows are the identifier rows of an organization, as
-- the indexer and the triggers keep them.
CREATE FUNCTION search_identifier_rows(p_org uuid)
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

CREATE FUNCTION search_index_asset() RETURNS trigger
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

CREATE FUNCTION search_identifier_interface() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM search_identifier
         WHERE organization_id = OLD.organization_id AND field = 'network.mac' AND source_id = OLD.id;
        RETURN OLD;
    END IF;
    IF NEW.mac IS NULL THEN
        DELETE FROM search_identifier
         WHERE organization_id = NEW.organization_id AND field = 'network.mac' AND source_id = NEW.id;
    ELSE
        DELETE FROM search_identifier
         WHERE organization_id = NEW.organization_id AND field = 'network.mac' AND source_id = NEW.id AND entity_id <> NEW.ci_id;
        INSERT INTO search_identifier (organization_id, entity_type, entity_id, field, source_id, norm)
        VALUES (NEW.organization_id, 'ci', NEW.ci_id, 'network.mac', NEW.id, search_mac_norm(NEW.mac))
        ON CONFLICT (source_id, field, entity_id) WHERE source_id IS NOT NULL
            DO UPDATE SET norm = EXCLUDED.norm WHERE search_identifier.norm <> EXCLUDED.norm;
    END IF;
    IF TG_OP = 'UPDATE' AND OLD.ci_id IS DISTINCT FROM NEW.ci_id THEN
        UPDATE search_identifier s SET entity_id = NEW.ci_id
          FROM ip_address i
         WHERE s.organization_id = NEW.organization_id AND s.field = 'network.ip' AND s.source_id = i.id
           AND i.interface_id = NEW.id AND s.entity_id <> NEW.ci_id;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER search_identifier_interface_update AFTER UPDATE OF mac, ci_id ON network_interface
    FOR EACH ROW WHEN (OLD.mac IS DISTINCT FROM NEW.mac OR OLD.ci_id IS DISTINCT FROM NEW.ci_id)
    EXECUTE FUNCTION search_identifier_interface();
CREATE TRIGGER search_identifier_interface_write AFTER INSERT OR DELETE ON network_interface
    FOR EACH ROW EXECUTE FUNCTION search_identifier_interface();

CREATE FUNCTION search_identifier_ip() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    ci_owner uuid;
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM search_identifier
         WHERE organization_id = OLD.organization_id AND field = 'network.ip' AND source_id = OLD.id;
        RETURN OLD;
    END IF;
    IF NEW.interface_id IS NOT NULL THEN
        SELECT n.ci_id INTO ci_owner FROM network_interface n WHERE n.id = NEW.interface_id;
    END IF;
    IF ci_owner IS NULL THEN
        DELETE FROM search_identifier
         WHERE organization_id = NEW.organization_id AND field = 'network.ip' AND source_id = NEW.id;
        RETURN NEW;
    END IF;
    DELETE FROM search_identifier
     WHERE organization_id = NEW.organization_id AND field = 'network.ip' AND source_id = NEW.id AND entity_id <> ci_owner;
    INSERT INTO search_identifier (organization_id, entity_type, entity_id, field, source_id, norm, addr)
    VALUES (NEW.organization_id, 'ci', ci_owner, 'network.ip', NEW.id, host(NEW.address), host(NEW.address)::inet)
    ON CONFLICT (source_id, field, entity_id) WHERE source_id IS NOT NULL
        DO UPDATE SET norm = EXCLUDED.norm, addr = EXCLUDED.addr
        WHERE search_identifier.norm <> EXCLUDED.norm OR search_identifier.addr IS DISTINCT FROM EXCLUDED.addr;
    RETURN NEW;
END
$$;

CREATE TRIGGER search_identifier_ip_update AFTER UPDATE OF address, interface_id ON ip_address
    FOR EACH ROW WHEN (OLD.address IS DISTINCT FROM NEW.address OR OLD.interface_id IS DISTINCT FROM NEW.interface_id)
    EXECUTE FUNCTION search_identifier_ip();
CREATE TRIGGER search_identifier_ip_write AFTER INSERT OR DELETE ON ip_address
    FOR EACH ROW EXECUTE FUNCTION search_identifier_ip();

-- Backfill. Every asset has its document; its field rows are those of the
-- trigger.
INSERT INTO search_document (organization_id, entity_type, entity_id, title, summary, url, metadata)
SELECT a.organization_id, 'asset', a.id, a.name, search_asset_summary(a), '/assets', '{}'::jsonb FROM asset a
ON CONFLICT (organization_id, entity_type, entity_id) DO UPDATE
    SET title = EXCLUDED.title, summary = EXCLUDED.summary, url = EXCLUDED.url;
DELETE FROM search_document_field f USING search_document d
 WHERE d.id = f.document_id AND d.entity_type = 'asset';
INSERT INTO search_document_field (organization_id, document_id, field, content, source)
SELECT d.organization_id, d.id, f.field, f.content, 'trigger'
  FROM search_document d JOIN asset e ON e.id = d.entity_id AND e.organization_id = d.organization_id
 CROSS JOIN LATERAL search_asset_fields(e) AS f
 WHERE d.entity_type = 'asset';
-- The searchable attributes of the CIs.
INSERT INTO search_document_field (organization_id, document_id, field, content)
SELECT d.organization_id, d.id, f.field, f.content
  FROM search_document d JOIN ci e ON e.id = d.entity_id AND e.organization_id = d.organization_id
 CROSS JOIN LATERAL search_ci_attribute_fields(e) AS f
 WHERE d.entity_type = 'ci'
ON CONFLICT (document_id, field) DO NOTHING;
-- The identifiers.
INSERT INTO search_identifier (organization_id, entity_type, entity_id, field, source_id, norm, addr)
SELECT o.id, r.entity_type, r.entity_id, r.field, r.source_id, r.norm, r.addr
  FROM organization o CROSS JOIN LATERAL search_identifier_rows(o.id) AS r;
