-- Reverts 000329: the free-number search and the origin AS of subnets go.
DROP FUNCTION IF EXISTS asn_next_free(uuid, bigint, bigint);
DROP INDEX IF EXISTS idx_subnet_origin_asn;
ALTER TABLE subnet DROP CONSTRAINT IF EXISTS subnet_origin_asn_fkey;
ALTER TABLE subnet DROP COLUMN IF EXISTS origin_asn_id;
