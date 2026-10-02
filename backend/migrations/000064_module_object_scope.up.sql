-- WP-028 (MGT-01..04, TKT-01): module tables with an object reference inherit
-- the client and site of that object.
--
-- 1. Derived columns, set by BEFORE triggers whatever the writer passes:
--    - ticket: from the related CI, else the related asset (client only).
--    - ticket_comment: from its ticket.
--    - document_link: from the linked CI, asset, ticket or assignment (via its
--      asset); stocktake links stay org-wide.
--    - maintenance_window_ci: from the CI.
--    - key_assignment: from the key item (client only).
--    A reference outside the writer's scope is not visible to the trigger,
--    derives NULL and is then rejected by WITH CHECK for scoped principals
--    (E-09), like the CI child tables of migration 000061.
-- 2. Propagation when a CI, asset, ticket or key item changes client or site.
-- 3. Policies with client and site predicate (E-09 NULL semantics).
-- 4. document and maintenance_window span several objects. They count their
--    links (link_count, ci_count): without links they stay org-wide, with
--    links they are visible when at least one link is. The EXISTS runs under
--    the RLS of the link table, so it sees exactly the links in scope. WITH
--    CHECK stays on the organization: which rows can be changed is decided by
--    USING, and removing the last visible link must not fail. A link to a
--    document or window the writer cannot see is rejected, so the counters
--    stay exact; a decrement finds no row only when the parent is deleted.

-- ─── 1. Columns ────────────────────────────────────────────────────────────────

ALTER TABLE ticket ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;
ALTER TABLE ticket_comment ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;
ALTER TABLE document_link ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;
ALTER TABLE maintenance_window_ci ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;
ALTER TABLE key_assignment ADD COLUMN client_id UUID;
ALTER TABLE document ADD COLUMN link_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE maintenance_window ADD COLUMN ci_count INTEGER NOT NULL DEFAULT 0;

-- ─── 2. Derivation ─────────────────────────────────────────────────────────────

CREATE FUNCTION derive_ticket_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    IF NEW.related_ci_id IS NOT NULL THEN
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id FROM ci c WHERE c.id = NEW.related_ci_id;
    END IF;
    IF NEW.client_id IS NULL AND NEW.related_asset_id IS NOT NULL THEN
        SELECT a.client_id INTO NEW.client_id FROM asset a WHERE a.id = NEW.related_asset_id;
    END IF;
    RETURN NEW;
END
$$;

CREATE FUNCTION derive_ticket_comment_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    SELECT t.client_id, t.site_id INTO NEW.client_id, NEW.site_id FROM ticket t WHERE t.id = NEW.ticket_id;
    RETURN NEW;
END
$$;

CREATE FUNCTION derive_document_link_scope() RETURNS trigger
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

CREATE FUNCTION derive_key_assignment_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    SELECT k.client_id INTO NEW.client_id FROM key_item k WHERE k.id = NEW.key_item_id;
    RETURN NEW;
END
$$;

CREATE TRIGGER ticket_scope BEFORE INSERT OR UPDATE ON ticket
    FOR EACH ROW EXECUTE FUNCTION derive_ticket_scope();
CREATE TRIGGER ticket_comment_scope BEFORE INSERT OR UPDATE ON ticket_comment
    FOR EACH ROW EXECUTE FUNCTION derive_ticket_comment_scope();
CREATE TRIGGER document_link_scope BEFORE INSERT OR UPDATE ON document_link
    FOR EACH ROW EXECUTE FUNCTION derive_document_link_scope();
CREATE TRIGGER maintenance_window_ci_scope BEFORE INSERT OR UPDATE ON maintenance_window_ci
    FOR EACH ROW EXECUTE FUNCTION derive_ci_scope('ci_id');
CREATE TRIGGER key_assignment_scope BEFORE INSERT OR UPDATE ON key_assignment
    FOR EACH ROW EXECUTE FUNCTION derive_key_assignment_scope();

-- ─── 3. Propagation ────────────────────────────────────────────────────────────

CREATE FUNCTION propagate_ci_module_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE ticket SET related_ci_id = related_ci_id WHERE related_ci_id = NEW.id;
    UPDATE maintenance_window_ci SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE document_link SET entity_id = entity_id WHERE entity_type = 'ci' AND entity_id = NEW.id;
    RETURN NULL;
END
$$;

CREATE FUNCTION propagate_asset_module_scope() RETURNS trigger
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

CREATE FUNCTION propagate_ticket_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE ticket_comment SET ticket_id = ticket_id WHERE ticket_id = NEW.id;
    UPDATE document_link SET entity_id = entity_id WHERE entity_type = 'ticket' AND entity_id = NEW.id;
    RETURN NULL;
END
$$;

CREATE FUNCTION propagate_key_item_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE key_assignment SET key_item_id = key_item_id WHERE key_item_id = NEW.id;
    RETURN NULL;
END
$$;

CREATE TRIGGER ci_module_scope_propagation AFTER UPDATE ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_ci_module_scope();
CREATE TRIGGER asset_module_scope_propagation AFTER UPDATE ON asset
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION propagate_asset_module_scope();
CREATE TRIGGER ticket_scope_propagation AFTER UPDATE ON ticket
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_ticket_scope();
CREATE TRIGGER key_item_scope_propagation AFTER UPDATE ON key_item
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION propagate_key_item_scope();

-- ─── 4. Link counters ──────────────────────────────────────────────────────────

CREATE FUNCTION count_document_links() RETURNS trigger
LANGUAGE plpgsql AS $$
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
    RETURN NULL;
END
$$;

CREATE FUNCTION count_maintenance_window_cis() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        UPDATE maintenance_window SET ci_count = ci_count - 1 WHERE id = OLD.maintenance_window_id;
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        UPDATE maintenance_window SET ci_count = ci_count + 1 WHERE id = NEW.maintenance_window_id;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'maintenance_window % is not visible', NEW.maintenance_window_id USING ERRCODE = '42501';
        END IF;
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER document_link_count AFTER INSERT OR DELETE OR UPDATE OF document_id ON document_link
    FOR EACH ROW EXECUTE FUNCTION count_document_links();
CREATE TRIGGER maintenance_window_ci_count AFTER INSERT OR DELETE OR UPDATE OF maintenance_window_id ON maintenance_window_ci
    FOR EACH ROW EXECUTE FUNCTION count_maintenance_window_cis();

-- require_visible_parent rejects a link to a document or window the writer
-- cannot see. It runs before the row exists, so the new link itself cannot
-- make the parent visible. TG_ARGV: parent table, foreign key column.
CREATE FUNCTION require_visible_parent() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    parent_id UUID := (to_jsonb(NEW) ->> TG_ARGV[1])::uuid;
    visible BOOLEAN;
BEGIN
    IF TG_OP = 'UPDATE' AND parent_id IS NOT DISTINCT FROM (to_jsonb(OLD) ->> TG_ARGV[1])::uuid THEN
        RETURN NEW;
    END IF;
    EXECUTE format('SELECT EXISTS (SELECT 1 FROM %I WHERE id = $1)', TG_ARGV[0]) INTO visible USING parent_id;
    IF NOT visible THEN
        RAISE EXCEPTION '% % is not visible', TG_ARGV[0], parent_id USING ERRCODE = '42501';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER document_link_parent_visible BEFORE INSERT OR UPDATE ON document_link
    FOR EACH ROW EXECUTE FUNCTION require_visible_parent('document', 'document_id');
CREATE TRIGGER maintenance_window_ci_parent_visible BEFORE INSERT OR UPDATE ON maintenance_window_ci
    FOR EACH ROW EXECUTE FUNCTION require_visible_parent('maintenance_window', 'maintenance_window_id');

-- ─── 5. Backfill ───────────────────────────────────────────────────────────────

UPDATE ticket SET related_ci_id = related_ci_id;
UPDATE ticket_comment SET ticket_id = ticket_id;
UPDATE document_link SET entity_id = entity_id;
UPDATE maintenance_window_ci SET ci_id = ci_id;
UPDATE key_assignment SET key_item_id = key_item_id;
UPDATE document d SET link_count = (SELECT count(*) FROM document_link l WHERE l.document_id = d.id);
UPDATE maintenance_window w SET ci_count = (SELECT count(*) FROM maintenance_window_ci c WHERE c.maintenance_window_id = w.id);

CREATE INDEX idx_ticket_client ON ticket (client_id) WHERE client_id IS NOT NULL;
CREATE INDEX idx_ticket_comment_client ON ticket_comment (client_id) WHERE client_id IS NOT NULL;
CREATE INDEX idx_document_link_client ON document_link (client_id) WHERE client_id IS NOT NULL;
CREATE INDEX idx_maintenance_window_ci_client ON maintenance_window_ci (client_id) WHERE client_id IS NOT NULL;
CREATE INDEX idx_key_assignment_client ON key_assignment (client_id) WHERE client_id IS NOT NULL;

-- ─── 6. Policies ───────────────────────────────────────────────────────────────

DO $$
DECLARE
    entry RECORD;
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    client_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    client_ids CONSTANT text := 'string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_ids CONSTANT text := 'string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[]';
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('ticket', 'ticket_tenant_isolation'),
            ('ticket_comment', 'ticket_comment_tenant_isolation'),
            ('document_link', 'document_link_tenant_isolation'),
            ('maintenance_window_ci', 'maintenance_window_ci_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('DROP POLICY %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format(
            'CREATE POLICY %1$I ON %2$I '
            'USING (%3$s AND (%4$s OR client_id IS NULL OR client_id = ANY (%5$s)) '
                'AND (%6$s OR site_id IS NULL OR site_id = ANY (%7$s))) '
            'WITH CHECK (%3$s AND (%4$s OR client_id = ANY (%5$s)) '
                'AND (%6$s OR site_id = ANY (%7$s)))',
            entry.policy_name, entry.table_name,
            org_match, client_all, client_ids, site_all, site_ids);
    END LOOP;

    DROP POLICY key_assignment_isolation ON key_assignment;
    EXECUTE format(
        'CREATE POLICY key_assignment_isolation ON key_assignment '
        'USING (%1$s AND (%2$s OR client_id IS NULL OR client_id = ANY (%3$s))) '
        'WITH CHECK (%1$s AND (%2$s OR client_id = ANY (%3$s)))',
        org_match, client_all, client_ids);

    DROP POLICY document_tenant_isolation ON document;
    EXECUTE format(
        'CREATE POLICY document_tenant_isolation ON document '
        'USING (%1$s AND (link_count = 0 OR EXISTS (SELECT 1 FROM document_link l WHERE l.document_id = document.id))) '
        'WITH CHECK (%1$s)',
        org_match);

    DROP POLICY maintenance_window_isolation ON maintenance_window;
    EXECUTE format(
        'CREATE POLICY maintenance_window_isolation ON maintenance_window '
        'USING (%1$s AND (ci_count = 0 OR EXISTS (SELECT 1 FROM maintenance_window_ci c WHERE c.maintenance_window_id = maintenance_window.id))) '
        'WITH CHECK (%1$s)',
        org_match);
END
$$;
