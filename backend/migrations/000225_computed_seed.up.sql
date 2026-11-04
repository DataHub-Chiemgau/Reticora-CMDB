-- Migration 000225: system definition warranty_days_left (WP-718, MET-10
-- computed, JOB-02 [P2], VIE-02).
--
-- A computed CI attribute of the system (organization_id NULL, no CI type,
-- scope global, read-only for the application): the days from today (in
-- organization.timezone) to warranty_end of the asset linked through
-- ci.asset_id, empty without an asset or a date ($defs/expr of
-- api/rules.schema.json). Assets have no attribute definitions; the value
-- belongs to the CI and is filterable as attributes.warranty_days_left.
--
-- trg_attribute_definition_regenerate regenerates every type schema (a
-- system global): each gets the attribute with readOnly and x-computed and
-- a new schema_version. The values are written by the first run of the
-- refresh job attribute.computed_refresh, not here.
--
-- No index and no index job (K22, SR-04): the attribute index job skips
-- system definitions (DDLJob.Ensure), so only a migration could build the
-- index of a system definition, and this one builds none. The key index
-- idx_attr_key_warranty_days_left (000164) can serve the filter once an
-- organization defines a searchable, non-unique global attribute of the
-- same key.

INSERT INTO attribute_definition (id, organization_id, ci_type_id, scope, key, display_name, description,
    data_type, searchable, computed)
VALUES ('a7180000-0000-4000-8000-000000000001', NULL, NULL, 'global', 'warranty_days_left',
    '{"de": "Garantie (Resttage)", "en": "Warranty days left"}'::jsonb,
    'Days from today until the warranty end of the linked asset; empty without an asset or a warranty end (WP-718).',
    'integer', true,
    '{"op": "days_between", "args": [{"op": "today"}, {"op": "field", "field": "asset.warranty_end"}]}'::jsonb);
