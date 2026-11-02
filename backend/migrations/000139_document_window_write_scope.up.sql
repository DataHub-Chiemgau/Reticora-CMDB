-- WP-341 (MGT-01, MGT-04, TEN-06; F-004, F-048, F-185): documents and
-- maintenance windows are written only by principals that see all of their
-- links, and the link counters stay exact.
--
-- Migration 000064 shows a document or window when it has no link or at
-- least one visible link, and 000079 restricts UPDATE and DELETE only to the
-- organization. A principal restricted to client 1 therefore renamed and
-- deleted documents and windows shared with client 2 (the foreign key
-- cascade removed the links of client 2) and created, changed, deleted and
-- took over unlinked (org-wide) rows (E-09).
--
-- 1. app_scope_widen()/app_scope_restore(): the counter triggers update
--    link_count and ci_count with the client and site scope cleared, so a
--    decrement is never skipped because the remover no longer sees the
--    parent. app.org_id stays.
-- 2. Counters are written only by the counter triggers: a BEFORE UPDATE
--    trigger rejects a change at trigger depth 1 (a direct UPDATE), a new
--    row starts at 0 (restrictive INSERT policy).
-- 3. An org-wide principal reads every document and window of the
--    organization, also one whose counter and links disagree. Restrictive
--    UPDATE and DELETE policies: a principal restricted in the
--    client or site scope changes a row only when it has links and the
--    counter equals the number of links visible under RLS, i.e. it sees all
--    of them. Org-wide principals are unchanged.
-- 4. E-09: a deferred constraint trigger rejects at commit a row inserted by
--    a scoped principal that still has no link; require_visible_parent lets
--    a scoped principal link only rows with links or rows of its own
--    transaction (a new window with its CIs), so an org-wide row cannot be
--    taken over. Cascaded link deletes (e.g. maintenance_window_ci when a CI
--    is deleted) only decrement the counter and stay allowed.

-- ─── 1. Scope helpers ──────────────────────────────────────────────────────────

-- app_scope_widen clears the client and site scope transaction-locally and
-- returns the previous values for app_scope_restore. A function SET clause
-- would restore the values on return, so only search_path is fixed.
CREATE FUNCTION app_scope_widen() RETURNS text[]
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

CREATE FUNCTION app_scope_restore(previous text[]) RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    PERFORM set_config('app.client_scope', COALESCE(previous[1], ''), true),
            set_config('app.site_scope', COALESCE(previous[2], ''), true);
END
$$;

-- ─── 2. Counters ───────────────────────────────────────────────────────────────

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

CREATE OR REPLACE FUNCTION count_maintenance_window_cis() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    previous text[] := app_scope_widen();
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
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

-- protect_link_counter rejects a change of the counter column TG_ARGV[0]
-- outside the counter triggers: those run the UPDATE from an AFTER trigger
-- of the link table, so the guard sees a trigger depth above 1.
CREATE FUNCTION protect_link_counter() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF pg_trigger_depth() <= 1 AND (to_jsonb(NEW) -> TG_ARGV[0]) IS DISTINCT FROM (to_jsonb(OLD) -> TG_ARGV[0]) THEN
        RAISE EXCEPTION '%.% is maintained by the link triggers', TG_TABLE_NAME, TG_ARGV[0] USING ERRCODE = '42501';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER document_link_count_guard BEFORE UPDATE ON document
    FOR EACH ROW EXECUTE FUNCTION protect_link_counter('link_count');
CREATE TRIGGER maintenance_window_ci_count_guard BEFORE UPDATE ON maintenance_window
    FOR EACH ROW EXECUTE FUNCTION protect_link_counter('ci_count');

-- ─── 3. Links of scoped principals ─────────────────────────────────────────────

-- require_visible_parent rejects a link to a document or window the writer
-- cannot see. It runs before the row exists, so the new link itself cannot
-- make the parent visible. A principal restricted in the client or site
-- scope may link only a parent that already has links (TG_ARGV[2], the
-- counter) or that its own transaction inserted: an unlinked row is
-- org-wide (E-09). TG_ARGV: parent table, foreign key column, counter column.
DROP TRIGGER document_link_parent_visible ON document_link;
DROP TRIGGER maintenance_window_ci_parent_visible ON maintenance_window_ci;
DROP FUNCTION require_visible_parent();

CREATE FUNCTION require_visible_parent() RETURNS trigger
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

CREATE TRIGGER document_link_parent_visible BEFORE INSERT OR UPDATE ON document_link
    FOR EACH ROW EXECUTE FUNCTION require_visible_parent('document', 'document_id', 'link_count');
CREATE TRIGGER maintenance_window_ci_parent_visible BEFORE INSERT OR UPDATE ON maintenance_window_ci
    FOR EACH ROW EXECUTE FUNCTION require_visible_parent('maintenance_window', 'maintenance_window_id', 'ci_count');

-- require_linked_row runs at commit for a row inserted by a scoped
-- principal and rejects it when it still has no link (E-09). A row the
-- principal no longer sees has links or was deleted. TG_ARGV: counter column.
CREATE FUNCTION require_linked_row() RETURNS trigger
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

CREATE CONSTRAINT TRIGGER document_scoped_insert_linked AFTER INSERT ON document
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION require_linked_row('link_count');
CREATE CONSTRAINT TRIGGER maintenance_window_scoped_insert_linked AFTER INSERT ON maintenance_window
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION require_linked_row('ci_count');

-- ─── 4. Policies ───────────────────────────────────────────────────────────────

DO $$
DECLARE
    entry RECORD;
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    org_wide CONSTANT text := '(NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL '
                              'AND NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL)';
    writable text;
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('document', 'document_tenant_isolation', 'link_count', 'document_link', 'document_id'),
            ('maintenance_window', 'maintenance_window_isolation', 'ci_count', 'maintenance_window_ci', 'maintenance_window_id')
        ) AS t(table_name, policy_name, counter, link_table, link_column)
    LOOP
        writable := format('%1$s AND (%2$s OR (%3$I > 0 AND %3$I = (SELECT count(*) FROM %4$I l WHERE l.%5$I = %6$I.id)))',
                           org_match, org_wide, entry.counter, entry.link_table, entry.link_column, entry.table_name);
        -- Reading: an org-wide principal sees every row of the organization,
        -- so the counter triggers (run widened) also reach a row whose last
        -- link was just removed.
        EXECUTE format('DROP POLICY %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format('CREATE POLICY %1$I ON %2$I '
                       'USING (%3$s AND (%4$s OR %5$I = 0 OR EXISTS (SELECT 1 FROM %6$I l WHERE l.%7$I = %2$I.id))) '
                       'WITH CHECK (%3$s)',
                       entry.policy_name, entry.table_name, org_match, org_wide, entry.counter, entry.link_table, entry.link_column);
        EXECUTE format('DROP POLICY %I ON %I', entry.table_name || '_scoped_update', entry.table_name);
        EXECUTE format('DROP POLICY %I ON %I', entry.table_name || '_scoped_delete', entry.table_name);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                       entry.table_name || '_scoped_update', entry.table_name, writable, writable);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR DELETE USING (%s)',
                       entry.table_name || '_scoped_delete', entry.table_name, writable);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR INSERT WITH CHECK (%s AND %I = 0)',
                       entry.table_name || '_scoped_insert', entry.table_name, org_match, entry.counter);
    END LOOP;
END
$$;
