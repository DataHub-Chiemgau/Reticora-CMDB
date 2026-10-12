DROP TRIGGER trg_location_propagate ON location;
CREATE TRIGGER trg_location_propagate AFTER UPDATE ON location
    FOR EACH ROW
    WHEN (OLD.path IS DISTINCT FROM NEW.path OR OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION location_propagate();

DROP TRIGGER trg_location_refresh_ci ON location;
CREATE TRIGGER trg_location_refresh_ci AFTER UPDATE ON location
    FOR EACH ROW WHEN (OLD.path IS DISTINCT FROM NEW.path
                       OR OLD.site_id IS DISTINCT FROM NEW.site_id
                       OR OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION location_refresh_ci();
