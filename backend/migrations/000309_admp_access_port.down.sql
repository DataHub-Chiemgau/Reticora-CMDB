-- Reverts 000309: the ADMP port inventory, its triggers and functions and
-- the inventory time of the account go.
DROP TRIGGER IF EXISTS trg_ci_admp_access_port_scope ON ci;
DROP TRIGGER IF EXISTS trg_network_interface_admp_access_port ON network_interface;
DROP TABLE admp_access_port;
DROP FUNCTION admp_access_port_follow();
DROP FUNCTION admp_access_port_scope();
ALTER TABLE admp_account DROP COLUMN inventory_fetched_at;
