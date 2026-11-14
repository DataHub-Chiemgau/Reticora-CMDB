-- Down of 000369: the links to organizations and clients go, the report
-- documents become category other; list, category check and
-- derive_document_link_scope() return to 000266/000262, the policy
-- document_link_tenant_isolation to 000217.

DELETE FROM document_link WHERE entity_type IN ('organization', 'client');
UPDATE document SET category = 'other' WHERE category = 'compliance_report';

ALTER TABLE document_link DROP CONSTRAINT doc_link_entity_type_check;
ALTER TABLE document_link ADD CONSTRAINT doc_link_entity_type_check CHECK (entity_type IN (
    'ci', 'asset', 'ticket', 'location', 'disposal_record', 'stocktake', 'contract', 'stock_movement'
));

ALTER TABLE document DROP CONSTRAINT document_category_check;
ALTER TABLE document ADD CONSTRAINT document_category_check CHECK (category IN (
    'general', 'contract', 'invoice', 'manual', 'diagram', 'certificate', 'policy', 'other', 'signature'
));

-- Version of 000266.
CREATE OR REPLACE FUNCTION derive_document_link_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    NEW.team_id := NULL;
    CASE NEW.entity_type
        WHEN 'ci' THEN
            SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
              FROM ci c WHERE c.id = NEW.entity_id AND c.organization_id = NEW.organization_id;
        WHEN 'asset' THEN
            SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id
              FROM asset a WHERE a.id = NEW.entity_id AND a.organization_id = NEW.organization_id;
        WHEN 'ticket' THEN
            SELECT t.client_id, t.site_id, t.team_id INTO NEW.client_id, NEW.site_id, NEW.team_id
              FROM ticket t WHERE t.id = NEW.entity_id AND t.organization_id = NEW.organization_id;
        WHEN 'location' THEN
            SELECT l.client_id, l.site_id INTO NEW.client_id, NEW.site_id
              FROM location l WHERE l.id = NEW.entity_id AND l.organization_id = NEW.organization_id;
        WHEN 'disposal_record' THEN
            SELECT d.client_id, d.site_id INTO NEW.client_id, NEW.site_id
              FROM disposal_record d WHERE d.id = NEW.entity_id AND d.organization_id = NEW.organization_id;
        WHEN 'contract' THEN
            SELECT c.client_id INTO NEW.client_id
              FROM contract c WHERE c.id = NEW.entity_id AND c.organization_id = NEW.organization_id;
        WHEN 'stock_movement' THEN
            SELECT m.client_id, m.site_id INTO NEW.client_id, NEW.site_id
              FROM stock_movement m WHERE m.id = NEW.entity_id AND m.organization_id = NEW.organization_id;
        ELSE
            NULL; -- stocktake: org-wide
    END CASE;
    RETURN NEW;
END
$$;

-- Version of 000217.
DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    client_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    client_ids CONSTANT text := 'string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_ids CONSTANT text := 'string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[]';
    team_all CONSTANT text := 'NULLIF(current_setting(''app.team_scope'', true), '''') IS NULL';
    team_ids CONSTANT text := 'string_to_array(current_setting(''app.team_scope'', true), '','')::uuid[]';
    link_writable text := format('%1$s AND (%2$s OR client_id = ANY (%3$s)) AND (%4$s OR site_id = ANY (%5$s)) '
                                 'AND (%6$s OR team_id = ANY (%7$s))',
                                 org_match, client_all, client_ids, site_all, site_ids, team_all, team_ids);
BEGIN
    DROP POLICY document_link_tenant_isolation ON document_link;
    EXECUTE format(
        'CREATE POLICY document_link_tenant_isolation ON document_link '
        'USING (%1$s AND (%2$s OR client_id IS NULL OR client_id = ANY (%3$s)) '
            'AND (%4$s OR site_id IS NULL OR site_id = ANY (%5$s)) '
            'AND (%6$s OR team_id IS NULL OR team_id = ANY (%7$s))) '
        'WITH CHECK (%8$s)',
        org_match, client_all, client_ids, site_all, site_ids, team_all, team_ids, link_writable);
END
$$;
