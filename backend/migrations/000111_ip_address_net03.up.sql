-- Migration 000111: ip_address after NET-03 (WP-124).
--
-- type (discovered | reserved | static | dhcp) replaces status. Data
-- transfer: dhcp and reserved keep their value; an active or deprecated
-- address that discovery has seen (last_seen_at) is discovered, otherwise
-- static; available (a free placeholder) becomes reserved. The partial
-- unique indexes with vrf_id (000091) exist already.

ALTER TABLE ip_address DROP CONSTRAINT chk_ip_status;
ALTER TABLE ip_address RENAME COLUMN status TO type;
ALTER TABLE ip_address DISABLE TRIGGER trg_ip_address_updated_at;
UPDATE ip_address SET type = CASE
    WHEN type IN ('dhcp', 'reserved') THEN type
    WHEN type = 'available' THEN 'reserved'
    WHEN last_seen_at IS NOT NULL THEN 'discovered'
    ELSE 'static'
END;
ALTER TABLE ip_address ENABLE TRIGGER trg_ip_address_updated_at;
ALTER TABLE ip_address ALTER COLUMN type SET DEFAULT 'static';
ALTER TABLE ip_address ADD CONSTRAINT ip_address_type_check
    CHECK (type IN ('discovered', 'reserved', 'static', 'dhcp'));
