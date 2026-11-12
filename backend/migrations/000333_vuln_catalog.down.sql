-- Reverts 000333: the global vulnerability catalog.

DROP TABLE vuln_affected;
DROP TABLE vuln_advisory;
DROP TABLE vuln_source;
DROP FUNCTION reticora_vuln_catalog_write_guard();
