-- Reverts 000304: subnet.nameserver_set_id and the table nameserver_set go.
DROP INDEX idx_subnet_nameserver_set;
ALTER TABLE subnet DROP CONSTRAINT subnet_nameserver_set_fkey;
ALTER TABLE subnet DROP COLUMN nameserver_set_id;
DROP TABLE nameserver_set;
