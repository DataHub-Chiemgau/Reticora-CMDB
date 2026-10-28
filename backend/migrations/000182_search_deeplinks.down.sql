-- Migration 000182 down: the index functions of 000132 and their constant
-- urls '/locations' and '/contacts' again.

CREATE OR REPLACE FUNCTION search_index_location() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM search_document
         WHERE organization_id = OLD.organization_id AND entity_type = 'location' AND entity_id = OLD.id;
        RETURN OLD;
    END IF;
    INSERT INTO search_document (organization_id, entity_type, entity_id, title, summary, url, metadata)
    VALUES (NEW.organization_id, 'location', NEW.id, NEW.name, COALESCE(NEW.kind, ''), '/locations', '{}'::jsonb)
    ON CONFLICT (organization_id, entity_type, entity_id) DO UPDATE
        SET title = EXCLUDED.title, summary = EXCLUDED.summary, updated_at = now();
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
            concat_ws(' ', NEW.email, NEW.phone, NEW.role, NEW.department, NEW.notes), '/contacts', '{}'::jsonb)
    ON CONFLICT (organization_id, entity_type, entity_id) DO UPDATE
        SET title = EXCLUDED.title, summary = EXCLUDED.summary, updated_at = now();
    RETURN NEW;
END
$$;

UPDATE search_document SET url = '/locations' WHERE entity_type = 'location' AND url <> '/locations';
UPDATE search_document SET url = '/contacts' WHERE entity_type = 'contact' AND url <> '/contacts';
