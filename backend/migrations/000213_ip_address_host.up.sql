-- NET-06 (WP-300): the duplicate check compares the host address of
-- ip_address rows regardless of their prefix length (discovery stores
-- 10.0.0.5/24, POST /ips 10.0.0.5). host(inet) is not leakproof, so under
-- the row-level policies of the application role an expression index over
-- host(address) is never an index condition (K22, SR-04). A stored column
-- is compared with the leakproof text equality and is indexable under RLS.
-- Nothing writes it: ipCols/scanIP do not read it, INSERT and UPDATE never
-- set it.
ALTER TABLE ip_address ADD COLUMN address_host text GENERATED ALWAYS AS (host(address)) STORED;

CREATE INDEX IF NOT EXISTS idx_ip_address_org_vrf_host ON ip_address (organization_id, vrf_id, address_host);
