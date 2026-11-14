-- Migration 000328: crypto-shredding of the sealed connection properties
-- (ADM-07, WP-1075, E-243, Konvention 16). Since WP-1075 UpsertConnection
-- seals the personal values of admp_connection.properties into
-- pii_subject (as UpsertService does since migration 000310) instead of
-- refusing them, and keeps only their surrogate references
-- ({"$pii": "<surrogate_id>"}). A reference the row no longer carries (a
-- refresh replaced the value) and every reference of a deleted row (ADMP
-- answered 404, expiry, the cascade of an account or an organization)
-- deletes its pii_subject row, so no personal value outlives its
-- connection. A row only carries references created for it
-- (UpsertConnection neutralises foreign ones), so shredding never hits a
-- subject of another object. The holder and the address keep their own
-- triggers (migration 000297).

CREATE FUNCTION admp_connection_properties_shred() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM pii_subject
         WHERE organization_id = OLD.organization_id
           AND surrogate_id IN (SELECT admp_service_pii_refs(OLD.properties));
    ELSE
        DELETE FROM pii_subject
         WHERE organization_id = OLD.organization_id
           AND surrogate_id IN (SELECT admp_service_pii_refs(OLD.properties)
                                EXCEPT SELECT admp_service_pii_refs(NEW.properties));
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_admp_connection_properties_shred
    AFTER DELETE ON admp_connection
    FOR EACH ROW EXECUTE FUNCTION admp_connection_properties_shred();
CREATE TRIGGER trg_admp_connection_properties_shred_replaced
    AFTER UPDATE OF properties ON admp_connection
    FOR EACH ROW WHEN (OLD.properties IS DISTINCT FROM NEW.properties)
    EXECUTE FUNCTION admp_connection_properties_shred();
