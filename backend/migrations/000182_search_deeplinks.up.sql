-- Migration 000182: search hits of locations and contacts lead to their
-- entity (SRC-01, WP-594, F-334).
--
-- The index rows of 000132 carried the constant urls '/locations' (the root
-- of the tree) and '/contacts' (a route the SPA does not have). A location
-- now links to its node in the tree ('/locations?node=<id>'); a contact has
-- no page in phase 1, its url is empty and the command palette shows the
-- contact in a card instead. The upsert also writes the url, so a row of an
-- older version is corrected on the next write. The expressions are the
-- ones of the reindex (search/pg_repository.go).

CREATE OR REPLACE FUNCTION search_index_location() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM search_document
         WHERE organization_id = OLD.organization_id AND entity_type = 'location' AND entity_id = OLD.id;
        RETURN OLD;
    END IF;
    INSERT INTO search_document (organization_id, entity_type, entity_id, title, summary, url, metadata)
    VALUES (NEW.organization_id, 'location', NEW.id, NEW.name, COALESCE(NEW.kind, ''), '/locations?node=' || NEW.id, '{}'::jsonb)
    ON CONFLICT (organization_id, entity_type, entity_id) DO UPDATE
        SET title = EXCLUDED.title, summary = EXCLUDED.summary, url = EXCLUDED.url, updated_at = now();
    RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION search_index_contact() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM search_document
         WHERE organization_id = OLD.organization_id AND entity_type = 'contact' AND entity_id = OLD.id;
        RETURN OLD;
    END IF;
    INSERT INTO search_document (organization_id, entity_type, entity_id, title, summary, url, metadata)
    VALUES (NEW.organization_id, 'contact', NEW.id, COALESCE(NEW.display_name, ''),
            concat_ws(' ', NEW.email, NEW.phone, NEW.role, NEW.department, NEW.notes), '', '{}'::jsonb)
    ON CONFLICT (organization_id, entity_type, entity_id) DO UPDATE
        SET title = EXCLUDED.title, summary = EXCLUDED.summary, url = EXCLUDED.url, updated_at = now();
    RETURN NEW;
END
$$;

-- Existing rows get the new urls.
UPDATE search_document SET url = '/locations?node=' || entity_id
 WHERE entity_type = 'location' AND url <> '/locations?node=' || entity_id;
UPDATE search_document SET url = ''
 WHERE entity_type = 'contact' AND url <> '';
