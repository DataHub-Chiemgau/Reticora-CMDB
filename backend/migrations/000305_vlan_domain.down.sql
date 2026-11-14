-- Reverts 000305: the partial indexes of 000091, vlan.domain_id and the
-- tables vlan_domain_site and vlan_domain. VLANs of a domain become VLANs
-- without domain; a number that is then taken twice fails the index.
DROP INDEX idx_vlan_domain_site;
DROP INDEX vlan_domain_number;
DROP INDEX vlan_org_number;
DROP INDEX vlan_site_number;
ALTER TABLE vlan DROP CONSTRAINT vlan_domain_site_fkey;
ALTER TABLE vlan DROP CONSTRAINT vlan_domain_fkey;
ALTER TABLE vlan DROP COLUMN domain_id;
CREATE UNIQUE INDEX vlan_site_number ON vlan (organization_id, site_id, vlan_id) WHERE site_id IS NOT NULL;
CREATE UNIQUE INDEX vlan_org_number ON vlan (organization_id, vlan_id) WHERE site_id IS NULL;
DROP TABLE vlan_domain_site;
DROP FUNCTION vlan_domain_site_derive_client();
DROP TABLE vlan_domain;
