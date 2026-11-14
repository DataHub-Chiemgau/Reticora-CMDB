-- Reverts 000297: the ADMP cache, its triggers and functions go. The unique
-- keys on network_interface and pii_subject go only when no foreign key
-- references them any more (since 000287 others do).
DROP TRIGGER IF EXISTS trg_ci_admp_service_port_scope ON ci;
DROP TRIGGER IF EXISTS trg_network_interface_admp_service_port ON network_interface;
DROP TABLE admp_service_port;
DROP TABLE admp_service;
DROP TABLE admp_connection;
DROP FUNCTION admp_service_port_follow();
DROP FUNCTION admp_connection_shred();
DROP FUNCTION admp_connection_shred_replaced();
DROP FUNCTION admp_connection_subject_check();
DROP FUNCTION admp_service_deleted();
DROP FUNCTION admp_service_connection_moved();
DROP FUNCTION admp_service_port_derive();
DROP FUNCTION admp_cache_refresh_client(uuid, uuid);
DROP FUNCTION admp_cache_refresh_connection_client(uuid, uuid);
DROP FUNCTION admp_service_port_scope();
DROP FUNCTION admp_cache_account_scope();

DO $$
DECLARE
    k record;
BEGIN
    FOR k IN SELECT c.conrelid, c.conname, c.conindid FROM pg_constraint c
              WHERE (c.conrelid = 'network_interface'::regclass AND c.conname = 'network_interface_id_organization_key')
                 OR (c.conrelid = 'pii_subject'::regclass
                     AND c.conname IN ('pii_subject_surrogate_id_organization_key', 'pii_subject_id_organization_key')) LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_constraint f WHERE f.contype = 'f' AND f.confrelid = k.conrelid AND f.conindid = k.conindid) THEN
            EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', k.conrelid::regclass, k.conname);
        END IF;
    END LOOP;
END
$$;
