-- WP-032 (SRC-01): search hits carry the client and site of their entity.
--
-- 1. client_id/site_id on search_document, derived by trigger from the
--    entity: ci, ticket, location (location_node) with site; asset, contact
--    and reservation (via its asset) with client only. Documents and
--    compliance entries span several objects and stay org-wide; their hits
--    keep the query-time check against the entity table.
-- 2. When an entity changes client or site, its search rows follow.
-- 3. Client and site predicate (E-09 NULL semantics).
-- 4. The entity type check admits 'location' and 'reservation', which the
--    reindex writes since WP-015.

ALTER TABLE search_document ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;

ALTER TABLE search_document DROP CONSTRAINT search_document_entity_type_check;
ALTER TABLE search_document ADD CONSTRAINT search_document_entity_type_check
    CHECK (entity_type = ANY (ARRAY['ci', 'asset', 'document', 'ticket', 'contact', 'compliance', 'location', 'reservation']));

CREATE FUNCTION derive_search_document_scope() RETURNS trigger
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
            SELECT n.client_id, n.site_id INTO NEW.client_id, NEW.site_id FROM location_node n WHERE n.id = NEW.entity_id;
        WHEN 'asset' THEN
            SELECT a.client_id INTO NEW.client_id FROM asset a WHERE a.id = NEW.entity_id;
        WHEN 'contact' THEN
            SELECT c.client_id INTO NEW.client_id FROM contact c WHERE c.id = NEW.entity_id;
        WHEN 'reservation' THEN
            SELECT a.client_id INTO NEW.client_id
              FROM reservation r JOIN asset a ON a.id = r.asset_id WHERE r.id = NEW.entity_id;
        ELSE
            NULL; -- document, compliance: org-wide
    END CASE;
    RETURN NEW;
END
$$;

CREATE TRIGGER search_document_scope BEFORE INSERT OR UPDATE ON search_document
    FOR EACH ROW EXECUTE FUNCTION derive_search_document_scope();

-- propagate_search_scope re-derives the search rows of the changed entity.
-- TG_ARGV[0] is the entity type.
CREATE FUNCTION propagate_search_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE search_document SET entity_id = entity_id
     WHERE entity_type = TG_ARGV[0] AND entity_id = NEW.id;
    IF TG_ARGV[0] = 'asset' THEN
        UPDATE search_document SET entity_id = entity_id
         WHERE entity_type = 'reservation'
           AND entity_id IN (SELECT r.id FROM reservation r WHERE r.asset_id = NEW.id);
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER ci_search_scope_propagation AFTER UPDATE ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_search_scope('ci');
CREATE TRIGGER ticket_search_scope_propagation AFTER UPDATE ON ticket
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_search_scope('ticket');
CREATE TRIGGER location_node_search_scope_propagation AFTER UPDATE ON location_node
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_search_scope('location');
CREATE TRIGGER asset_search_scope_propagation AFTER UPDATE ON asset
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION propagate_search_scope('asset');
CREATE TRIGGER contact_search_scope_propagation AFTER UPDATE ON contact
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION propagate_search_scope('contact');
CREATE TRIGGER reservation_search_scope_propagation AFTER UPDATE ON reservation
    FOR EACH ROW WHEN (OLD.asset_id IS DISTINCT FROM NEW.asset_id)
    EXECUTE FUNCTION propagate_search_scope('reservation');

UPDATE search_document SET entity_id = entity_id;

CREATE INDEX idx_search_document_client ON search_document (client_id) WHERE client_id IS NOT NULL;

DROP POLICY search_document_tenant_isolation ON search_document;
CREATE POLICY search_document_tenant_isolation ON search_document
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    );
