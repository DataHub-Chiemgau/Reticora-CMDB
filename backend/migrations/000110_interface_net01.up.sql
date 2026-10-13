-- Migration 000110: network_interface after NET-01 (WP-123).
--
-- The columns take the names of NET-01 (mac, if_type); the name of an
-- interface is unique per CI. if_index, mtu, miss_count and the index
-- (org, mac) (000098) and the vlan_id reference exist already.

ALTER TABLE network_interface RENAME COLUMN mac_address TO mac;
ALTER TABLE network_interface RENAME COLUMN interface_type TO if_type;
ALTER TABLE network_interface RENAME CONSTRAINT chk_interface_type TO network_interface_if_type_check;

-- Duplicate names of one CI keep their rows (cables and IP addresses point
-- to them): every duplicate after the oldest gets a numbered suffix.
ALTER TABLE network_interface DISABLE TRIGGER trg_network_interface_updated_at;
UPDATE network_interface n SET name = n.name || ' (' || d.rank || ')'
FROM (SELECT id, row_number() OVER (PARTITION BY ci_id, name ORDER BY created_at, id) - 1 AS rank
      FROM network_interface) d
WHERE d.id = n.id AND d.rank > 0;
ALTER TABLE network_interface ENABLE TRIGGER trg_network_interface_updated_at;

ALTER TABLE network_interface ADD CONSTRAINT network_interface_ci_name_key UNIQUE (ci_id, name);
