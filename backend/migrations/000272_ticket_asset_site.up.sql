-- WP-828 (TEN-05, TEN-09; finding of the channel matrix phase 2): a ticket
-- of an asset takes the site of the asset as well.
--
-- derive_ticket_scope (000064) took client and site from the related CI,
-- without a CI only the client from the asset: asset.site_id came later
-- (000151, WP-392). A ticket of an asset without CI therefore had no site.
-- Row level security keeps rows without site readable for every site scope
-- (E-09), so the search index (search_document of the ticket) showed the
-- ticket of an asset at site 1 to a principal restricted to site 2, and a
-- principal restricted in the site scope could open no ticket of an asset
-- (WITH CHECK, 403). The REST reads hid it only through the subquery
-- ticketVisible on the asset.
--
-- 1. derive_ticket_scope takes client and site from the asset when the CI
--    gives no client, as derive_document_link_scope does (000217). It reads
--    the asset under the writer's row level security like before. The
--    propagation asset_module_scope_propagation (000151) re-derives the
--    tickets of an asset whose client or site changes.
-- 2. Backfill: the tickets of an asset without site are derived again; the
--    propagation trigger of ticket carries the site to comments, links and
--    the search index. updated_at stays (pattern 000107).

CREATE OR REPLACE FUNCTION derive_ticket_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    IF NEW.related_ci_id IS NOT NULL THEN
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id FROM ci c WHERE c.id = NEW.related_ci_id;
    END IF;
    IF NEW.client_id IS NULL AND NEW.related_asset_id IS NOT NULL THEN
        SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id FROM asset a WHERE a.id = NEW.related_asset_id;
    END IF;
    RETURN NEW;
END
$$;

ALTER TABLE ticket DISABLE TRIGGER trg_ticket_updated_at;
UPDATE ticket SET related_asset_id = related_asset_id WHERE related_asset_id IS NOT NULL AND site_id IS NULL;
ALTER TABLE ticket ENABLE TRIGGER trg_ticket_updated_at;
