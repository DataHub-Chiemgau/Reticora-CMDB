DROP INDEX idx_network_interface_vlan;
ALTER TABLE network_interface DROP CONSTRAINT network_interface_vlan_fkey;
ALTER TABLE network_interface DROP COLUMN vlan_id;

DROP INDEX idx_subnet_vlan;
ALTER TABLE subnet DROP CONSTRAINT subnet_vlan_fkey;
ALTER TABLE subnet ADD COLUMN vlan_number integer;
UPDATE subnet s SET vlan_number = v.vlan_id FROM vlan v WHERE v.id = s.vlan_id;
UPDATE subnet s SET vlan_number = (q.row_data ->> 'vlan_number')::integer
FROM migration_quarantine q
WHERE q.migration = '000091' AND q.source_table = 'subnet' AND s.id = (q.row_data ->> 'id')::uuid;
DELETE FROM migration_quarantine WHERE migration = '000091';
ALTER TABLE subnet DROP COLUMN vlan_id;
ALTER TABLE subnet RENAME COLUMN vlan_number TO vlan_id;

DROP TABLE vlan;
DROP FUNCTION vlan_derive_client();
