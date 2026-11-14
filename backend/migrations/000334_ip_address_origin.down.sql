-- Down of WP-914: removes the review type ip_binding_conflict and origin
-- and pin of IP addresses.
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT organization_id, 'review_item', to_jsonb(r), 'review kind ' || kind || ' removed by down migration', '000334'
FROM review_item r WHERE kind = 'ip_binding_conflict';
DELETE FROM review_item WHERE kind = 'ip_binding_conflict';
ALTER TABLE review_item DROP CONSTRAINT review_item_kind_check;
ALTER TABLE review_item ADD CONSTRAINT review_item_kind_check
    CHECK (kind = ANY (ARRAY['unclassified_device', 'ambiguous_identity', 'conflicting_values', 'probable_duplicate',
        'override_conflict', 'resurrected_device', 'unlicensed_ci', 'scope_suggested', 'serial_mismatch', 'duplicate_ip',
        'unknown_relationship']));

DROP INDEX idx_ip_address_pinned;
ALTER TABLE ip_address DROP COLUMN pinned, DROP COLUMN origin;
