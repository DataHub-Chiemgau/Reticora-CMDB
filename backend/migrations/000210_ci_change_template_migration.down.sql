-- Down of 000210: chk_ci_change_type with the list of 000167. It fails
-- while rows with change_type template_migration exist: the history of a
-- template migration is not rewritten.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM ci_change WHERE change_type = 'template_migration') THEN
        RAISE EXCEPTION 'ci_change holds rows of change_type template_migration; 000210 cannot be rolled back';
    END IF;
END
$$;

ALTER TABLE ci_change DROP CONSTRAINT chk_ci_change_type;
ALTER TABLE ci_change ADD CONSTRAINT chk_ci_change_type CHECK (change_type IN (
    'create', 'update', 'delete', 'status_change', 'relationship_change',
    'attribute_change', 'type_changed', 'lifecycle_transition',
    'location_change', 'movement', 'override', 'reconciliation_decision',
    'parent_child_change', 'assignment'
));
