-- Down of 000281: count_document_links of 000139 and the policy
-- document_tenant_isolation of 000217, without document.orphaned and its
-- guard. An orphaned document is org-wide again (SR-56).

DROP POLICY document_tenant_isolation ON document;
DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    org_wide CONSTANT text := '(NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL '
                              'AND NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL '
                              'AND NULLIF(current_setting(''app.team_scope'', true), '''') IS NULL)';
BEGIN
    EXECUTE format('CREATE POLICY document_tenant_isolation ON document '
                   'USING (%1$s AND (%2$s OR link_count = 0 OR EXISTS (SELECT 1 FROM document_link l WHERE l.document_id = document.id))) '
                   'WITH CHECK (%1$s)',
                   org_match, org_wide);
END
$$;

DROP TRIGGER document_orphaned_guard ON document;

CREATE OR REPLACE FUNCTION count_document_links() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        UPDATE document SET link_count = link_count - 1 WHERE id = OLD.document_id;
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        UPDATE document SET link_count = link_count + 1 WHERE id = NEW.document_id;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'document % is not visible', NEW.document_id USING ERRCODE = '42501';
        END IF;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

ALTER TABLE document DROP COLUMN orphaned;
