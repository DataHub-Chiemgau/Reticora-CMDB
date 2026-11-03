-- Migration 000219: import mapping profiles (WP-774, IMP-IO-01, API-06, E-38, E-49).
--
-- 1. import_profile holds a stored mapping of an import per organization:
--    entity and file format of the import, mode (create_missing,
--    update_existing or both), the sheet of an xlsx file, the mapping
--    (jsonb object: per source column a target with an ordered list of
--    transformations, defaults per target) and the identity keys. The
--    structure of mapping is checked by the application (importer.Profile);
--    the targets are checked against the metamodel when an import runs
--    (WP-302).
-- 2. The name is unique per organization (409 in the API).
-- 3. created_by is a surrogate without foreign key, like job.created_by. The
--    table references neither client nor object tables.
-- 4. Row level security with ENABLE and FORCE and one policy FOR ALL
--    (pattern 000115); truncate guard of WP-352. Grants for reticora_app
--    come from the default privileges of 000056.

CREATE TABLE import_profile (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT,
    entity          TEXT NOT NULL
        CONSTRAINT import_profile_entity_check CHECK (entity IN ('ci', 'asset', 'contract', 'consumable', 'location', 'relationship')),
    format          TEXT NOT NULL
        CONSTRAINT import_profile_format_check CHECK (format IN ('csv', 'xlsx', 'json')),
    mode            TEXT NOT NULL
        CONSTRAINT import_profile_mode_check CHECK (mode IN ('create_missing', 'update_existing', 'both')),
    sheet           TEXT,
    mapping         JSONB NOT NULL
        CONSTRAINT import_profile_mapping_check CHECK (jsonb_typeof(mapping) = 'object'),
    identity_keys   TEXT[] NOT NULL DEFAULT '{}',
    created_by      UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT import_profile_organization_id_name_key UNIQUE (organization_id, name)
);
CREATE TRIGGER trg_import_profile_updated_at BEFORE UPDATE ON import_profile
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE import_profile ENABLE ROW LEVEL SECURITY;
ALTER TABLE import_profile FORCE ROW LEVEL SECURITY;
CREATE POLICY import_profile_isolation ON import_profile FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON import_profile
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
