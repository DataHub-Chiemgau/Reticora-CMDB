-- WP-914 (IPM-14, IPM-20, REC-04, E-216): origin and pin of IP addresses.
--
-- * ip_address.origin manual | discovery | scan | import: where the row
--   came from. The API writes manual, the discovery sync discovery; scan
--   (WP-1063) and import (WP-920) follow. Backfill: type discovered is
--   discovery, every other row manual. The backfill neither touches
--   updated_at nor writes history rows (triggers disabled as in 000321).
-- * ip_address.pinned marks a binding as authoritative: the discovery sync
--   neither moves nor removes a pinned row, and an observation of its
--   address at another device opens the review ip_binding_conflict.
-- * idx_ip_address_pinned: the pinned rows by organization, VRF and host,
--   for the one set-based lookup of pinned addresses of other devices per
--   discovery sync (AK4); partial, so it stays as small as the pins.
-- * review_item_kind_check (000130) gains ip_binding_conflict.

ALTER TABLE ip_address
    ADD COLUMN origin text NOT NULL DEFAULT 'manual'
        CONSTRAINT ip_address_origin_check CHECK (origin IN ('manual', 'discovery', 'scan', 'import')),
    ADD COLUMN pinned boolean NOT NULL DEFAULT false;

ALTER TABLE ip_address DISABLE TRIGGER USER;
UPDATE ip_address SET origin = 'discovery' WHERE type = 'discovered';
ALTER TABLE ip_address ENABLE TRIGGER USER;

CREATE INDEX idx_ip_address_pinned ON ip_address (organization_id, vrf_id, address_host) WHERE pinned;

ALTER TABLE review_item DROP CONSTRAINT review_item_kind_check;
ALTER TABLE review_item ADD CONSTRAINT review_item_kind_check
    CHECK (kind = ANY (ARRAY['unclassified_device', 'ambiguous_identity', 'conflicting_values', 'probable_duplicate',
        'override_conflict', 'resurrected_device', 'unlicensed_ci', 'scope_suggested', 'serial_mismatch', 'duplicate_ip',
        'unknown_relationship', 'ip_binding_conflict']));
