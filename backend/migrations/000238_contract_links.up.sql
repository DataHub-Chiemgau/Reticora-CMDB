-- WP-737 (MGT-12, DB-05, MET-12): contracts as targets of document links.
--
-- 1. doc_link_entity_type_check admits contract next to the values of
--    000217 (ci, asset, assignment, ticket, location, disposal_record,
--    stocktake).
-- 2. derive_document_link_scope (000217) derives the client of a link to a
--    contract from the contract (client_id NULL: org-wide); contract has no
--    site and no team.
-- 3. propagate_contract_scope re-derives the links of a contract whose
--    client changes (trigger contract_scope_propagation).
-- 4. delete_contract_document_links removes the links of a deleted contract
--    in the same transaction (trigger contract_document_links); the
--    documents stay, document.link_count follows via count_document_links.
-- Both functions run with the scope widened (app_scope_widen, 000217) and a
-- fixed search_path, like propagate_document_link_scope.

-- ─── 1. Link targets ───────────────────────────────────────────────────────────

ALTER TABLE document_link DROP CONSTRAINT doc_link_entity_type_check;
ALTER TABLE document_link ADD CONSTRAINT doc_link_entity_type_check CHECK (entity_type IN (
    'ci', 'asset', 'assignment', 'ticket', 'location', 'disposal_record', 'stocktake', 'contract'
));

-- ─── 2. Derivation ─────────────────────────────────────────────────────────────

-- derive_document_link_scope runs under the RLS of the writer: a target
-- outside its scope derives NULL and is then rejected by WITH CHECK for
-- scoped principals (E-09). stocktake links stay org-wide.
CREATE OR REPLACE FUNCTION derive_document_link_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    NEW.team_id := NULL;
    CASE NEW.entity_type
        WHEN 'ci' THEN
            SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
              FROM ci c WHERE c.id = NEW.entity_id AND c.organization_id = NEW.organization_id;
        WHEN 'asset' THEN
            SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id
              FROM asset a WHERE a.id = NEW.entity_id AND a.organization_id = NEW.organization_id;
        WHEN 'ticket' THEN
            SELECT t.client_id, t.site_id, t.team_id INTO NEW.client_id, NEW.site_id, NEW.team_id
              FROM ticket t WHERE t.id = NEW.entity_id AND t.organization_id = NEW.organization_id;
        WHEN 'assignment' THEN
            SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id
              FROM assignment s JOIN asset a ON a.id = s.asset_id
             WHERE s.id = NEW.entity_id AND s.organization_id = NEW.organization_id;
        WHEN 'location' THEN
            SELECT l.client_id, l.site_id INTO NEW.client_id, NEW.site_id
              FROM location l WHERE l.id = NEW.entity_id AND l.organization_id = NEW.organization_id;
        WHEN 'disposal_record' THEN
            SELECT d.client_id, d.site_id INTO NEW.client_id, NEW.site_id
              FROM disposal_record d WHERE d.id = NEW.entity_id AND d.organization_id = NEW.organization_id;
        WHEN 'contract' THEN
            SELECT c.client_id INTO NEW.client_id
              FROM contract c WHERE c.id = NEW.entity_id AND c.organization_id = NEW.organization_id;
        ELSE
            NULL; -- stocktake: org-wide
    END CASE;
    RETURN NEW;
END
$$;

-- ─── 3. Propagation ────────────────────────────────────────────────────────────

-- A no-op UPDATE re-runs the derivation trigger of the links.
CREATE FUNCTION propagate_contract_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE document_link SET entity_id = entity_id WHERE entity_type = 'contract' AND entity_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER contract_scope_propagation AFTER UPDATE ON contract
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION propagate_contract_scope();

-- ─── 4. Deletion ───────────────────────────────────────────────────────────────

-- The links of a deleted contract go with it; the documents stay.
CREATE FUNCTION delete_contract_document_links() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    DELETE FROM document_link
     WHERE entity_type = 'contract' AND entity_id = OLD.id AND organization_id = OLD.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER contract_document_links AFTER DELETE ON contract
    FOR EACH ROW EXECUTE FUNCTION delete_contract_document_links();
