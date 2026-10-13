-- Migration 000132: locations and contacts as search hits (SRC-01, WP-186).
--
-- The entity kinds location and contact are allowed in search_document and
-- answered by the query (read permission location:read / contact:read), but
-- only a reindex wrote them. These triggers keep their index rows current on
-- every write, whatever path changes the row; the expressions are the ones
-- of the reindex (search/pg_repository.go). Contacts keep no personal data
-- in the index beyond what the contact row holds (surrogates with the PII
-- vault); their names come from the vault at query time.

CREATE FUNCTION search_index_location() RETURNS trigger
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

CREATE TRIGGER location_search_index AFTER INSERT OR UPDATE OF name, kind OR DELETE ON location
    FOR EACH ROW EXECUTE FUNCTION search_index_location();

CREATE FUNCTION search_index_contact() RETURNS trigger
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

CREATE TRIGGER contact_search_index AFTER INSERT OR UPDATE OF display_name, email, phone, role, department, notes OR DELETE ON contact
    FOR EACH ROW EXECUTE FUNCTION search_index_contact();

-- Existing locations and contacts are indexed once.
INSERT INTO search_document (organization_id, entity_type, entity_id, title, summary, url, metadata)
SELECT organization_id, 'location', id, name, COALESCE(kind, ''), '/locations', '{}'::jsonb FROM location
ON CONFLICT (organization_id, entity_type, entity_id) DO NOTHING;
INSERT INTO search_document (organization_id, entity_type, entity_id, title, summary, url, metadata)
SELECT organization_id, 'contact', id, COALESCE(display_name, ''), concat_ws(' ', email, phone, role, department, notes), '/contacts', '{}'::jsonb
FROM contact
ON CONFLICT (organization_id, entity_type, entity_id) DO NOTHING;
