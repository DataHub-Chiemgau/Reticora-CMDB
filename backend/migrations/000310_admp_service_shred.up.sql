-- Migration 000310: crypto-shredding of the sealed service properties
-- (ADM-05, WP-1074, E-243, Konvention 16). UpsertService seals the
-- personal values of admp_service.properties into pii_subject and keeps
-- only their surrogate references ({"$pii": "<surrogate_id>"}). A
-- reference the row no longer carries (a refresh replaced the value) and
-- every reference of a deleted row (expiry, the cascade of an account or
-- an organization, a direct delete) deletes its pii_subject row, so no
-- personal value outlives its service. A row only carries references
-- created for it (UpsertService neutralises foreign ones), so shredding
-- never hits a subject of another object.

-- admp_service_pii_refs returns the surrogate ids referenced in props at
-- any depth.
CREATE FUNCTION admp_service_pii_refs(props jsonb) RETURNS SETOF uuid
LANGUAGE sql
IMMUTABLE
SET search_path = pg_catalog, public, pg_temp
AS $$
    SELECT DISTINCT (ref #>> '{}')::uuid
      FROM jsonb_path_query(props, 'lax $.**."$pii"') AS ref
     WHERE jsonb_typeof(ref) = 'string'
       AND (ref #>> '{}') ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
$$;

CREATE FUNCTION admp_service_shred() RETURNS trigger
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

CREATE TRIGGER trg_admp_service_shred
    AFTER DELETE ON admp_service
    FOR EACH ROW EXECUTE FUNCTION admp_service_shred();
CREATE TRIGGER trg_admp_service_shred_replaced
    AFTER UPDATE OF properties ON admp_service
    FOR EACH ROW WHEN (OLD.properties IS DISTINCT FROM NEW.properties)
    EXECUTE FUNCTION admp_service_shred();
