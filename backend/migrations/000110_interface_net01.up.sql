-- Migration 000110: network_interface after NET-01 (WP-123).
--
-- The columns take the names of NET-01 (mac, if_type); the name of an
-- interface is unique per CI. if_index, mtu, miss_count and the index
-- (org, mac) (000098) and the vlan_id reference exist already.

ALTER TABLE network_interface RENAME COLUMN mac_address TO mac;
ALTER TABLE network_interface RENAME COLUMN interface_type TO if_type;
ALTER TABLE network_interface RENAME CONSTRAINT chk_interface_type TO network_interface_if_type_check;

-- Duplicate names of one CI keep their rows (cables and IP addresses point
-- to them): every duplicate after the oldest gets a numbered suffix, the
-- next number whose name the CI does not have yet. An existing 'eth0 (1)'
-- keeps its name, a second 'eth0' becomes 'eth0 (2)' (F-310, E-94).
ALTER TABLE network_interface DISABLE TRIGGER trg_network_interface_updated_at;
DO $$
DECLARE
    dup record;
    n integer;
BEGIN
    FOR dup IN
        SELECT id, ci_id, name FROM (
            SELECT id, ci_id, name, created_at,
                   row_number() OVER (PARTITION BY ci_id, name ORDER BY created_at, id) AS rank
            FROM network_interface) d
        WHERE d.rank > 1
        ORDER BY ci_id, name, created_at, id
    LOOP
        n := 1;
        WHILE EXISTS (SELECT 1 FROM network_interface
                      WHERE ci_id = dup.ci_id AND name = dup.name || ' (' || n || ')') LOOP
            n := n + 1;
        END LOOP;
        UPDATE network_interface SET name = dup.name || ' (' || n || ')' WHERE id = dup.id;
    END LOOP;
END $$;
ALTER TABLE network_interface ENABLE TRIGGER trg_network_interface_updated_at;

ALTER TABLE network_interface ADD CONSTRAINT network_interface_ci_name_key UNIQUE (ci_id, name);
