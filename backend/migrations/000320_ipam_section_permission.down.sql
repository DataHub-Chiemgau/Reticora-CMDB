-- Down of 000320: removes the rights per IPAM section (WP-910).

DROP POLICY search_document_section_acl ON search_document;
DROP POLICY ip_address_section_acl_delete ON ip_address;
DROP POLICY ip_address_section_acl_update ON ip_address;
DROP POLICY ip_address_section_acl_insert ON ip_address;
DROP POLICY ip_address_section_acl_read ON ip_address;
DROP POLICY subnet_section_acl_delete ON subnet;
DROP POLICY subnet_section_acl_update ON subnet;
DROP POLICY subnet_section_acl_insert ON subnet;
DROP POLICY subnet_section_acl_read ON subnet;
DROP POLICY ipam_section_acl_read ON ipam_section;

DROP TRIGGER trg_subnet_propagate_section ON subnet;
DROP FUNCTION subnet_propagate_section();
DROP TRIGGER trg_ip_address_section ON ip_address;
DROP FUNCTION ip_address_section();
ALTER TABLE ip_address DROP COLUMN section_id;

-- default_section_of as of 000298.
CREATE OR REPLACE FUNCTION default_section_of(p_org uuid) RETURNS uuid
LANGUAGE sql STABLE
SET search_path = pg_catalog, public, pg_temp
AS $$
    SELECT id FROM ipam_section WHERE organization_id = p_org AND is_default
$$;

DROP FUNCTION ipam_section_denied(text);

-- app_scope_widen and app_scope_restore as of 000217.
CREATE OR REPLACE FUNCTION app_scope_widen() RETURNS text[]
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
    previous text[] := ARRAY[current_setting('app.client_scope', true), current_setting('app.site_scope', true),
                             current_setting('app.team_scope', true)];
BEGIN
    PERFORM set_config('app.client_scope', '', true), set_config('app.site_scope', '', true),
            set_config('app.team_scope', '', true);
    RETURN previous;
END
$$;

CREATE OR REPLACE FUNCTION app_scope_restore(previous text[]) RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    PERFORM set_config('app.client_scope', COALESCE(previous[1], ''), true),
            set_config('app.site_scope', COALESCE(previous[2], ''), true),
            set_config('app.team_scope', COALESCE(previous[3], ''), true);
END
$$;

DROP TABLE ipam_section_permission;
