-- WP-073 (ENT-03, ENT-08, CH21, E-22): a discovered device that would exceed
-- max_cis is held as review item unlicensed_ci with the full device record
-- (payload.device) instead of becoming a CI; nothing is lost. At most one
-- such item is open per device (payload.device_key).

ALTER TABLE review_item DROP CONSTRAINT review_item_kind_check;
ALTER TABLE review_item ADD CONSTRAINT review_item_kind_check
    CHECK (kind = ANY (ARRAY['ambiguous_identity', 'conflicting_values', 'unclassified_device', 'override_conflict',
                             'unlicensed_ci']));

CREATE UNIQUE INDEX review_item_open_unlicensed_ci
    ON review_item (organization_id, (payload ->> 'device_key'))
    WHERE kind = 'unlicensed_ci' AND status = 'open';
