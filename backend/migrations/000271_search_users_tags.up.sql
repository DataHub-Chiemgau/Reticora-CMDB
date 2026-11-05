-- Migration 000271: search over tags and users (SRC-02, WP-788).
--
-- 1. Tags: every ci and asset document gets the field row core.tags
--    (source 'trigger') with the key and every language value of the
--    display name of each assigned tag (search_tag_text). The triggers of
--    ci_tag and asset_tag_link rebuild the row of the affected document
--    with one statement (search_refresh_tags); their UPDATE triggers fire
--    on a changed ci_id/asset_id or tag_id only, since the scope
--    propagation (000218, 000249) writes empty updates. The trigger of tag
--    renews the rows of all documents of a renamed tag in one statement;
--    under the policy of search_document_field it reaches the documents in
--    the scope of the writer, the others the next reindex job (WP-786).
--    PGRepository.IndexDocuments writes the row of a ci document as well:
--    a CI and its tags are written before its document exists.
-- 2. Users: only the surrogate (app_user.id) is indexed, never a name or an
--    e-mail address (SEC-05). search_identifier gets core.owner_user_id at
--    a CI (from the indexer and, for the owner mirrored from the asset by
--    asset_propagate_owner, from the trigger of asset), core.owner_user_id
--    and core.assigned_user_id at an asset, and asset.assigned_user_id (the
--    holder of the linked asset, source_id = asset) at every CI with
--    ci.asset_id = the asset. search_index_asset keeps them on a change of
--    owner_user_id, assigned_user_id or ci_id; the part on the CIs of the
--    asset reads them organization-wide like asset_propagate_owner, so a
--    CI outside the scope of the writer keeps its rows.
-- 3. search_identifier_rows (reindex) returns the user identifiers too.
-- 4. Backfill of core.tags and of the user identifiers. No new table, no
--    change of a policy.

-- search_tag_text is the text of the tags of a ci or asset: per tag its
-- key and the values of its display name, ordered by key; NULL without a
-- tag.
CREATE FUNCTION search_tag_text(p_type text, p_id uuid) RETURNS text
LANGUAGE sql STABLE AS $$
    SELECT string_agg(concat_ws(' ', t.key, t.display_name ->> 'de', t.display_name ->> 'en'), ' ' ORDER BY t.key)
      FROM tag t
     WHERE t.id IN (SELECT ct.tag_id FROM ci_tag ct WHERE p_type = 'ci' AND ct.ci_id = p_id
                    UNION ALL
                    SELECT l.tag_id FROM asset_tag_link l WHERE p_type = 'asset' AND l.asset_id = p_id)
$$;

-- search_refresh_tags rebuilds the field rows core.tags of the documents of
-- the entities with one statement: a row per document with tags, none
-- without. An entity named twice is refreshed once.
CREATE FUNCTION search_refresh_tags(p_org uuid, p_types text[], p_ids uuid[]) RETURNS void
LANGUAGE sql AS $$
    WITH d AS (
        SELECT sd.id, search_tag_text(sd.entity_type, sd.entity_id) AS content
          FROM search_document sd
          JOIN (SELECT DISTINCT * FROM unnest(p_types, p_ids)) AS i(entity_type, entity_id)
            ON sd.organization_id = p_org AND sd.entity_type = i.entity_type AND sd.entity_id = i.entity_id
    ), gone AS (
        DELETE FROM search_document_field f USING d
         WHERE f.organization_id = p_org AND f.document_id = d.id AND f.field = 'core.tags' AND d.content IS NULL
    )
    INSERT INTO search_document_field (organization_id, document_id, field, content, source)
    SELECT p_org, d.id, 'core.tags', d.content, 'trigger' FROM d WHERE d.content IS NOT NULL
    ON CONFLICT (document_id, field) DO UPDATE SET content = EXCLUDED.content, source = 'trigger'
        WHERE search_document_field.content <> EXCLUDED.content OR search_document_field.source <> 'trigger'
$$;

CREATE FUNCTION search_index_ci_tag() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM search_refresh_tags(NEW.organization_id, ARRAY['ci'], ARRAY[NEW.ci_id]);
    ELSIF TG_OP = 'DELETE' THEN
        PERFORM search_refresh_tags(OLD.organization_id, ARRAY['ci'], ARRAY[OLD.ci_id]);
    ELSE
        PERFORM search_refresh_tags(NEW.organization_id, ARRAY['ci', 'ci'], ARRAY[OLD.ci_id, NEW.ci_id]);
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER search_index_ci_tag_write AFTER INSERT OR DELETE ON ci_tag
    FOR EACH ROW EXECUTE FUNCTION search_index_ci_tag();
CREATE TRIGGER search_index_ci_tag_update AFTER UPDATE OF ci_id, tag_id ON ci_tag
    FOR EACH ROW WHEN (OLD.ci_id IS DISTINCT FROM NEW.ci_id OR OLD.tag_id IS DISTINCT FROM NEW.tag_id)
    EXECUTE FUNCTION search_index_ci_tag();

CREATE FUNCTION search_index_asset_tag() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM search_refresh_tags(NEW.organization_id, ARRAY['asset'], ARRAY[NEW.asset_id]);
    ELSIF TG_OP = 'DELETE' THEN
        PERFORM search_refresh_tags(OLD.organization_id, ARRAY['asset'], ARRAY[OLD.asset_id]);
    ELSE
        PERFORM search_refresh_tags(NEW.organization_id, ARRAY['asset', 'asset'], ARRAY[OLD.asset_id, NEW.asset_id]);
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER search_index_asset_tag_write AFTER INSERT OR DELETE ON asset_tag_link
    FOR EACH ROW EXECUTE FUNCTION search_index_asset_tag();
CREATE TRIGGER search_index_asset_tag_update AFTER UPDATE OF asset_id, tag_id ON asset_tag_link
    FOR EACH ROW WHEN (OLD.asset_id IS DISTINCT FROM NEW.asset_id OR OLD.tag_id IS DISTINCT FROM NEW.tag_id)
    EXECUTE FUNCTION search_index_asset_tag();

-- A renamed tag renews the rows of all its CIs and assets in one statement.
CREATE FUNCTION search_index_tag() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM search_refresh_tags(NEW.organization_id, array_agg(x.entity_type), array_agg(x.entity_id))
       FROM (SELECT 'ci' AS entity_type, ct.ci_id AS entity_id FROM ci_tag ct WHERE ct.tag_id = NEW.id
             UNION ALL
             SELECT 'asset', l.asset_id FROM asset_tag_link l WHERE l.tag_id = NEW.id) AS x
     HAVING count(*) > 0;
    RETURN NULL;
END
$$;

CREATE TRIGGER search_index_tag_update AFTER UPDATE OF key, display_name ON tag
    FOR EACH ROW WHEN (OLD.key IS DISTINCT FROM NEW.key OR OLD.display_name IS DISTINCT FROM NEW.display_name)
    EXECUTE FUNCTION search_index_tag();

-- The identifier rows of an organization (version of 000233 with the user
-- identifiers).
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
    UNION ALL
    SELECT 'ci', c.id, 'core.owner_user_id', NULL::uuid, c.owner_user_id::text, NULL::inet
      FROM ci c WHERE c.organization_id = p_org AND c.deleted_at IS NULL AND c.owner_user_id IS NOT NULL
    UNION ALL
    SELECT 'ci', c.id, 'asset.assigned_user_id', a.id, a.assigned_user_id::text, NULL::inet
      FROM ci c JOIN asset a ON a.id = c.asset_id AND a.organization_id = c.organization_id
     WHERE c.organization_id = p_org AND c.deleted_at IS NULL AND a.assigned_user_id IS NOT NULL
    UNION ALL
    SELECT 'asset', a.id, 'core.owner_user_id', NULL::uuid, a.owner_user_id::text, NULL::inet
      FROM asset a WHERE a.organization_id = p_org AND a.owner_user_id IS NOT NULL
    UNION ALL
    SELECT 'asset', a.id, 'core.assigned_user_id', NULL::uuid, a.assigned_user_id::text, NULL::inet
      FROM asset a WHERE a.organization_id = p_org AND a.assigned_user_id IS NOT NULL
$$;

-- search_index_asset (version of 000233): the field rows of the asset keep
-- core.tags, which the triggers of asset_tag_link own; owner and holder
-- of the asset and at its CIs.
CREATE OR REPLACE FUNCTION search_index_asset() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    doc_id uuid;
    inv text;
    previous text[];
    previous_team text;
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM search_document
         WHERE organization_id = OLD.organization_id AND entity_type = 'asset' AND entity_id = OLD.id;
        DELETE FROM search_identifier
         WHERE organization_id = OLD.organization_id
           AND ((entity_type = 'asset' AND entity_id = OLD.id)
                OR (field IN ('asset.inventory_no', 'asset.assigned_user_id') AND source_id = OLD.id));
        RETURN OLD;
    END IF;
    INSERT INTO search_document (organization_id, entity_type, entity_id, title, summary, url, metadata)
    VALUES (NEW.organization_id, 'asset', NEW.id, NEW.name, search_asset_summary(NEW), '/assets', '{}'::jsonb)
    ON CONFLICT (organization_id, entity_type, entity_id) DO UPDATE
        SET title = EXCLUDED.title, summary = EXCLUDED.summary, url = EXCLUDED.url, updated_at = now()
    RETURNING id INTO doc_id;

    DELETE FROM search_document_field s
     WHERE s.organization_id = NEW.organization_id AND s.document_id = doc_id AND s.field <> 'core.tags'
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

    IF TG_OP = 'INSERT' OR OLD.owner_user_id IS DISTINCT FROM NEW.owner_user_id
       OR OLD.assigned_user_id IS DISTINCT FROM NEW.assigned_user_id OR OLD.ci_id IS DISTINCT FROM NEW.ci_id THEN
        -- Owner and holder of the asset.
        DELETE FROM search_identifier s
         WHERE s.organization_id = NEW.organization_id AND s.entity_type = 'asset' AND s.entity_id = NEW.id
           AND s.field IN ('core.owner_user_id', 'core.assigned_user_id')
           AND s.norm IS DISTINCT FROM (CASE s.field WHEN 'core.owner_user_id' THEN NEW.owner_user_id::text
                                                     ELSE NEW.assigned_user_id::text END);
        INSERT INTO search_identifier (organization_id, entity_type, entity_id, field, norm)
        SELECT NEW.organization_id, 'asset', NEW.id, u.field, u.norm
          FROM (VALUES ('core.owner_user_id', NEW.owner_user_id::text),
                       ('core.assigned_user_id', NEW.assigned_user_id::text)) AS u(field, norm)
         WHERE u.norm IS NOT NULL AND NOT EXISTS (SELECT 1 FROM search_identifier s
              WHERE s.organization_id = NEW.organization_id AND s.entity_type = 'asset' AND s.entity_id = NEW.id
                AND s.field = u.field);

        -- The holder and the mirrored owner at the CIs of the asset, read
        -- organization-wide (pattern asset_propagate_owner).
        previous := app_scope_widen();
        previous_team := current_setting('app.team_scope', true);
        PERFORM set_config('app.team_scope', '', true);
        DELETE FROM search_identifier s
         WHERE s.organization_id = NEW.organization_id AND s.field = 'asset.assigned_user_id' AND s.source_id = NEW.id
           AND (NEW.assigned_user_id IS NULL OR s.norm <> NEW.assigned_user_id::text
                OR NOT EXISTS (SELECT 1 FROM ci c WHERE c.id = s.entity_id AND c.asset_id = NEW.id AND c.deleted_at IS NULL));
        INSERT INTO search_identifier (organization_id, entity_type, entity_id, field, source_id, norm)
        SELECT NEW.organization_id, 'ci', c.id, 'asset.assigned_user_id', NEW.id, NEW.assigned_user_id::text
          FROM ci c
         WHERE c.organization_id = NEW.organization_id AND c.asset_id = NEW.id AND c.deleted_at IS NULL
           AND NEW.assigned_user_id IS NOT NULL
        ON CONFLICT (source_id, field, entity_id) WHERE source_id IS NOT NULL DO NOTHING;
        DELETE FROM search_identifier s USING ci c
         WHERE s.organization_id = NEW.organization_id AND s.entity_type = 'ci' AND s.entity_id = c.id
           AND s.field = 'core.owner_user_id' AND c.asset_id = NEW.id AND c.organization_id = NEW.organization_id
           AND s.norm IS DISTINCT FROM c.owner_user_id::text;
        INSERT INTO search_identifier (organization_id, entity_type, entity_id, field, norm)
        SELECT NEW.organization_id, 'ci', c.id, 'core.owner_user_id', c.owner_user_id::text
          FROM ci c
         WHERE c.organization_id = NEW.organization_id AND c.asset_id = NEW.id AND c.deleted_at IS NULL
           AND c.owner_user_id IS NOT NULL
           AND NOT EXISTS (SELECT 1 FROM search_identifier s
                WHERE s.organization_id = NEW.organization_id AND s.entity_type = 'ci' AND s.entity_id = c.id
                  AND s.field = 'core.owner_user_id');
        PERFORM set_config('app.team_scope', COALESCE(previous_team, ''), true);
        PERFORM app_scope_restore(previous);
    END IF;
    RETURN NEW;
END
$$;

DROP TRIGGER trg_asset_sync_search_index ON asset;
CREATE TRIGGER trg_asset_sync_search_index
    AFTER INSERT OR UPDATE OF name, inventory_no, category, status, lifecycle_state, supplier, supplier_id,
        serial_number, location, notes, custom_fields, ci_id, owner_user_id, assigned_user_id OR DELETE ON asset
    FOR EACH ROW EXECUTE FUNCTION search_index_asset();

-- Backfill: core.tags of every ci and asset document, the user identifiers.
INSERT INTO search_document_field (organization_id, document_id, field, content, source)
SELECT d.organization_id, d.id, 'core.tags', t.content, 'trigger'
  FROM search_document d CROSS JOIN LATERAL (SELECT search_tag_text(d.entity_type, d.entity_id) AS content) AS t
 WHERE d.entity_type IN ('ci', 'asset') AND t.content IS NOT NULL
ON CONFLICT (document_id, field) DO UPDATE SET content = EXCLUDED.content, source = 'trigger';
INSERT INTO search_identifier (organization_id, entity_type, entity_id, field, source_id, norm, addr)
SELECT o.id, r.entity_type, r.entity_id, r.field, r.source_id, r.norm, r.addr
  FROM organization o CROSS JOIN LATERAL search_identifier_rows(o.id) AS r
 WHERE r.field IN ('core.owner_user_id', 'core.assigned_user_id', 'asset.assigned_user_id')
ON CONFLICT (source_id, field, entity_id) WHERE source_id IS NOT NULL DO NOTHING;
