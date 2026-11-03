-- Reverts 000217_document_link_targets: functions, triggers and policies of
-- 000064, 000079, 000139 and 000146, the CHECK of 000011. Links to locations
-- and disposal records are removed first, team_id last.

DELETE FROM document_link WHERE entity_type IN ('location', 'disposal_record');

-- ─── Policies (000064/000079 for document_link, 000139/000146 for document) ──

DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    client_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    client_ids CONSTANT text := 'string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_ids CONSTANT text := 'string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[]';
    link_check text;
    org_wide CONSTANT text := '(NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL '
                              'AND NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL)';
    link_writable CONSTANT text := '(NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL '
                                   'OR l.client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])) '
                                   'AND (NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL '
                                   'OR l.site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[]))';
    writable text;
BEGIN
    DROP POLICY document_link_tenant_isolation ON document_link;
    DROP POLICY document_link_scoped_update ON document_link;
    DROP POLICY document_link_scoped_delete ON document_link;
    EXECUTE format(
        'CREATE POLICY document_link_tenant_isolation ON document_link '
        'USING (%1$s AND (%2$s OR client_id IS NULL OR client_id = ANY (%3$s)) '
            'AND (%4$s OR site_id IS NULL OR site_id = ANY (%5$s))) '
        'WITH CHECK (%1$s AND (%2$s OR client_id = ANY (%3$s)) '
            'AND (%4$s OR site_id = ANY (%5$s)))',
        org_match, client_all, client_ids, site_all, site_ids);
    SELECT p.with_check INTO link_check FROM pg_policies p
     WHERE p.schemaname = 'public' AND p.tablename = 'document_link' AND p.policyname = 'document_link_tenant_isolation';
    EXECUTE format('CREATE POLICY document_link_scoped_update ON public.document_link AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                   link_check, link_check);
    EXECUTE format('CREATE POLICY document_link_scoped_delete ON public.document_link AS RESTRICTIVE FOR DELETE USING (%s)',
                   link_check);

    DROP POLICY document_tenant_isolation ON document;
    DROP POLICY document_scoped_update ON document;
    DROP POLICY document_scoped_delete ON document;
    EXECUTE format('CREATE POLICY %1$I ON %2$I '
                   'USING (%3$s AND (%4$s OR %5$I = 0 OR EXISTS (SELECT 1 FROM %6$I l WHERE l.%7$I = %2$I.id))) '
                   'WITH CHECK (%3$s)',
                   'document_tenant_isolation', 'document', org_match, org_wide, 'link_count', 'document_link', 'document_id');
    writable := format('%1$s AND (%2$s OR (%3$I > 0 AND %3$I = (SELECT count(*) FROM %4$I l WHERE l.%5$I = %6$I.id AND %7$s)))',
                       org_match, org_wide, 'link_count', 'document_link', 'document_id', 'document', link_writable);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                   'document_scoped_update', 'document', writable, writable);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR DELETE USING (%s)',
                   'document_scoped_delete', 'document', writable);
END
$$;

-- ─── Links of scoped principals (000139) ───────────────────────────────────────

DROP TRIGGER document_link_parent_visible ON document_link;
DROP TRIGGER document_scoped_insert_linked ON document;

CREATE OR REPLACE FUNCTION require_visible_parent() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    parent_id UUID := (to_jsonb(NEW) ->> TG_ARGV[1])::uuid;
    org_wide BOOLEAN := NULLIF(current_setting('app.client_scope', true), '') IS NULL
                        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL;
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

CREATE OR REPLACE FUNCTION require_linked_row() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    unlinked BOOLEAN;
BEGIN
    IF NULLIF(current_setting('app.client_scope', true), '') IS NULL
       AND NULLIF(current_setting('app.site_scope', true), '') IS NULL THEN
        RETURN NULL;
    END IF;
    EXECUTE format('SELECT %I = 0 FROM %I WHERE id = $1', TG_ARGV[0], TG_TABLE_NAME) INTO unlinked USING NEW.id;
    IF unlinked THEN
        RAISE EXCEPTION '% % has no link in the scope', TG_TABLE_NAME, NEW.id USING ERRCODE = '42501';
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER document_link_parent_visible BEFORE INSERT OR UPDATE ON document_link
    FOR EACH ROW EXECUTE FUNCTION require_visible_parent('document', 'document_id', 'link_count');
CREATE CONSTRAINT TRIGGER document_scoped_insert_linked AFTER INSERT ON document
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION require_linked_row('link_count');

-- ─── Propagation (000064) ──────────────────────────────────────────────────────

DROP TRIGGER disposal_record_document_scope_propagation ON disposal_record;
DROP TRIGGER location_document_scope_propagation ON location;
DROP FUNCTION propagate_document_link_scope();

DROP TRIGGER ticket_scope_propagation ON ticket;
CREATE TRIGGER ticket_scope_propagation AFTER UPDATE ON ticket
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_ticket_scope();
DROP TRIGGER asset_module_scope_propagation ON asset;
CREATE TRIGGER asset_module_scope_propagation AFTER UPDATE ON asset
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION propagate_asset_module_scope();

CREATE OR REPLACE FUNCTION propagate_asset_module_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE ticket SET related_asset_id = related_asset_id WHERE related_asset_id = NEW.id;
    UPDATE document_link SET entity_id = entity_id WHERE entity_type = 'asset' AND entity_id = NEW.id;
    UPDATE document_link SET entity_id = entity_id
     WHERE entity_type = 'assignment'
       AND entity_id IN (SELECT s.id FROM assignment s WHERE s.asset_id = NEW.id);
    RETURN NULL;
END
$$;

-- ─── Derivation (000064) ───────────────────────────────────────────────────────

CREATE OR REPLACE FUNCTION derive_document_link_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    CASE NEW.entity_type
        WHEN 'ci' THEN
            SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id FROM ci c WHERE c.id = NEW.entity_id;
        WHEN 'asset' THEN
            SELECT a.client_id INTO NEW.client_id FROM asset a WHERE a.id = NEW.entity_id;
        WHEN 'ticket' THEN
            SELECT t.client_id, t.site_id INTO NEW.client_id, NEW.site_id FROM ticket t WHERE t.id = NEW.entity_id;
        WHEN 'assignment' THEN
            SELECT a.client_id INTO NEW.client_id
              FROM assignment s JOIN asset a ON a.id = s.asset_id WHERE s.id = NEW.entity_id;
        ELSE
            NULL; -- stocktake: org-wide
    END CASE;
    RETURN NEW;
END
$$;

-- ─── Scope helpers (000139) ────────────────────────────────────────────────────

CREATE OR REPLACE FUNCTION app_scope_widen() RETURNS text[]
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
    previous text[] := ARRAY[current_setting('app.client_scope', true), current_setting('app.site_scope', true)];
BEGIN
    PERFORM set_config('app.client_scope', '', true), set_config('app.site_scope', '', true);
    RETURN previous;
END
$$;

CREATE OR REPLACE FUNCTION app_scope_restore(previous text[]) RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    PERFORM set_config('app.client_scope', COALESCE(previous[1], ''), true),
            set_config('app.site_scope', COALESCE(previous[2], ''), true);
END
$$;

-- ─── Link targets (000011) ─────────────────────────────────────────────────────

DROP INDEX idx_document_link_team;
ALTER TABLE document_link DROP COLUMN team_id;

ALTER TABLE document_link DROP CONSTRAINT doc_link_entity_type_check;
ALTER TABLE document_link ADD CONSTRAINT doc_link_entity_type_check CHECK (entity_type IN (
    'ci', 'asset', 'ticket', 'assignment', 'stocktake'
));

UPDATE document_link SET entity_id = entity_id;
