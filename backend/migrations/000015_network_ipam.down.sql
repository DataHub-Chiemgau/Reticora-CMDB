-- Down migration for network/IPAM tables

DROP INDEX IF EXISTS idx_cable_target;
DROP INDEX IF EXISTS idx_cable_source;
DROP INDEX IF EXISTS idx_cable_org;
DROP INDEX IF EXISTS idx_ip_address_subnet;
DROP INDEX IF EXISTS idx_ip_address_org;
DROP INDEX IF EXISTS idx_subnet_org;
DROP INDEX IF EXISTS idx_network_interface_org;
DROP INDEX IF EXISTS idx_network_interface_ci;

DROP POLICY IF EXISTS org_isolation ON cable;
DROP POLICY IF EXISTS org_isolation ON ip_address;
DROP POLICY IF EXISTS org_isolation ON subnet;
DROP POLICY IF EXISTS org_isolation ON network_interface;

DROP TABLE IF EXISTS cable;
DROP TABLE IF EXISTS ip_address;
DROP TABLE IF EXISTS subnet;
DROP TABLE IF EXISTS network_interface;
