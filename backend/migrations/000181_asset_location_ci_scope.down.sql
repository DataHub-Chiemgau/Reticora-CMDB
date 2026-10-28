-- Restores asset_propagate_location of 000088: the CIs follow under the
-- writer's RLS without the check of 000181, search_path not fixed.
CREATE OR REPLACE FUNCTION asset_propagate_location() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE ci SET location_id = NEW.location_id
    WHERE asset_id = NEW.id AND location_id IS DISTINCT FROM NEW.location_id;
    RETURN NULL;
END
$$;
ALTER FUNCTION asset_propagate_location() RESET ALL;
