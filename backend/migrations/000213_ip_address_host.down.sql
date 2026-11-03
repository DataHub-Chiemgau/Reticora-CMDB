DROP INDEX IF EXISTS idx_ip_address_org_vrf_host;
ALTER TABLE ip_address DROP COLUMN address_host;
