-- WP-087 (CI-13): a device that matches a soft-deleted CI is held in a
-- resurrected_device review (reactivate or new CI), never created twice.
ALTER TABLE review_item DROP CONSTRAINT review_item_kind_check;
ALTER TABLE review_item ADD CONSTRAINT review_item_kind_check
    CHECK (kind = ANY (ARRAY['ambiguous_identity', 'conflicting_values', 'unclassified_device', 'override_conflict',
        'unlicensed_ci', 'serial_mismatch', 'probable_duplicate', 'resurrected_device']));
