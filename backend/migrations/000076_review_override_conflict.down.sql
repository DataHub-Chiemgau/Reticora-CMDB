DROP INDEX review_item_open_override_conflict;
DELETE FROM review_item WHERE kind = 'override_conflict';
ALTER TABLE review_item DROP CONSTRAINT review_item_kind_check;
ALTER TABLE review_item ADD CONSTRAINT review_item_kind_check
    CHECK (kind = ANY (ARRAY['ambiguous_identity', 'conflicting_values', 'unclassified_device']));
