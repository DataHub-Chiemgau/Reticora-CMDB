-- WP-088 (REC-08): the ten review types, the resolution action
-- (merge | new | dismiss | accept), one open item per object and type
-- (object_key) and the index for the escalation of items open > 14 days.
ALTER TABLE review_item DROP CONSTRAINT review_item_kind_check;
ALTER TABLE review_item ADD CONSTRAINT review_item_kind_check
    CHECK (kind = ANY (ARRAY['unclassified_device', 'ambiguous_identity', 'conflicting_values', 'probable_duplicate',
        'override_conflict', 'resurrected_device', 'unlicensed_ci', 'scope_suggested', 'serial_mismatch', 'duplicate_ip']));

ALTER TABLE review_item
    ADD COLUMN object_key text,
    ADD COLUMN action text,
    ADD CONSTRAINT review_item_action_check CHECK (action IN ('merge', 'new', 'dismiss', 'accept'));

-- Open items get the key their creator uses now; of several open items of
-- one object and type only the oldest is keyed.
WITH keyed AS (
    SELECT id, organization_id, kind, CASE kind
        WHEN 'override_conflict' THEN 'ci:' || (payload ->> 'ci_id') || '/' || (payload ->> 'field')
        WHEN 'unlicensed_ci' THEN NULLIF(payload ->> 'device_key', '')
        WHEN 'conflicting_values' THEN 'ci:' || (payload ->> 'matched_ci_id')
        WHEN 'probable_duplicate' THEN 'ci:' || (payload ->> 'ci_id')
        WHEN 'resurrected_device' THEN 'ci:' || (SELECT string_agg(c::text, ',' ORDER BY c::text) FROM unnest(candidate_ci_ids) AS c)
        END AS key, created_at
    FROM review_item WHERE status = 'open'
), first AS (
    SELECT id, key, row_number() OVER (PARTITION BY organization_id, kind, key ORDER BY created_at, id) AS n
    FROM keyed WHERE key IS NOT NULL
)
UPDATE review_item r SET object_key = first.key FROM first WHERE r.id = first.id AND first.n = 1;

CREATE UNIQUE INDEX review_item_open_object ON review_item (organization_id, kind, object_key)
    WHERE status = 'open' AND object_key IS NOT NULL;
CREATE INDEX review_item_open_age ON review_item (organization_id, created_at) WHERE status = 'open';
