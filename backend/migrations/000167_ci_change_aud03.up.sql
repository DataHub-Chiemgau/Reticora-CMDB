-- WP-487 (AUD-03, CI-11, F-095): ci_change after decision E-82 (option 2).
--
-- The field rows stay the storage form of the CI history; the field-granular
-- 409 check (API-07) and the merge keep reading them. New columns turn them
-- into changes of AUD-03:
--   - change_kind is the kind of the change (created, updated, deleted,
--     restored, merged, type_changed, relationship_added,
--     relationship_removed, status_changed); it holds for the change set;
--   - change_set_id groups the field rows of one write of a CI;
--   - actor_user_id is the user who made the change (a surrogate id, no
--     personal data, CH16; actor_id also names API keys);
--   - collector_id and observed_at name the collector and the time of the
--     observation of a discovery change;
--   - source (ui, api, collector, import, system, as in audit_log),
--     request_id and correlation_id tie the change to the request (AUD-07).
-- The type change is recorded as 'type_changed' (CI-11); existing
-- 'type_change' rows are renamed. Existing rows get their change_kind from
-- change_type (backfill); they keep change_set_id NULL and appear one by one.
-- A writer that does not set change_kind (ipam, rack, override) gets it from
-- a BEFORE INSERT OR UPDATE trigger with the same mapping.
--
-- ci_change stays a tenant table with ENABLE and FORCE ROW LEVEL SECURITY;
-- its policies are unchanged.

ALTER TABLE ci_change
    ADD COLUMN change_kind TEXT,
    ADD COLUMN change_set_id UUID,
    ADD COLUMN actor_user_id UUID,
    ADD COLUMN collector_id UUID,
    ADD COLUMN source TEXT,
    ADD COLUMN request_id TEXT,
    ADD COLUMN correlation_id TEXT,
    ADD COLUMN observed_at TIMESTAMPTZ;

ALTER TABLE ci_change ADD CONSTRAINT chk_ci_change_kind CHECK (change_kind IN (
    'created', 'updated', 'deleted', 'restored', 'merged', 'type_changed',
    'relationship_added', 'relationship_removed', 'status_changed'
));
ALTER TABLE ci_change ADD CONSTRAINT chk_ci_change_source CHECK (source IN ('ui', 'api', 'collector', 'import', 'system'));

-- ci_change_kind maps a field row to the change_kind of AUD-03. alone is
-- false when the row shares its change with other rows: a status change
-- together with other fields is an update.
CREATE FUNCTION ci_change_kind(p_change_type TEXT, p_field_name TEXT, p_old_value JSONB, p_new_value JSONB, p_alone BOOLEAN)
RETURNS TEXT
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT CASE
        WHEN p_change_type IN ('type_change', 'type_changed') THEN 'type_changed'
        WHEN p_change_type = 'create' THEN 'created'
        WHEN p_change_type = 'delete' AND p_field_name = 'merged_into_id' THEN 'merged'
        WHEN p_change_type = 'delete' THEN 'deleted'
        WHEN p_change_type = 'reconciliation_decision' AND p_field_name = 'merged_ci' THEN 'merged'
        WHEN p_change_type = 'reconciliation_decision' AND p_field_name = 'unmerged_ci' THEN 'restored'
        WHEN p_field_name = 'deleted_at' AND (p_new_value IS NULL OR p_new_value = 'null'::jsonb) THEN 'restored'
        WHEN p_change_type = 'status_change' AND p_alone THEN 'status_changed'
        WHEN p_change_type = 'relationship_change' AND (p_old_value IS NULL OR p_old_value = 'null'::jsonb)
            AND p_new_value IS NOT NULL AND p_new_value <> 'null'::jsonb THEN 'relationship_added'
        WHEN p_change_type = 'relationship_change' AND (p_new_value IS NULL OR p_new_value = 'null'::jsonb)
            AND p_old_value IS NOT NULL AND p_old_value <> 'null'::jsonb THEN 'relationship_removed'
        ELSE 'updated'
    END
$$;

-- Backfill. Existing rows have no change set: the rows of one write of a CI
-- share the CI and the transaction time (created_at). The scope trigger is
-- off, so the rows keep the client and site of the time of the change.
ALTER TABLE ci_change DROP CONSTRAINT chk_ci_change_type;
ALTER TABLE ci_change DISABLE TRIGGER ci_change_scope;
UPDATE ci_change c
SET change_type = CASE WHEN c.change_type = 'type_change' THEN 'type_changed' ELSE c.change_type END,
    change_kind = ci_change_kind(c.change_type, c.field_name, c.old_value, c.new_value,
        c.change_type <> 'status_change' OR NOT EXISTS (
            SELECT 1 FROM ci_change o WHERE o.ci_id = c.ci_id AND o.created_at = c.created_at AND o.id <> c.id));
ALTER TABLE ci_change ENABLE TRIGGER ci_change_scope;

ALTER TABLE ci_change ADD CONSTRAINT chk_ci_change_type CHECK (change_type IN (
    'create', 'update', 'delete', 'status_change', 'relationship_change',
    'attribute_change', 'type_changed', 'lifecycle_transition',
    'location_change', 'movement', 'override', 'reconciliation_decision',
    'parent_child_change', 'assignment'
));
ALTER TABLE ci_change ALTER COLUMN change_kind SET NOT NULL;

-- ci_change_derive_kind sets change_kind of a row whose writer does not.
-- A status change is status_changed unless its change set already has
-- another row; writers of change sets with several rows set change_kind
-- themselves (ci). It also runs on UPDATE: the unmerge writes ci_change rows
-- back from a merge snapshot (ci/merge.go restoreRow), and a snapshot taken
-- before this migration has no change_kind and may name 'type_change'.
CREATE FUNCTION ci_change_derive_kind() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    alone BOOLEAN := true;
BEGIN
    IF NEW.change_type = 'type_change' THEN
        NEW.change_type := 'type_changed';
    END IF;
    IF NEW.change_kind IS NULL THEN
        IF NEW.change_type = 'status_change' AND NEW.change_set_id IS NOT NULL THEN
            alone := NOT EXISTS (SELECT 1 FROM ci_change c
                WHERE c.ci_id = NEW.ci_id AND c.change_set_id = NEW.change_set_id AND c.id <> NEW.id);
        END IF;
        NEW.change_kind := ci_change_kind(NEW.change_type, NEW.field_name, NEW.old_value, NEW.new_value, alone);
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER ci_change_kind BEFORE INSERT OR UPDATE ON ci_change
    FOR EACH ROW EXECUTE FUNCTION ci_change_derive_kind();
