-- Migration 000111 down: status instead of type (discovered and static
-- become active).

ALTER TABLE ip_address DROP CONSTRAINT ip_address_type_check;
ALTER TABLE ip_address DISABLE TRIGGER trg_ip_address_updated_at;
UPDATE ip_address SET type = 'active' WHERE type IN ('discovered', 'static');
ALTER TABLE ip_address ENABLE TRIGGER trg_ip_address_updated_at;
ALTER TABLE ip_address ALTER COLUMN type SET DEFAULT 'active';
ALTER TABLE ip_address RENAME COLUMN type TO status;
ALTER TABLE ip_address ADD CONSTRAINT chk_ip_status
    CHECK (status IN ('active', 'reserved', 'deprecated', 'dhcp', 'available'));
