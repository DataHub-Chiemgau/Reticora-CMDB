DROP INDEX review_item_open_age;
DROP INDEX review_item_open_object;
ALTER TABLE review_item DROP CONSTRAINT review_item_action_check, DROP COLUMN action, DROP COLUMN object_key;

INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT organization_id, 'review_item', to_jsonb(r), 'review kind ' || kind || ' removed by down migration', '000097'
FROM review_item r WHERE kind IN ('scope_suggested', 'duplicate_ip');
DELETE FROM review_item WHERE kind IN ('scope_suggested', 'duplicate_ip');
ALTER TABLE review_item DROP CONSTRAINT review_item_kind_check;
ALTER TABLE review_item ADD CONSTRAINT review_item_kind_check
    CHECK (kind = ANY (ARRAY['ambiguous_identity', 'conflicting_values', 'unclassified_device', 'override_conflict',
        'unlicensed_ci', 'serial_mismatch', 'probable_duplicate', 'resurrected_device']));
