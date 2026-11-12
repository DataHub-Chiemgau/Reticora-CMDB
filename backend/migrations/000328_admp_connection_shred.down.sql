-- Reverts 000328: the shredding of the sealed connection properties goes.
DROP TRIGGER IF EXISTS trg_admp_connection_properties_shred_replaced ON admp_connection;
DROP TRIGGER IF EXISTS trg_admp_connection_properties_shred ON admp_connection;
DROP FUNCTION admp_connection_properties_shred();
