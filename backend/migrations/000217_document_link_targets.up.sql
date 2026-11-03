-- WP-267 (MGT-01, API-06): link targets of documents with derived client,
-- site and team, and the team scope for documents.
--
-- 1. doc_link_entity_type_check admits location and disposal_record next to
--    ci, asset, assignment, ticket and stocktake (000011). contract
--    (WP-737) and stock_movement (WP-744) follow.
-- 2. document_link.team_id: derive_document_link_scope (000064) takes it
--    from the linked ticket. It also derives client and site of a location
--    and of a disposal record (its own columns since 000145), and the site
--    of an asset (000151) and of an assignment (via its asset).
-- 3. Propagation: asset_module_scope_propagation fires also when the site of
--    an asset changes, ticket_scope_propagation also when the team of a
--    ticket changes; new AFTER UPDATE triggers on location and
--    disposal_record re-derive their links. Replaced and new propagation
--    functions run with the scope widened (app_scope_widen, 000139) and a
--    fixed search_path, like 000141, 000145 and 000151.
-- 4. Team scope: the policy of document_link gets the team predicate of
--    ticket (000065). A principal restricted in the team scope is no longer
--    org-wide for documents: the document policies of 000139/000146, the
--    restrictive UPDATE/DELETE policies of document_link (000079) and the
--    triggers require_visible_parent and require_linked_row (000139, for
--    documents only: trigger argument 'team') take app.team_scope into
--    account. app_scope_widen/app_scope_restore clear and restore
--    app.team_scope as well, so the counter triggers keep finding the
--    document under the restrictive UPDATE policy.

-- ─── 1. Link targets ───────────────────────────────────────────────────────────

ALTER TABLE document_link DROP CONSTRAINT doc_link_entity_type_check;
ALTER TABLE document_link ADD CONSTRAINT doc_link_entity_type_check CHECK (entity_type IN (
    'ci', 'asset', 'assignment', 'ticket', 'location', 'disposal_record', 'stocktake'
));

ALTER TABLE document_link ADD COLUMN team_id UUID;
CREATE INDEX idx_document_link_team ON document_link (team_id) WHERE team_id IS NOT NULL;

-- ─── 2. Scope helpers ──────────────────────────────────────────────────────────

-- app_scope_widen clears the client, site and team scope transaction-locally
-- and returns the previous values for app_scope_restore. A function SET
-- clause would restore the values on return, so only search_path is fixed.
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

-- ─── 3. Derivation ─────────────────────────────────────────────────────────────

-- derive_document_link_scope runs under the RLS of the writer: a target
-- outside its scope derives NULL and is then rejected by WITH CHECK for
-- scoped principals (E-09). stocktake links stay org-wide.
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
        WHEN 'assignment' THEN
            SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id
              FROM assignment s JOIN asset a ON a.id = s.asset_id
             WHERE s.id = NEW.entity_id AND s.organization_id = NEW.organization_id;
        WHEN 'location' THEN
            SELECT l.client_id, l.site_id INTO NEW.client_id, NEW.site_id
              FROM location l WHERE l.id = NEW.entity_id AND l.organization_id = NEW.organization_id;
        WHEN 'disposal_record' THEN
            SELECT d.client_id, d.site_id INTO NEW.client_id, NEW.site_id
              FROM disposal_record d WHERE d.id = NEW.entity_id AND d.organization_id = NEW.organization_id;
        ELSE
            NULL; -- stocktake: org-wide
    END CASE;
    RETURN NEW;
END
$$;

-- ─── 4. Propagation ────────────────────────────────────────────────────────────

-- A no-op UPDATE re-runs the derivation triggers of the dependent rows.
CREATE OR REPLACE FUNCTION propagate_asset_module_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE ticket SET related_asset_id = related_asset_id WHERE related_asset_id = NEW.id;
    UPDATE document_link SET entity_id = entity_id WHERE entity_type = 'asset' AND entity_id = NEW.id;
    UPDATE document_link SET entity_id = entity_id
     WHERE entity_type = 'assignment'
       AND entity_id IN (SELECT s.id FROM assignment s WHERE s.asset_id = NEW.id);
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

-- propagate_document_link_scope re-derives the links of entity type
-- TG_ARGV[0] to the updated row.
CREATE FUNCTION propagate_document_link_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE document_link SET entity_id = entity_id WHERE entity_type = TG_ARGV[0] AND entity_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

DROP TRIGGER asset_module_scope_propagation ON asset;
CREATE TRIGGER asset_module_scope_propagation AFTER UPDATE ON asset
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_asset_module_scope();
DROP TRIGGER ticket_scope_propagation ON ticket;
CREATE TRIGGER ticket_scope_propagation AFTER UPDATE ON ticket
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id
                       OR OLD.team_id IS DISTINCT FROM NEW.team_id)
    EXECUTE FUNCTION propagate_ticket_scope();
CREATE TRIGGER location_document_scope_propagation AFTER UPDATE ON location
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_document_link_scope('location');
CREATE TRIGGER disposal_record_document_scope_propagation AFTER UPDATE ON disposal_record
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_document_link_scope('disposal_record');

-- ─── 5. Links of scoped principals ─────────────────────────────────────────────

-- require_visible_parent (000139): a principal restricted in the client or
-- site scope, or with TG_ARGV[3] = 'team' in the team scope, links only a
-- parent that already has links or that its own transaction inserted.
-- TG_ARGV: parent table, foreign key column, counter column, optional 'team'.
CREATE OR REPLACE FUNCTION require_visible_parent() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    parent_id UUID := (to_jsonb(NEW) ->> TG_ARGV[1])::uuid;
    org_wide BOOLEAN := NULLIF(current_setting('app.client_scope', true), '') IS NULL
                        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL
                        AND (TG_NARGS < 4 OR TG_ARGV[3] IS DISTINCT FROM 'team'
                             OR NULLIF(current_setting('app.team_scope', true), '') IS NULL);
    visible BOOLEAN;
BEGIN
    IF TG_OP = 'UPDATE' AND parent_id IS NOT DISTINCT FROM (to_jsonb(OLD) ->> TG_ARGV[1])::uuid THEN
        RETURN NEW;
    END IF;
    EXECUTE format('SELECT EXISTS (SELECT 1 FROM %I WHERE id = $1 AND ($2 OR %I > 0 OR xmin = pg_current_xact_id()::xid))',
                   TG_ARGV[0], TG_ARGV[2])
        INTO visible USING parent_id, org_wide;
    IF NOT visible THEN
        RAISE EXCEPTION '% % is not visible', TG_ARGV[0], parent_id USING ERRCODE = '42501';
    END IF;
    RETURN NEW;
END
$$;

-- require_linked_row (000139): a row inserted by a principal restricted in
-- the client or site scope, or with TG_ARGV[1] = 'team' in the team scope,
-- needs a link at commit. TG_ARGV: counter column, optional 'team'.
CREATE OR REPLACE FUNCTION require_linked_row() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    unlinked BOOLEAN;
BEGIN
    IF NULLIF(current_setting('app.client_scope', true), '') IS NULL
       AND NULLIF(current_setting('app.site_scope', true), '') IS NULL
       AND (TG_NARGS < 2 OR TG_ARGV[1] IS DISTINCT FROM 'team'
            OR NULLIF(current_setting('app.team_scope', true), '') IS NULL) THEN
        RETURN NULL;
    END IF;
    EXECUTE format('SELECT %I = 0 FROM %I WHERE id = $1', TG_ARGV[0], TG_TABLE_NAME) INTO unlinked USING NEW.id;
    IF unlinked THEN
        RAISE EXCEPTION '% % has no link in the scope', TG_TABLE_NAME, NEW.id USING ERRCODE = '42501';
    END IF;
    RETURN NULL;
END
$$;

DROP TRIGGER document_link_parent_visible ON document_link;
CREATE TRIGGER document_link_parent_visible BEFORE INSERT OR UPDATE ON document_link
    FOR EACH ROW EXECUTE FUNCTION require_visible_parent('document', 'document_id', 'link_count', 'team');
DROP TRIGGER document_scoped_insert_linked ON document;
CREATE CONSTRAINT TRIGGER document_scoped_insert_linked AFTER INSERT ON document
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION require_linked_row('link_count', 'team');

-- ─── 6. Policies ───────────────────────────────────────────────────────────────

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
    -- The links of a document the principal may write: client, site and
    -- team in its scope (the WITH CHECK expression of document_link).
    counted_writable text := format('(%1$s OR l.client_id = ANY (%2$s)) AND (%3$s OR l.site_id = ANY (%4$s)) '
                                    'AND (%5$s OR l.team_id = ANY (%6$s))',
                                    client_all, client_ids, site_all, site_ids, team_all, team_ids);
    writable text;
BEGIN
    DROP POLICY document_link_tenant_isolation ON document_link;
    DROP POLICY document_link_scoped_update ON document_link;
    DROP POLICY document_link_scoped_delete ON document_link;
    EXECUTE format(
        'CREATE POLICY document_link_tenant_isolation ON document_link '
        'USING (%1$s AND (%2$s OR client_id IS NULL OR client_id = ANY (%3$s)) '
            'AND (%4$s OR site_id IS NULL OR site_id = ANY (%5$s)) '
            'AND (%6$s OR team_id IS NULL OR team_id = ANY (%7$s))) '
        'WITH CHECK (%8$s)',
        org_match, client_all, client_ids, site_all, site_ids, team_all, team_ids, link_writable);
    EXECUTE format('CREATE POLICY document_link_scoped_update ON document_link AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                   link_writable, link_writable);
    EXECUTE format('CREATE POLICY document_link_scoped_delete ON document_link AS RESTRICTIVE FOR DELETE USING (%s)',
                   link_writable);

    writable := format('%1$s AND (%2$s OR (link_count > 0 AND link_count = (SELECT count(*) FROM document_link l '
                       'WHERE l.document_id = document.id AND %3$s)))',
                       org_match, org_wide, counted_writable);
    DROP POLICY document_tenant_isolation ON document;
    DROP POLICY document_scoped_update ON document;
    DROP POLICY document_scoped_delete ON document;
    EXECUTE format('CREATE POLICY document_tenant_isolation ON document '
                   'USING (%1$s AND (%2$s OR link_count = 0 OR EXISTS (SELECT 1 FROM document_link l WHERE l.document_id = document.id))) '
                   'WITH CHECK (%1$s)',
                   org_match, org_wide);
    EXECUTE format('CREATE POLICY document_scoped_update ON document AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                   writable, writable);
    EXECUTE format('CREATE POLICY document_scoped_delete ON document AS RESTRICTIVE FOR DELETE USING (%s)', writable);
END
$$;

-- ─── 7. Backfill ───────────────────────────────────────────────────────────────

UPDATE document_link SET entity_id = entity_id;
