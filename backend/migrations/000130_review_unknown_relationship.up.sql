-- WP-158 (TOP-01): a neighbor reported with a relationship key the
-- topology does not know becomes the review item unknown_relationship
-- instead of a connected_to edge.
ALTER TABLE review_item DROP CONSTRAINT review_item_kind_check;
ALTER TABLE review_item ADD CONSTRAINT review_item_kind_check
    CHECK (kind = ANY (ARRAY['unclassified_device', 'ambiguous_identity', 'conflicting_values', 'probable_duplicate',
        'override_conflict', 'resurrected_device', 'unlicensed_ci', 'scope_suggested', 'serial_mismatch', 'duplicate_ip',
        'unknown_relationship']));
