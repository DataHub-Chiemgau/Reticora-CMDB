-- Reverts 000310: the shredding of the sealed service properties goes.
DROP TRIGGER IF EXISTS trg_admp_service_shred_replaced ON admp_service;
DROP TRIGGER IF EXISTS trg_admp_service_shred ON admp_service;
DROP FUNCTION admp_service_shred();
DROP FUNCTION admp_service_pii_refs(jsonb);
