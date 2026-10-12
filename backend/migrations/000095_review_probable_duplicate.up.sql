-- WP-086 (REC-02, CH29): a hostname/FQDN hit creates a new CI and a
-- probable_duplicate review instead of a merge.
ALTER TABLE review_item DROP CONSTRAINT review_item_kind_check;
ALTER TABLE review_item ADD CONSTRAINT review_item_kind_check
    CHECK (kind = ANY (ARRAY['ambiguous_identity', 'conflicting_values', 'unclassified_device', 'override_conflict',
        'unlicensed_ci', 'serial_mismatch', 'probable_duplicate']));
