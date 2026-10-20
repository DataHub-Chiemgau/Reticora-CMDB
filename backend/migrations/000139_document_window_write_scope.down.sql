-- Reverts WP-341: organization-only restrictive policies of migration 000079,
-- counters and require_visible_parent of migration 000064.

DO $$
DECLARE
    entry RECORD;
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
BEGIN
    FOR entry IN SELECT * FROM (VALUES ('document'), ('maintenance_window')) AS t(table_name)
    LOOP
        EXECUTE format('DROP POLICY %I ON %I', entry.table_name || '_scoped_insert', entry.table_name);
        EXECUTE format('DROP POLICY %I ON %I', entry.table_name || '_scoped_update', entry.table_name);
        EXECUTE format('DROP POLICY %I ON %I', entry.table_name || '_scoped_delete', entry.table_name);
        EXECUTE format('CREATE POLICY %I ON public.%I AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                       entry.table_name || '_scoped_update', entry.table_name, org_match, org_match);
        EXECUTE format('CREATE POLICY %I ON public.%I AS RESTRICTIVE FOR DELETE USING (%s)',
                       entry.table_name || '_scoped_delete', entry.table_name, org_match);
    END LOOP;

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

DROP TRIGGER document_scoped_insert_linked ON document;
DROP TRIGGER maintenance_window_scoped_insert_linked ON maintenance_window;
DROP FUNCTION require_linked_row();

DROP TRIGGER document_link_parent_visible ON document_link;
DROP TRIGGER maintenance_window_ci_parent_visible ON maintenance_window_ci;
DROP FUNCTION require_visible_parent();

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

DROP TRIGGER document_link_count_guard ON document;
DROP TRIGGER maintenance_window_ci_count_guard ON maintenance_window;
DROP FUNCTION protect_link_counter();

CREATE OR REPLACE FUNCTION count_document_links() RETURNS trigger
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

CREATE OR REPLACE FUNCTION count_maintenance_window_cis() RETURNS trigger
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

DROP FUNCTION app_scope_restore(text[]);
DROP FUNCTION app_scope_widen();
