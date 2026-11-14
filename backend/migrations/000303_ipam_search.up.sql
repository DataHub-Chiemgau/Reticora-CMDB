-- WP-909 (IPM-20, SR-04): subnets and IP addresses as hits of the global
-- search and the index of the state filter of the address list.
--
-- 1. search_document admits the entity types subnet and ip_address.
-- 2. search_index_subnet() and search_index_ip_address() keep their
--    documents current on every write, whatever path changes the row
--    (pattern 000132): INSERT, UPDATE of an indexed column or of client_id
--    and site_id (the upsert re-derives the scope of the document through
--    search_document_scope), DELETE. The UPDATE triggers have no column
--    list and decide by their WHEN condition on the values after the
--    BEFORE triggers: client_id and site_id of an address are derived
--    (derive_ip_scope, 000061) when a write names only interface_id, as
--    PATCH /ips and the propagation of an interface's scope (000141) do.
--    A subnet that changes its section carries the section_id into the
--    metadata of its addresses' documents. Title: CIDR and name of a subnet, the
--    host of an address; summary: description, DNS name and description;
--    metadata: vrf_id and section_id, for an address also subnet_id (the
--    section of its subnet; WP-910). The documents mirror rows that passed
--    their own policies, so they are written with the scope widened
--    (app_scope_widen). Existing rows are indexed once (ON CONFLICT DO
--    NOTHING).
-- 3. derive_search_document_scope (version of 000264) takes client and site
--    of the subnet or the address.
-- 4. idx_ip_address_org_state is a B-tree on columns (Konvention 22): the
--    state filter of the address list under row level security.

-- ─── 1. Entity types ─────────────────────────────────────────────────────────

ALTER TABLE search_document DROP CONSTRAINT search_document_entity_type_check;
ALTER TABLE search_document ADD CONSTRAINT search_document_entity_type_check
    CHECK (entity_type = ANY (ARRAY['ci', 'asset', 'document', 'ticket', 'contact', 'compliance', 'location', 'reservation',
                                    'subnet', 'ip_address']));

-- ─── 3. Scope of the documents ───────────────────────────────────────────────

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
        WHEN 'subnet' THEN
            SELECT s.client_id, s.site_id INTO NEW.client_id, NEW.site_id FROM subnet s WHERE s.id = NEW.entity_id;
        WHEN 'ip_address' THEN
            SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id FROM ip_address a WHERE a.id = NEW.entity_id;
        ELSE
            NULL; -- document, compliance: org-wide
    END CASE;
    RETURN NEW;
END
$$;

-- ─── 2. Live index ───────────────────────────────────────────────────────────

CREATE FUNCTION search_index_subnet() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM search_document
         WHERE organization_id = OLD.organization_id AND entity_type = 'subnet' AND entity_id = OLD.id;
        PERFORM app_scope_restore(previous);
        RETURN OLD;
    END IF;
    INSERT INTO search_document (organization_id, entity_type, entity_id, title, summary, url, metadata)
    VALUES (NEW.organization_id, 'subnet', NEW.id, concat_ws(' ', NEW.cidr::text, NEW.name), COALESCE(NEW.description, ''),
            '/ipam/subnets/' || NEW.id, jsonb_build_object('vrf_id', NEW.vrf_id, 'section_id', NEW.section_id))
    ON CONFLICT (organization_id, entity_type, entity_id) DO UPDATE
        SET title = EXCLUDED.title, summary = EXCLUDED.summary, url = EXCLUDED.url, metadata = EXCLUDED.metadata,
            updated_at = now();
    -- The section of the addresses' documents is the one of their subnet.
    IF TG_OP = 'UPDATE' AND OLD.section_id IS DISTINCT FROM NEW.section_id THEN
        UPDATE search_document d
           SET metadata = jsonb_set(d.metadata, '{section_id}', to_jsonb(NEW.section_id)), updated_at = now()
          FROM ip_address a
         WHERE a.organization_id = NEW.organization_id AND a.subnet_id = NEW.id
           AND d.organization_id = NEW.organization_id AND d.entity_type = 'ip_address' AND d.entity_id = a.id;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NEW;
END
$$;

CREATE TRIGGER subnet_search_index_insert AFTER INSERT ON subnet
    FOR EACH ROW EXECUTE FUNCTION search_index_subnet();
CREATE TRIGGER subnet_search_index_update AFTER UPDATE ON subnet
    FOR EACH ROW WHEN (OLD.cidr IS DISTINCT FROM NEW.cidr OR OLD.name IS DISTINCT FROM NEW.name
        OR OLD.description IS DISTINCT FROM NEW.description OR OLD.vrf_id IS DISTINCT FROM NEW.vrf_id
        OR OLD.section_id IS DISTINCT FROM NEW.section_id OR OLD.client_id IS DISTINCT FROM NEW.client_id
        OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION search_index_subnet();
CREATE TRIGGER subnet_search_index_delete AFTER DELETE ON subnet
    FOR EACH ROW EXECUTE FUNCTION search_index_subnet();

CREATE FUNCTION search_index_ip_address() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM search_document
         WHERE organization_id = OLD.organization_id AND entity_type = 'ip_address' AND entity_id = OLD.id;
        PERFORM app_scope_restore(previous);
        RETURN OLD;
    END IF;
    INSERT INTO search_document (organization_id, entity_type, entity_id, title, summary, url, metadata)
    VALUES (NEW.organization_id, 'ip_address', NEW.id, host(NEW.address), concat_ws(' ', NEW.dns_name, NEW.description),
            CASE WHEN NEW.subnet_id IS NULL THEN '/ipam?address=' || NEW.id
                 ELSE '/ipam/subnets/' || NEW.subnet_id || '?address=' || NEW.id END,
            jsonb_build_object('vrf_id', NEW.vrf_id, 'subnet_id', NEW.subnet_id,
                               'section_id', (SELECT s.section_id FROM subnet s WHERE s.id = NEW.subnet_id)))
    ON CONFLICT (organization_id, entity_type, entity_id) DO UPDATE
        SET title = EXCLUDED.title, summary = EXCLUDED.summary, url = EXCLUDED.url, metadata = EXCLUDED.metadata,
            updated_at = now();
    PERFORM app_scope_restore(previous);
    RETURN NEW;
END
$$;

CREATE TRIGGER ip_address_search_index_insert AFTER INSERT ON ip_address
    FOR EACH ROW EXECUTE FUNCTION search_index_ip_address();
CREATE TRIGGER ip_address_search_index_update AFTER UPDATE ON ip_address
    FOR EACH ROW WHEN (OLD.address IS DISTINCT FROM NEW.address OR OLD.dns_name IS DISTINCT FROM NEW.dns_name
        OR OLD.description IS DISTINCT FROM NEW.description OR OLD.vrf_id IS DISTINCT FROM NEW.vrf_id
        OR OLD.subnet_id IS DISTINCT FROM NEW.subnet_id OR OLD.client_id IS DISTINCT FROM NEW.client_id
        OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION search_index_ip_address();
CREATE TRIGGER ip_address_search_index_delete AFTER DELETE ON ip_address
    FOR EACH ROW EXECUTE FUNCTION search_index_ip_address();

-- Existing subnets and addresses are indexed once.
INSERT INTO search_document (organization_id, entity_type, entity_id, title, summary, url, metadata)
SELECT organization_id, 'subnet', id, concat_ws(' ', cidr::text, name), COALESCE(description, ''), '/ipam/subnets/' || id,
       jsonb_build_object('vrf_id', vrf_id, 'section_id', section_id)
  FROM subnet
ON CONFLICT (organization_id, entity_type, entity_id) DO NOTHING;
INSERT INTO search_document (organization_id, entity_type, entity_id, title, summary, url, metadata)
SELECT a.organization_id, 'ip_address', a.id, host(a.address), concat_ws(' ', a.dns_name, a.description),
       CASE WHEN a.subnet_id IS NULL THEN '/ipam?address=' || a.id ELSE '/ipam/subnets/' || a.subnet_id || '?address=' || a.id END,
       jsonb_build_object('vrf_id', a.vrf_id, 'subnet_id', a.subnet_id, 'section_id', s.section_id)
  FROM ip_address a LEFT JOIN subnet s ON s.id = a.subnet_id
ON CONFLICT (organization_id, entity_type, entity_id) DO NOTHING;

-- ─── 4. State filter ─────────────────────────────────────────────────────────

CREATE INDEX IF NOT EXISTS idx_ip_address_org_state ON ip_address (organization_id, state);
