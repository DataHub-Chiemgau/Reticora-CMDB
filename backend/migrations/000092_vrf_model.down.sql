DROP INDEX uq_ip_address_org_vrf_interface_address;
DROP INDEX uq_ip_address_org_vrf_address_no_interface;
CREATE UNIQUE INDEX uq_ip_address_org_address_no_interface ON ip_address (organization_id, address) WHERE interface_id IS NULL;
CREATE UNIQUE INDEX uq_ip_address_org_interface_address ON ip_address (organization_id, interface_id, address) WHERE interface_id IS NOT NULL;

DROP TRIGGER trg_subnet_propagate_vrf ON subnet;
DROP FUNCTION subnet_propagate_vrf();
DROP TRIGGER trg_ip_address_vrf ON ip_address;
DROP FUNCTION ip_address_vrf();
ALTER TABLE ip_address DROP CONSTRAINT ip_address_vrf_fkey;
ALTER TABLE ip_address DROP COLUMN vrf_id;

DROP TRIGGER trg_subnet_default_vrf ON subnet;
DROP FUNCTION subnet_default_vrf();
DROP FUNCTION default_vrf_of(uuid);
ALTER TABLE subnet DROP CONSTRAINT subnet_org_vrf_cidr_key;
ALTER TABLE subnet ADD CONSTRAINT subnet_organization_id_cidr_key UNIQUE (organization_id, cidr);
ALTER TABLE subnet DROP CONSTRAINT subnet_vrf_fkey;
ALTER TABLE subnet DROP COLUMN vrf_id;

DROP TRIGGER trg_organization_default_vrf ON organization;
DROP FUNCTION organization_default_vrf();
DROP TABLE vrf;
DROP FUNCTION vrf_protect_default();
