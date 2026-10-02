-- WP-037 (AI-01): RAG chunks carry the client and site of their source
-- object, so the policy hides chunks outside the principal's scope.
--
-- client_id/site_id are derived by trigger from the source: ci and ticket
-- with site, asset with client only. Documents and compliance entries span
-- several objects and stay org-wide; retrieval keeps the query-time check
-- against the source table for them. When a source changes client or site,
-- its chunks follow.

ALTER TABLE ai_chunk ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;

CREATE FUNCTION derive_ai_chunk_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    CASE NEW.entity_type
        WHEN 'ci' THEN
            SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id FROM ci c WHERE c.id = NEW.entity_id;
        WHEN 'ticket' THEN
            SELECT t.client_id, t.site_id INTO NEW.client_id, NEW.site_id FROM ticket t WHERE t.id = NEW.entity_id;
        WHEN 'asset' THEN
            SELECT a.client_id INTO NEW.client_id FROM asset a WHERE a.id = NEW.entity_id;
        ELSE
            NULL; -- document, compliance: org-wide
    END CASE;
    RETURN NEW;
END
$$;

CREATE TRIGGER ai_chunk_scope BEFORE INSERT OR UPDATE ON ai_chunk
    FOR EACH ROW EXECUTE FUNCTION derive_ai_chunk_scope();

-- propagate_ai_chunk_scope re-derives the chunks of the changed source.
-- TG_ARGV[0] is the entity type.
CREATE FUNCTION propagate_ai_chunk_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE ai_chunk SET entity_id = entity_id WHERE entity_type = TG_ARGV[0] AND entity_id = NEW.id;
    RETURN NULL;
END
$$;

CREATE TRIGGER ci_ai_chunk_scope_propagation AFTER UPDATE ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_ai_chunk_scope('ci');
CREATE TRIGGER ticket_ai_chunk_scope_propagation AFTER UPDATE ON ticket
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_ai_chunk_scope('ticket');
CREATE TRIGGER asset_ai_chunk_scope_propagation AFTER UPDATE ON asset
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION propagate_ai_chunk_scope('asset');

UPDATE ai_chunk SET entity_id = entity_id;

CREATE INDEX idx_ai_chunk_client ON ai_chunk (client_id) WHERE client_id IS NOT NULL;

DROP POLICY ai_chunk_tenant_isolation ON ai_chunk;
CREATE POLICY ai_chunk_tenant_isolation ON ai_chunk
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
