-- Migration 000280: the field rows of the full-text index carry the client
-- and site of their document (NFR-02, SRC-01, RBA-07; finding of the load
-- run 42582336513, global_search p95 2.5 s).
--
-- The policy of search_document_field (000228) checked the scope of a row
-- with EXISTS on search_document, whose own policy (000067) applies the
-- client and site scope. The global search reads every custom field row
-- and tag row of the organization (attributeRows) and @@ is not leakproof,
-- so the policy ran before the match: one index probe into search_document
-- per field row, about 45,000 buffers per search at 40,000 objects.
--
-- 1. client_id and site_id on search_document_field, always those of the
--    document: search_field_scope (BEFORE INSERT and BEFORE UPDATE of the
--    key and scope columns) reads them from search_document; a writer
--    cannot set others. The lookup runs under the RLS of the writer: a
--    document the writer does not see refuses the row (42501), as the
--    foreign key refuses a missing one. In the index statement
--    (IndexDocuments) the trigger sees the document the same statement
--    upserted (a PL/pgSQL query takes a fresh snapshot after the command
--    counter advanced), which the policy subquery of 000228 could not.
-- 2. search_field_scope_propagation: when the client or site of a document
--    changes (derive_search_document_scope after a move of its entity,
--    propagate_search_scope), its field rows follow in the same statement,
--    with the client and site scope widened (app_scope_widen, 000139/000217;
--    app.org_id stays) so no row of the document is left behind. The
--    trigger is not UPDATE OF client_id, site_id: the columns change in the
--    BEFORE trigger search_document_scope, the statements set entity_id.
-- 3. The policies are those of search_document (000067, 000079) over the
--    columns of the row: USING with the organization, then client and site
--    with the NULL semantics of E-09, so a principal reads exactly the
--    field rows of the documents it reads; the EXISTS lookup is gone.
--    WITH CHECK without NULL for scoped principals and the restrictive
--    policies search_document_field_scoped_update and _scoped_delete
--    (TEN-05, pattern of ci_identity_mac in 000161): a scoped principal
--    writes the field rows of the documents it may write, as it writes
--    the documents themselves. Before, WITH CHECK checked the organization
--    only, because the policy could not see a document written in the
--    same statement; the trigger now sets the columns before the check.
-- 4. Backfill of the columns from the documents before the triggers exist.
-- 5. idx_search_document_field_match: the custom field rows and tag rows
--    the global search matches (attributeRows), about one in fifteen field
--    rows. Its predicate calls only leakproof functions (starts_with,
--    texteq), so the application role uses it under the policy
--    (TestRLSIndexesAreLeakproof) instead of reading every field row of the
--    organization. document_id serves the lookups of these rows per
--    candidate (the readable custom fields of the hit statement). IF NOT
--    EXISTS lets an operator build it beforehand with CREATE INDEX
--    CONCURRENTLY (docs/schema-baseline.md).

ALTER TABLE search_document_field ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;

UPDATE search_document_field f SET client_id = d.client_id, site_id = d.site_id
  FROM search_document d
 WHERE d.id = f.document_id AND (d.client_id IS NOT NULL OR d.site_id IS NOT NULL);

CREATE FUNCTION search_field_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    SELECT d.client_id, d.site_id INTO NEW.client_id, NEW.site_id
      FROM search_document d
     WHERE d.id = NEW.document_id AND d.organization_id = NEW.organization_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'search document % is not visible', NEW.document_id
            USING ERRCODE = '42501', CONSTRAINT = 'search_document_field_document_fkey';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER search_field_scope BEFORE INSERT OR UPDATE OF organization_id, document_id, client_id, site_id
    ON search_document_field FOR EACH ROW EXECUTE FUNCTION search_field_scope();

CREATE FUNCTION propagate_search_field_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE search_document_field SET client_id = NEW.client_id, site_id = NEW.site_id
     WHERE document_id = NEW.id AND organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER search_field_scope_propagation AFTER UPDATE ON search_document
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_search_field_scope();

DROP POLICY search_document_field_tenant_isolation ON search_document_field;
CREATE POLICY search_document_field_tenant_isolation ON search_document_field FOR ALL
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    );
CREATE POLICY search_document_field_scoped_update ON search_document_field AS RESTRICTIVE FOR UPDATE
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    );
CREATE POLICY search_document_field_scoped_delete ON search_document_field AS RESTRICTIVE FOR DELETE
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    );

CREATE INDEX IF NOT EXISTS idx_search_document_field_match ON search_document_field (organization_id, document_id)
    WHERE starts_with(field, 'attributes.') OR field = 'core.tags';
