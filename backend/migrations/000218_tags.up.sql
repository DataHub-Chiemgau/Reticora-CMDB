-- Migration 000218: tag catalog per organization and tag assignments of CIs
-- (CI-07, CI-08, CI-12; WP-281, E-125).
--
-- 1. tag is the catalog of an organization: key (lower case, digits, _ and
--    -, at most 63 characters, unique per organization), display_name in
--    de/en like ci_type (000104), optional color, is_system. It has no
--    entity-specific column, so tickets can use it later (WP-290).
-- 2. ci_tag assigns tags to CIs: the CI with the organization (K19, ON
--    DELETE CASCADE, the hard delete relies on it), the tag with the
--    organization without an action (an assigned tag cannot be deleted, the
--    organization still can). client_id and site_id follow the CI
--    (derive_ci_scope); propagate_ci_scope (version of 000209, scope widened)
--    re-derives them when the CI changes client or site.
-- 3. Policies: tag org-wide, ci_tag like ci_template (000206) and the CI
--    children of 000131; the TRUNCATE guard of the tenant tables (000144).
-- 4. The system tag archived for every organization; a new organization
--    gets it from organization_default_tags (pattern organization_default_vrf,
--    000092).
-- 5. Data migration from ci.attributes.tags and the overrides of the field
--    tags (section "Backfill", executed by tags_integration_test.go): the
--    effective value of every CI (deleted ones too) becomes ci_tag rows;
--    attributes.tags, the field values of tags, open override_conflict
--    reviews of tags and the tags in open merge snapshots go; saved views
--    filter on the normalized keys. version, schema_version and updated_at of
--    the CIs stay.
-- 6. CHECK ci_field_value_no_tags: tags are no field value any more; the
--    override path PUT /cis/{id}/fields/tags/override fails with 422 (E-125).

-- ─── 1. Catalog ────────────────────────────────────────────────────────────────

-- The key rule of the catalog: lower case, every run of other characters
-- than a-z, 0-9, _ and - becomes _, leading _ and - are removed, at most 63
-- characters. An empty result is no key (ci.TagKey implements the same).
CREATE FUNCTION tag_normalize_key(raw text) RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE
SET search_path = pg_catalog, public, pg_temp
AS $$
    SELECT left(regexp_replace(regexp_replace(lower(raw), '[^a-z0-9_-]+', '_', 'g'), '^[_-]+', ''), 63)
$$;

CREATE TABLE tag (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    key             TEXT NOT NULL CONSTRAINT tag_key_check CHECK (key ~ '^[a-z0-9][a-z0-9_-]{0,62}$'),
    display_name    JSONB NOT NULL,
    color           TEXT CONSTRAINT tag_color_check CHECK (color ~ '^#[0-9a-f]{6}$'),
    is_system       BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tag_organization_key_key UNIQUE (organization_id, key),
    CONSTRAINT tag_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT tag_display_name_i18n CHECK (
        jsonb_typeof(display_name) = 'object'
        AND display_name ?| ARRAY['de', 'en']
        AND (display_name - 'de' - 'en') = '{}'::jsonb
        AND (NOT display_name ? 'de' OR (jsonb_typeof(display_name -> 'de') = 'string' AND display_name ->> 'de' <> ''))
        AND (NOT display_name ? 'en' OR (jsonb_typeof(display_name -> 'en') = 'string' AND display_name ->> 'en' <> ''))
    )
);

CREATE TRIGGER trg_tag_updated_at BEFORE UPDATE ON tag
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ─── 2. Assignments ────────────────────────────────────────────────────────────

CREATE TABLE ci_tag (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    ci_id           UUID NOT NULL,
    tag_id          UUID NOT NULL,
    client_id       UUID,
    site_id         UUID,
    created_by      UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ci_tag_ci_id_tag_id_key UNIQUE (ci_id, tag_id),
    CONSTRAINT ci_tag_ci_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT ci_tag_tag_fkey FOREIGN KEY (tag_id, organization_id)
        REFERENCES tag (id, organization_id)
);

CREATE INDEX idx_ci_tag_org_tag ON ci_tag (organization_id, tag_id);

CREATE TRIGGER ci_tag_scope BEFORE INSERT OR UPDATE ON ci_tag
    FOR EACH ROW EXECUTE FUNCTION derive_ci_scope('ci_id');

-- A no-op UPDATE re-runs the derivation triggers of the dependent rows
-- (version of 000209 with ci_tag).
CREATE OR REPLACE FUNCTION propagate_ci_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE network_interface SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_contact SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_change SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_field_value SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_instance_field_definition SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE rack_mount SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE compliance_result SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE security_finding SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE discovery_result SET matched_ci_id = matched_ci_id WHERE matched_ci_id = NEW.id;
    UPDATE ci_relationship SET source_ci_id = source_ci_id WHERE source_ci_id = NEW.id OR target_ci_id = NEW.id;
    UPDATE relationship_suppression SET source_ci_id = source_ci_id WHERE source_ci_id = NEW.id OR target_ci_id = NEW.id;
    UPDATE ci_snapshot SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_tag SET ci_id = ci_id WHERE ci_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

-- ─── 3. Policies ───────────────────────────────────────────────────────────────

ALTER TABLE tag ENABLE ROW LEVEL SECURITY;
ALTER TABLE tag FORCE ROW LEVEL SECURITY;

CREATE POLICY tag_isolation ON tag
    USING (organization_id = current_setting('app.org_id')::uuid)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid);

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON tag
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE ci_tag ENABLE ROW LEVEL SECURITY;
ALTER TABLE ci_tag FORCE ROW LEVEL SECURITY;

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
        'CREATE POLICY ci_tag_isolation ON ci_tag '
        'USING (%1$s AND (%2$s OR client_id IS NULL OR client_id = ANY (%3$s)) '
            'AND (%4$s OR site_id IS NULL OR site_id = ANY (%5$s))) '
        'WITH CHECK (%6$s)',
        org_match, client_all, client_ids, site_all, site_ids, writable);
    EXECUTE format('CREATE POLICY ci_tag_scoped_update ON ci_tag AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                   writable, writable);
    EXECUTE format('CREATE POLICY ci_tag_scoped_delete ON ci_tag AS RESTRICTIVE FOR DELETE USING (%s)', writable);
END
$$;

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON ci_tag
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── 4. System tag ─────────────────────────────────────────────────────────────

-- Every new organization gets the system tag archived (basis of the bulk
-- action archive, WP-304). tag has FORCE RLS: the insert runs in the new
-- organization's tenant context, the caller's is restored.
CREATE FUNCTION organization_default_tags() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text := current_setting('app.org_id', true);
BEGIN
    PERFORM set_config('app.org_id', NEW.id::text, true);
    INSERT INTO tag (organization_id, key, display_name, is_system)
    VALUES (NEW.id, 'archived', '{"de": "Archiviert", "en": "Archived"}', true)
    ON CONFLICT (organization_id, key) DO NOTHING;
    PERFORM set_config('app.org_id', COALESCE(previous, ''), true);
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_organization_default_tags AFTER INSERT ON organization
    FOR EACH ROW EXECUTE FUNCTION organization_default_tags();

INSERT INTO tag (organization_id, key, display_name, is_system)
SELECT id, 'archived', '{"de": "Archiviert", "en": "Archived"}', true FROM organization
ON CONFLICT (organization_id, key) DO NOTHING;

-- ─── 5. Backfill ───────────────────────────────────────────────────────────────

-- The effective value of the field tags per CI, deleted CIs included: the
-- manual override, else attributes.tags. Only string elements of arrays.
CREATE TEMPORARY TABLE tag_backfill AS
SELECT c.organization_id, c.id AS ci_id, el.value #>> '{}' AS original,
       tag_normalize_key(el.value #>> '{}') AS key, c.created_at, el.ord
FROM ci c
LEFT JOIN ci_field_value fv ON fv.ci_id = c.id AND fv.field_name = 'tags' AND fv.override_at IS NOT NULL
CROSS JOIN LATERAL (SELECT CASE WHEN fv.ci_id IS NOT NULL THEN fv.override_value ELSE c.attributes -> 'tags' END AS tags) e
CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(e.tags) = 'array' THEN e.tags ELSE '[]'::jsonb END)
    WITH ORDINALITY AS el(value, ord)
WHERE jsonb_typeof(el.value) = 'string';

DELETE FROM tag_backfill WHERE key = '';

-- Missing catalog tags; the display name is the first original text.
INSERT INTO tag (organization_id, key, display_name)
SELECT DISTINCT ON (organization_id, key) organization_id, key, jsonb_build_object('de', original, 'en', original)
FROM tag_backfill
ORDER BY organization_id, key, created_at, ci_id, ord
ON CONFLICT (organization_id, key) DO NOTHING;

-- (1) The assignments.
INSERT INTO ci_tag (organization_id, ci_id, tag_id)
SELECT DISTINCT b.organization_id, b.ci_id, t.id
FROM tag_backfill b JOIN tag t ON t.organization_id = b.organization_id AND t.key = b.key
ON CONFLICT (ci_id, tag_id) DO NOTHING;

DROP TABLE tag_backfill;

-- (2) and (3) without schema version, updated_at or version change: the
-- delete of the field values projects ci.attributes again (000135).
ALTER TABLE ci DISABLE TRIGGER trg_ci_schema_version;
ALTER TABLE ci DISABLE TRIGGER trg_ci_updated_at;
UPDATE ci SET attributes = attributes - 'tags' WHERE attributes ? 'tags';
DELETE FROM ci_field_value WHERE field_name = 'tags';
ALTER TABLE ci ENABLE TRIGGER trg_ci_updated_at;
ALTER TABLE ci ENABLE TRIGGER trg_ci_schema_version;

-- (4) An accept of an open conflict would clear an override that is gone.
UPDATE review_item SET status = 'dismissed', action = 'dismiss', resolution = 'tags moved to the tag catalog (WP-281)',
       resolved_at = now()
WHERE kind = 'override_conflict' AND status = 'open' AND payload ->> 'field' = 'tags';

-- (5) Open merges: the unmerge must not bring tags back into attributes or
-- ci_field_value. The survivor keeps the united tags (known limitation).
CREATE FUNCTION tag_backfill_strip(attrs jsonb) RETURNS jsonb
LANGUAGE plpgsql IMMUTABLE
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    ns text;
BEGIN
    IF jsonb_typeof(attrs) IS DISTINCT FROM 'object' THEN
        RETURN attrs;
    END IF;
    attrs := attrs - 'tags';
    FOREACH ns IN ARRAY ARRAY['_overrides', '_observed', '_provenance'] LOOP
        IF jsonb_typeof(attrs -> ns) = 'object' THEN
            attrs := jsonb_set(attrs, ARRAY[ns], (attrs -> ns) - 'tags');
        END IF;
    END LOOP;
    RETURN attrs;
END
$$;

CREATE FUNCTION tag_backfill_strip_rows(snapshot_rows jsonb) RETURNS jsonb
LANGUAGE sql IMMUTABLE
SET search_path = pg_catalog, public, pg_temp
AS $$
    SELECT CASE WHEN jsonb_typeof(snapshot_rows) = 'array' THEN
        (SELECT COALESCE(jsonb_agg(r.value ORDER BY r.ord), '[]'::jsonb)
         FROM jsonb_array_elements(snapshot_rows) WITH ORDINALITY AS r(value, ord)
         WHERE NOT (r.value ->> 'table' = 'ci_field_value' AND r.value -> 'row' ->> 'field_name' = 'tags'))
    ELSE snapshot_rows END
$$;

UPDATE ci_merge SET snapshot = snapshot
    || CASE WHEN jsonb_typeof(snapshot -> 'survivor') = 'object'
            THEN jsonb_build_object('survivor', jsonb_set(snapshot -> 'survivor', '{attributes}',
                 COALESCE(tag_backfill_strip(snapshot -> 'survivor' -> 'attributes'), 'null'::jsonb), false))
            ELSE '{}'::jsonb END
    || CASE WHEN jsonb_typeof(snapshot -> 'victim') = 'object'
            THEN jsonb_build_object('victim', jsonb_set(snapshot -> 'victim', '{attributes}',
                 COALESCE(tag_backfill_strip(snapshot -> 'victim' -> 'attributes'), 'null'::jsonb), false))
            ELSE '{}'::jsonb END
    || CASE WHEN snapshot ? 'changed' THEN jsonb_build_object('changed', tag_backfill_strip_rows(snapshot -> 'changed'))
            ELSE '{}'::jsonb END
    || CASE WHEN snapshot ? 'removed' THEN jsonb_build_object('removed', tag_backfill_strip_rows(snapshot -> 'removed'))
            ELSE '{}'::jsonb END
WHERE unmerged_at IS NULL AND jsonb_typeof(snapshot) = 'object';

DROP FUNCTION tag_backfill_strip_rows(jsonb);
DROP FUNCTION tag_backfill_strip(jsonb);

-- (6) Saved views of CIs filter on the catalog keys.
UPDATE saved_view SET filter_spec = jsonb_set(filter_spec, '{tags}',
    (SELECT COALESCE(jsonb_agg(CASE WHEN jsonb_typeof(e.value) = 'string'
                                    THEN to_jsonb(tag_normalize_key(e.value #>> '{}')) ELSE e.value END ORDER BY e.ord), '[]'::jsonb)
     FROM jsonb_array_elements(filter_spec -> 'tags') WITH ORDINALITY AS e(value, ord)))
WHERE entity_kind = 'ci' AND jsonb_typeof(filter_spec -> 'tags') = 'array';

-- ─── end of backfill ───────────────────────────────────────────────────────────

-- ─── 6. No field value for tags ────────────────────────────────────────────────

ALTER TABLE ci_field_value ADD CONSTRAINT ci_field_value_no_tags CHECK (field_name <> 'tags');
