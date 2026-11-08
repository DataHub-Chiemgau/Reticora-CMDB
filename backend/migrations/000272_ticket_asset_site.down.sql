-- Migration 000272 down: derive_ticket_scope returns to its version of
-- 000064 (without CI only the client of the asset). The derived sites of
-- the backfill stay until the next change of a ticket re-derives it.
CREATE OR REPLACE FUNCTION derive_ticket_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    IF NEW.related_ci_id IS NOT NULL THEN
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id FROM ci c WHERE c.id = NEW.related_ci_id;
    END IF;
    IF NEW.client_id IS NULL AND NEW.related_asset_id IS NOT NULL THEN
        SELECT a.client_id INTO NEW.client_id FROM asset a WHERE a.id = NEW.related_asset_id;
    END IF;
    RETURN NEW;
END
$$;
