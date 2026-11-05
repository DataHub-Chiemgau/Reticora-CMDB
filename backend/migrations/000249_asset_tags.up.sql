-- Migration 000249: tag assignments of assets from the tag catalog (API-06,
-- WP-755, E-38; catalog of WP-281).
--
-- 1. asset_tag_link assigns tags of the catalog to assets (pattern ci_tag,
--    000218; the name asset_tag is the inventory number of WP-271/WP-738):
--    the asset with the organization and ON DELETE CASCADE (K19), the tag
--    with the organization without an action (an assigned tag cannot be
--    deleted, the organization still can).
-- 2. client_id and site_id follow the asset (derive_asset_tag_link_scope,
--    pattern derive_disposal_record_scope of 000145, the site of the asset
--    since 000151); propagate_asset_module_scope (version of 000217, scope
--    widened) re-derives them when the asset changes client or site.
-- 3. asset_version_bump (version of 000241) also raises the version when
--    the transaction sets app.asset_version_touch to 'on': a change of the
--    tag assignments (PUT /api/v1/assets/{id}/tags, POST /api/v1/tags/{id}/
--    merge) changes no column of the asset but is a new version (ETag).
-- 4. Policies like ci_tag (asset_tag_link_isolation with the client and
--    site predicate of E-09, asset_tag_link_scoped_update,
--    asset_tag_link_scoped_delete), ENABLE + FORCE RLS, TRUNCATE guard
--    (000144).
-- 5. Data migration from asset.custom_fields.tags (section "Backfill",
--    executed by asset/tags_integration_test.go): only arrays, of them only
--    string elements, key rule tag_normalize_key (000218), empty keys drop;
--    missing tags are created in the catalog (display name de/en the first
--    original text); then custom_fields loses tags and saved views of
--    assets filter on the normalized keys. trg_asset_updated_at and
--    asset_version_bump are disabled for the update (pattern 000107):
--    updated_at and version stay.

-- ─── 1. Assignments ────────────────────────────────────────────────────────────

CREATE TABLE asset_tag_link (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    asset_id        UUID NOT NULL,
    tag_id          UUID NOT NULL,
    client_id       UUID,
    site_id         UUID,
    created_by      UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT asset_tag_link_asset_id_tag_id_key UNIQUE (asset_id, tag_id),
    CONSTRAINT asset_tag_link_asset_fkey FOREIGN KEY (asset_id, organization_id)
        REFERENCES asset (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT asset_tag_link_tag_fkey FOREIGN KEY (tag_id, organization_id)
        REFERENCES tag (id, organization_id)
);

CREATE INDEX idx_asset_tag_link_org_tag ON asset_tag_link (organization_id, tag_id);

-- ─── 2. Scope derivation and propagation ───────────────────────────────────────

CREATE FUNCTION derive_asset_tag_link_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id
      FROM asset a WHERE a.id = NEW.asset_id AND a.organization_id = NEW.organization_id;
    RETURN NEW;
END
$$;

CREATE TRIGGER asset_tag_link_scope BEFORE INSERT OR UPDATE ON asset_tag_link
    FOR EACH ROW EXECUTE FUNCTION derive_asset_tag_link_scope();

-- A no-op UPDATE re-runs the derivation triggers of the dependent rows
-- (version of 000217 with asset_tag_link). The trigger
-- asset_module_scope_propagation fires on client_id and site_id (000217).
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
    UPDATE asset_tag_link SET asset_id = asset_id WHERE asset_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

-- ─── 3. Version of a tag change ────────────────────────────────────────────────

CREATE OR REPLACE FUNCTION asset_version_bump() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF (to_jsonb(NEW) - 'updated_at' - 'version' - 'serial_number_norm')
       IS DISTINCT FROM (to_jsonb(OLD) - 'updated_at' - 'version' - 'serial_number_norm')
       OR current_setting('app.asset_version_touch', true) = 'on' THEN
        NEW.version := OLD.version + 1;
    ELSE
        NEW.version := OLD.version;
    END IF;
    RETURN NEW;
END
$$;

-- ─── 4. Policies ───────────────────────────────────────────────────────────────

ALTER TABLE asset_tag_link ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_tag_link FORCE ROW LEVEL SECURITY;

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
    EXECUTE format(
        'CREATE POLICY asset_tag_link_isolation ON asset_tag_link '
        'USING (%1$s AND (%2$s OR client_id IS NULL OR client_id = ANY (%3$s)) '
            'AND (%4$s OR site_id IS NULL OR site_id = ANY (%5$s))) '
        'WITH CHECK (%6$s)',
        org_match, client_all, client_ids, site_all, site_ids, writable);
    EXECUTE format('CREATE POLICY asset_tag_link_scoped_update ON asset_tag_link AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                   writable, writable);
    EXECUTE format('CREATE POLICY asset_tag_link_scoped_delete ON asset_tag_link AS RESTRICTIVE FOR DELETE USING (%s)', writable);
END
$$;

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON asset_tag_link
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── 5. Backfill ───────────────────────────────────────────────────────────────

-- Only string elements of arrays.
CREATE TEMPORARY TABLE asset_tag_backfill AS
SELECT a.organization_id, a.id AS asset_id, el.value #>> '{}' AS original,
       tag_normalize_key(el.value #>> '{}') AS key, a.created_at, el.ord
FROM asset a
CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(a.custom_fields -> 'tags') = 'array'
                                             THEN a.custom_fields -> 'tags' ELSE '[]'::jsonb END)
    WITH ORDINALITY AS el(value, ord)
WHERE jsonb_typeof(el.value) = 'string';

DELETE FROM asset_tag_backfill WHERE key = '';

-- Missing catalog tags; the display name is the first original text.
INSERT INTO tag (organization_id, key, display_name)
SELECT DISTINCT ON (organization_id, key) organization_id, key, jsonb_build_object('de', original, 'en', original)
FROM asset_tag_backfill
ORDER BY organization_id, key, created_at, asset_id, ord
ON CONFLICT (organization_id, key) DO NOTHING;

INSERT INTO asset_tag_link (organization_id, asset_id, tag_id)
SELECT DISTINCT b.organization_id, b.asset_id, t.id
FROM asset_tag_backfill b JOIN tag t ON t.organization_id = b.organization_id AND t.key = b.key
ON CONFLICT (asset_id, tag_id) DO NOTHING;

DROP TABLE asset_tag_backfill;

-- custom_fields without tags, without updated_at or version change.
ALTER TABLE asset DISABLE TRIGGER trg_asset_updated_at;
ALTER TABLE asset DISABLE TRIGGER asset_version_bump;
UPDATE asset SET custom_fields = custom_fields - 'tags' WHERE custom_fields ? 'tags';
ALTER TABLE asset ENABLE TRIGGER asset_version_bump;
ALTER TABLE asset ENABLE TRIGGER trg_asset_updated_at;

-- Saved views of assets filter on the catalog keys.
UPDATE saved_view SET filter_spec = jsonb_set(filter_spec, '{tags}',
    (SELECT COALESCE(jsonb_agg(CASE WHEN jsonb_typeof(e.value) = 'string'
                                    THEN to_jsonb(tag_normalize_key(e.value #>> '{}')) ELSE e.value END ORDER BY e.ord), '[]'::jsonb)
     FROM jsonb_array_elements(filter_spec -> 'tags') WITH ORDINALITY AS e(value, ord)))
WHERE entity_kind = 'asset' AND jsonb_typeof(filter_spec -> 'tags') = 'array';

-- ─── end of backfill ───────────────────────────────────────────────────────────
