-- WP-852 (SR-56, TEN-05, TEN-09, E-09): a document that had links does not
-- become org-wide when its last link goes.
--
-- The policy of document (000064, 000139, 000217) shows a document without
-- a link (link_count = 0) to every principal of the organization: an
-- unlinked document is org-wide. E-09 lets only org-wide principals create
-- one (require_linked_row on INSERT), but nothing kept a linked document
-- from losing its links: a client-2 principal removed the last link of a
-- client-2 document (DELETE /api/v1/documents/{id}/links/{link_id}), and
-- since 000275 the deletion of a link target (delete_target_document_links;
-- delete_contract_document_links of 000238) removes the links, so the
-- document, its versions and its content became visible to every client.
--
-- 1. document.orphaned marks a document that had links. count_document_links
--    sets it when the counter falls to 0 (a link removed or moved away); a
--    new link does not clear it. Only the counter triggers write it
--    (document_orphaned_guard, protect_link_counter of 000139).
-- 2. document_tenant_isolation shows an unlinked document to every
--    principal only when it never had links (NOT orphaned). An orphaned one
--    is visible to org-wide principals only (no client, site or team
--    restriction, E-09, E-11), who may link it again; a scoped principal
--    can neither see nor link it (require_visible_parent: link_count = 0).
--    document_version, the search and the AI chunks read document under its
--    policy and follow.
-- 3. Backfill: an unlinked document whose links 000275 moved to
--    migration_quarantine, or with a document.linked or document.unlinked
--    entry in audit_log, had links and is marked orphaned.

ALTER TABLE document ADD COLUMN orphaned boolean NOT NULL DEFAULT false;

-- ─── 1. Counter ────────────────────────────────────────────────────────────────

CREATE OR REPLACE FUNCTION count_document_links() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        -- The SET list reads the old row: link_count = 1 is the last link.
        UPDATE document SET link_count = link_count - 1, orphaned = orphaned OR link_count = 1
         WHERE id = OLD.document_id;
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

CREATE TRIGGER document_orphaned_guard BEFORE UPDATE ON document
    FOR EACH ROW EXECUTE FUNCTION protect_link_counter('orphaned');

-- ─── 2. Policy ─────────────────────────────────────────────────────────────────

DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    org_wide CONSTANT text := '(NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL '
                              'AND NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL '
                              'AND NULLIF(current_setting(''app.team_scope'', true), '''') IS NULL)';
BEGIN
    DROP POLICY document_tenant_isolation ON document;
    EXECUTE format('CREATE POLICY document_tenant_isolation ON document '
                   'USING (%1$s AND (%2$s OR (link_count = 0 AND NOT orphaned) '
                   'OR EXISTS (SELECT 1 FROM document_link l WHERE l.document_id = document.id))) '
                   'WITH CHECK (%1$s)',
                   org_match, org_wide);
END
$$;

-- ─── 3. Backfill ───────────────────────────────────────────────────────────────

ALTER TABLE document DISABLE TRIGGER document_orphaned_guard;
UPDATE document d SET orphaned = true
 WHERE d.link_count = 0
   AND (EXISTS (SELECT 1 FROM migration_quarantine q
                 WHERE q.source_table = 'document_link' AND q.organization_id = d.organization_id
                   AND q.row_data ->> 'document_id' = d.id::text)
        OR EXISTS (SELECT 1 FROM audit_log a
                    WHERE a.resource_type = 'document' AND a.resource_id = d.id
                      AND a.organization_id = d.organization_id
                      AND a.action IN ('document.linked', 'document.unlinked')));
ALTER TABLE document ENABLE TRIGGER document_orphaned_guard;
