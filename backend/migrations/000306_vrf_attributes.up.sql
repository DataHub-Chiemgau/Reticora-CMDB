-- Migration 000306 (provisional number): VRF attributes (IPM-07, WP-907).
--
-- * vrf.route_distinguisher (RFC 4364: ASN16:nn32, IPv4:nn16 or
--   ASN32:nn16, checked by the API), unique per organization;
--   import_targets and export_targets in the same format. The default VRF
--   is the global routing table: its route distinguisher and route targets
--   stay empty (vrf_default_no_routing). is_default and the delete
--   protection (vrf_protect_default, 000092) stay unchanged.
-- * vrf_section(vrf, section): the IPAM sections of a VRF. Both sides
--   cascade. It has no client and no site: it is read org-wide like
--   vrf_isolation and written only by principals without client and site
--   restriction (restrictive policies as vrf_scoped_* of 000148, E-09); the
--   visibility of the section is checked by the writing path under row
--   level security.

ALTER TABLE vrf
    ADD COLUMN route_distinguisher text,
    ADD COLUMN import_targets text[] NOT NULL DEFAULT '{}',
    ADD COLUMN export_targets text[] NOT NULL DEFAULT '{}',
    ADD CONSTRAINT vrf_route_distinguisher_check CHECK (route_distinguisher IS NULL OR btrim(route_distinguisher) <> ''),
    ADD CONSTRAINT vrf_route_targets_check CHECK (cardinality(import_targets) <= 64 AND cardinality(export_targets) <= 64),
    ADD CONSTRAINT vrf_default_no_routing CHECK (NOT is_default OR (route_distinguisher IS NULL
        AND cardinality(import_targets) = 0 AND cardinality(export_targets) = 0));
CREATE UNIQUE INDEX vrf_route_distinguisher_key ON vrf (organization_id, route_distinguisher)
    WHERE route_distinguisher IS NOT NULL;

CREATE TABLE vrf_section (
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    vrf_id          uuid NOT NULL,
    section_id      uuid NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT vrf_section_pkey PRIMARY KEY (vrf_id, section_id),
    CONSTRAINT vrf_section_vrf_fkey FOREIGN KEY (vrf_id, organization_id)
        REFERENCES vrf (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT vrf_section_section_fkey FOREIGN KEY (section_id, organization_id)
        REFERENCES ipam_section (id, organization_id) ON DELETE CASCADE
);
CREATE INDEX idx_vrf_section_section ON vrf_section (section_id);

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON vrf_section
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE vrf_section ENABLE ROW LEVEL SECURITY;
ALTER TABLE vrf_section FORCE ROW LEVEL SECURITY;

CREATE POLICY vrf_section_isolation ON vrf_section
    USING (organization_id = current_setting('app.org_id')::uuid)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid);
CREATE POLICY vrf_section_scoped_insert ON vrf_section AS RESTRICTIVE FOR INSERT
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL);
CREATE POLICY vrf_section_scoped_update ON vrf_section AS RESTRICTIVE FOR UPDATE
    USING (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL);
CREATE POLICY vrf_section_scoped_delete ON vrf_section AS RESTRICTIVE FOR DELETE
    USING (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL);
