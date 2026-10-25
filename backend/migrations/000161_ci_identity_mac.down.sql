-- Migration ci_identity_mac down: the identity search falls back to the
-- fingerprint in ci.attributes; the policies, the index and the TRUNCATE
-- guard go with the table.
DROP TRIGGER IF EXISTS ci_identity_mac_update ON ci;
DROP TRIGGER IF EXISTS ci_identity_mac_insert ON ci;
DROP TABLE IF EXISTS ci_identity_mac;
DROP FUNCTION IF EXISTS sync_ci_identity_mac();
DROP FUNCTION IF EXISTS ci_identity_macs(macaddr, jsonb);
