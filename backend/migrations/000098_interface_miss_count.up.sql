-- WP-090 (REC-07, NET-01): interfaces and IP addresses count the
-- consecutive discovery observations of their CI that did not report them;
-- after three they are removed. NET-01 also asks for if_index, mtu and an
-- index on (organization, mac).
ALTER TABLE network_interface
    ADD COLUMN miss_count integer NOT NULL DEFAULT 0 CHECK (miss_count >= 0),
    ADD COLUMN if_index integer,
    ADD COLUMN mtu integer;
ALTER TABLE ip_address
    ADD COLUMN miss_count integer NOT NULL DEFAULT 0 CHECK (miss_count >= 0);
CREATE INDEX idx_network_interface_org_mac ON network_interface (organization_id, mac_address) WHERE mac_address IS NOT NULL;
