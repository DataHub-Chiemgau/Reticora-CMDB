-- WP-058 (REC-12, REC-08): the central write decision queues a review of
-- kind override_conflict when a source reports a value that differs from a
-- manual override. At most one such item is open per CI field.

ALTER TABLE review_item DROP CONSTRAINT review_item_kind_check;
ALTER TABLE review_item ADD CONSTRAINT review_item_kind_check
    CHECK (kind = ANY (ARRAY['ambiguous_identity', 'conflicting_values', 'unclassified_device', 'override_conflict']));

CREATE UNIQUE INDEX review_item_open_override_conflict
    ON review_item (organization_id, (payload ->> 'ci_id'), (payload ->> 'field'))
    WHERE kind = 'override_conflict' AND status = 'open';
