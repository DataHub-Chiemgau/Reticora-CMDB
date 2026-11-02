-- WP-487 down: back to the ci_change of migration 000166. The AUD-03
-- columns and the trigger go, 'type_changed' becomes 'type_change' again.

DROP TRIGGER ci_change_kind ON ci_change;
DROP FUNCTION ci_change_derive_kind();
DROP FUNCTION ci_change_kind(TEXT, TEXT, JSONB, JSONB, BOOLEAN);

ALTER TABLE ci_change DROP CONSTRAINT chk_ci_change_type;
ALTER TABLE ci_change DISABLE TRIGGER ci_change_scope;
UPDATE ci_change SET change_type = 'type_change' WHERE change_type = 'type_changed';
ALTER TABLE ci_change ENABLE TRIGGER ci_change_scope;
ALTER TABLE ci_change ADD CONSTRAINT chk_ci_change_type CHECK (change_type IN (
    'create', 'update', 'delete', 'status_change', 'relationship_change',
    'attribute_change', 'type_change', 'lifecycle_transition',
    'location_change', 'movement', 'override', 'reconciliation_decision',
    'parent_child_change', 'assignment'
));

ALTER TABLE ci_change
    DROP COLUMN observed_at,
    DROP COLUMN correlation_id,
    DROP COLUMN request_id,
    DROP COLUMN source,
    DROP COLUMN collector_id,
    DROP COLUMN actor_user_id,
    DROP COLUMN change_set_id,
    DROP COLUMN change_kind;
