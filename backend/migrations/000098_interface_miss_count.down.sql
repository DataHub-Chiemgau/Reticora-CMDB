DROP INDEX idx_network_interface_org_mac;
ALTER TABLE ip_address DROP COLUMN miss_count;
ALTER TABLE network_interface DROP COLUMN mtu, DROP COLUMN if_index, DROP COLUMN miss_count;
