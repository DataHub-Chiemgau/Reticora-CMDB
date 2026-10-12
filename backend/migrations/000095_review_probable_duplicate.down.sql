INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT organization_id, 'review_item', to_jsonb(r), 'review kind probable_duplicate removed by down migration', '000095'
FROM review_item r WHERE kind = 'probable_duplicate';
DELETE FROM review_item WHERE kind = 'probable_duplicate';
ALTER TABLE review_item DROP CONSTRAINT review_item_kind_check;
ALTER TABLE review_item ADD CONSTRAINT review_item_kind_check
    CHECK (kind = ANY (ARRAY['ambiguous_identity', 'conflicting_values', 'unclassified_device', 'override_conflict',
        'unlicensed_ci', 'serial_mismatch']));
