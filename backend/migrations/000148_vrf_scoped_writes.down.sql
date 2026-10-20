-- Reverts WP-372: vrf keeps only vrf_isolation (000092); ENABLE and FORCE
-- row level security stay.

DROP POLICY vrf_scoped_delete ON vrf;
DROP POLICY vrf_scoped_update ON vrf;
DROP POLICY vrf_scoped_insert ON vrf;
