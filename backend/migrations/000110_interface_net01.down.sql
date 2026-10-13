-- Migration 000110 down: former column names, no unique name per CI.

ALTER TABLE network_interface DROP CONSTRAINT network_interface_ci_name_key;
ALTER TABLE network_interface RENAME CONSTRAINT network_interface_if_type_check TO chk_interface_type;
ALTER TABLE network_interface RENAME COLUMN if_type TO interface_type;
ALTER TABLE network_interface RENAME COLUMN mac TO mac_address;
