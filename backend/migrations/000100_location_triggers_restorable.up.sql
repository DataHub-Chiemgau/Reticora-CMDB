-- WP-101 (NFR-05, OPS-04): the logical backup must restore without errors.
-- pg_restore runs with an empty search_path; the trigger conditions on
-- location compared ltree values with IS DISTINCT FROM, whose operator lives
-- in the schema of the ltree extension, and failed with
-- "operator does not exist: public.ltree = public.ltree". The conditions now
-- compare the text form of the path (pg_catalog operator); an ltree and its
-- text form correspond one to one, so the triggers fire exactly as before.

DROP TRIGGER trg_location_propagate ON location;
CREATE TRIGGER trg_location_propagate AFTER UPDATE ON location
    FOR EACH ROW
    WHEN (OLD.path::text IS DISTINCT FROM NEW.path::text OR OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION location_propagate();

DROP TRIGGER trg_location_refresh_ci ON location;
CREATE TRIGGER trg_location_refresh_ci AFTER UPDATE ON location
    FOR EACH ROW WHEN (OLD.path::text IS DISTINCT FROM NEW.path::text
                       OR OLD.site_id IS DISTINCT FROM NEW.site_id
                       OR OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION location_refresh_ci();
