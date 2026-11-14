-- Migration 000369: the compliance reports as documents (SEC-04, WP-1129,
-- E-239 option 2).
--
-- 1. document_category_check admits compliance_report next to the values of
--    000262: the job compliance.report stores each report (PDF or CSV) as a
--    document of this category.
-- 2. doc_link_entity_type_check admits organization and client next to the
--    list of 000266: an organization report is linked to the organization,
--    a client report to its client.
-- 3. derive_document_link_scope() (version of 000266) gets the branches
--    client (client_id is the client, which must belong to the
--    organization of the link) and organization (the organization of the
--    link itself; org-wide like stocktake). A link of a client report is
--    therefore visible in that client's scope only.
-- 4. document_link_tenant_isolation (version of 000217) shows a link of the
--    kind organization to org-wide principals only (no client, site or
--    team restriction): its scope columns stay NULL, which the policy
--    otherwise shows to every principal. document_tenant_isolation (000281)
--    finds a visible link through document_link under this policy, so an
--    organization report, with the data of every client, is visible to
--    org-wide principals only, like linkVisible in the repository.
--
-- No new table.

-- ─── 1. document category ─────────────────────────────────────────────────────

ALTER TABLE document DROP CONSTRAINT document_category_check;
ALTER TABLE document ADD CONSTRAINT document_category_check CHECK (category IN (
    'general', 'contract', 'invoice', 'manual', 'diagram', 'certificate', 'policy', 'other', 'signature',
    'compliance_report'
));

-- ─── 2. link targets ──────────────────────────────────────────────────────────

ALTER TABLE document_link DROP CONSTRAINT doc_link_entity_type_check;
ALTER TABLE document_link ADD CONSTRAINT doc_link_entity_type_check CHECK (entity_type IN (
    'ci', 'asset', 'ticket', 'location', 'disposal_record', 'stocktake', 'contract', 'stock_movement',
    'organization', 'client'
));

-- ─── 3. scope of a link ───────────────────────────────────────────────────────

-- Version of 000266 with the branches client and organization.
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
        WHEN 'client' THEN
            IF NOT EXISTS (SELECT 1 FROM client c WHERE c.id = NEW.entity_id AND c.organization_id = NEW.organization_id) THEN
                RAISE EXCEPTION 'document link: client % is not a client of the organization', NEW.entity_id
                    USING ERRCODE = 'foreign_key_violation';
            END IF;
            NEW.client_id := NEW.entity_id;
        WHEN 'organization' THEN
            IF NEW.entity_id IS DISTINCT FROM NEW.organization_id THEN
                RAISE EXCEPTION 'document link: a document is linked only to its own organization'
                    USING ERRCODE = 'foreign_key_violation';
            END IF;
        ELSE
            NULL; -- stocktake: org-wide
    END CASE;
    RETURN NEW;
END
$$;

-- ─── 4. visibility of an organization link ───────────────────────────────────

-- Version of 000217 with the organization links for org-wide principals
-- only.
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
    org_wide text := format('(%s AND %s AND %s)', client_all, site_all, team_all);
BEGIN
    DROP POLICY document_link_tenant_isolation ON document_link;
    EXECUTE format(
        'CREATE POLICY document_link_tenant_isolation ON document_link '
        'USING (%1$s AND (%2$s OR client_id IS NULL OR client_id = ANY (%3$s)) '
            'AND (%4$s OR site_id IS NULL OR site_id = ANY (%5$s)) '
            'AND (%6$s OR team_id IS NULL OR team_id = ANY (%7$s)) '
            'AND (entity_type <> ''organization'' OR %9$s)) '
        'WITH CHECK (%8$s)',
        org_match, client_all, client_ids, site_all, site_ids, team_all, team_ids, link_writable, org_wide);
END
$$;
