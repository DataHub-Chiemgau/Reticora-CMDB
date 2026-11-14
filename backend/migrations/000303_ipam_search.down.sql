DROP INDEX IF EXISTS idx_ip_address_org_state;

DROP TRIGGER IF EXISTS ip_address_search_index_insert ON ip_address;
DROP TRIGGER IF EXISTS ip_address_search_index_update ON ip_address;
DROP TRIGGER IF EXISTS ip_address_search_index_delete ON ip_address;
DROP FUNCTION IF EXISTS search_index_ip_address();
DROP TRIGGER IF EXISTS subnet_search_index_insert ON subnet;
DROP TRIGGER IF EXISTS subnet_search_index_update ON subnet;
DROP TRIGGER IF EXISTS subnet_search_index_delete ON subnet;
DROP FUNCTION IF EXISTS search_index_subnet();

DELETE FROM search_document WHERE entity_type IN ('subnet', 'ip_address');

-- The version of 000264.
CREATE OR REPLACE FUNCTION derive_search_document_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    CASE NEW.entity_type
        WHEN 'ci' THEN
            SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id FROM ci c WHERE c.id = NEW.entity_id;
        WHEN 'ticket' THEN
            SELECT t.client_id, t.site_id INTO NEW.client_id, NEW.site_id FROM ticket t WHERE t.id = NEW.entity_id;
        WHEN 'location' THEN
            SELECT l.client_id, l.site_id INTO NEW.client_id, NEW.site_id FROM location l WHERE l.id = NEW.entity_id;
        WHEN 'asset' THEN
            SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id FROM asset a WHERE a.id = NEW.entity_id;
        WHEN 'contact' THEN
            SELECT c.client_id INTO NEW.client_id FROM contact c WHERE c.id = NEW.entity_id;
        WHEN 'reservation' THEN
            -- The reservation carries the scope of its asset or bin
            -- (reservation_scope, 000258, 000264).
            SELECT r.client_id, r.site_id INTO NEW.client_id, NEW.site_id
              FROM reservation r WHERE r.id = NEW.entity_id;
        ELSE
            NULL; -- document, compliance: org-wide
    END CASE;
    RETURN NEW;
END
$$;

-- The check of 000067.
ALTER TABLE search_document DROP CONSTRAINT search_document_entity_type_check;
ALTER TABLE search_document ADD CONSTRAINT search_document_entity_type_check
    CHECK (entity_type = ANY (ARRAY['ci', 'asset', 'document', 'ticket', 'contact', 'compliance', 'location', 'reservation']));
