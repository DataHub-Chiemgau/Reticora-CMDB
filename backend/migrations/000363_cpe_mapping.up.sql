-- Migration 000363: the product mapping of CIs for the version match
-- (SEC-02, LCY-06, WP-1121, E-236 option 2).
--
-- cpe_mapping maps the CIs of an organization to a vendor and products of
-- the vulnerability catalog: by manufacturer and model (glob patterns,
-- case-insensitive) or by a sys_object_id prefix, with the version field
-- and the version scheme to compare. The rows of an organization go before
-- the system mapping in the code (security/cpe_system.go); within the
-- organization the lowest priority wins. Organization-wide configuration
-- without client or site: the policy checks app.org_id in USING and WITH
-- CHECK (like the rank table of WP-1105). created_by is a user id without
-- foreign key. No reference to another tenant table (Konvention 19).

CREATE TABLE cpe_mapping (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id       uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    match_kind            text NOT NULL CONSTRAINT cpe_mapping_match_kind_check CHECK (match_kind IN ('manufacturer_model', 'sys_object_id')),
    manufacturer_pattern  text NULL,
    model_pattern         text NULL,
    sys_object_id_prefix  text NULL,
    vendor                text NOT NULL CONSTRAINT cpe_mapping_vendor_check CHECK (btrim(vendor) <> ''),
    products              text[] NOT NULL CONSTRAINT cpe_mapping_products_check CHECK (cardinality(products) > 0),
    version_field         text NOT NULL DEFAULT 'firmware_version'
                              CONSTRAINT cpe_mapping_version_field_check CHECK (version_field IN ('firmware_version', 'os_version')),
    version_scheme        text NOT NULL DEFAULT 'generic'
                              CONSTRAINT cpe_mapping_version_scheme_check CHECK (version_scheme IN ('generic', 'cisco_ios', 'cisco_ios_xe', 'cisco_nxos', 'junos')),
    priority              integer NOT NULL DEFAULT 100,
    created_by            uuid NULL,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT cpe_mapping_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT cpe_mapping_pattern_check CHECK (
        (match_kind = 'manufacturer_model' AND manufacturer_pattern IS NOT NULL AND btrim(manufacturer_pattern) <> '')
        OR (match_kind = 'sys_object_id' AND sys_object_id_prefix IS NOT NULL AND btrim(sys_object_id_prefix) <> ''))
);
CREATE INDEX idx_cpe_mapping_kind_priority ON cpe_mapping (organization_id, match_kind, priority);

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON cpe_mapping
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE cpe_mapping ENABLE ROW LEVEL SECURITY;
ALTER TABLE cpe_mapping FORCE ROW LEVEL SECURITY;
CREATE POLICY cpe_mapping_isolation ON cpe_mapping
    USING (organization_id = current_setting('app.org_id')::uuid)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid);
