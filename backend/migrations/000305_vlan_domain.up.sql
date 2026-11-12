-- Migration 000305 (provisional number): VLAN domains (IPM-06, WP-906,
-- E-212 option 1). A VLAN domain is a layer-2 domain that spans sites, as
-- the L2 domains of phpIPAM do: a VLAN of a domain is unique per (domain,
-- vlan_id), VLANs without domain stay unique per site or, without site, per
-- organization (E-24).
--
-- * vlan_domain(organization, name unique per organization, description):
--   read org-wide, written only by principals without client and site
--   restriction (restrictive policies as vrf_scoped_* of 000148, E-09).
-- * vlan_domain_site(domain, site, client): the sites of a domain. The client
--   follows the site (BEFORE trigger, pattern vlan_derive_client); the
--   policies check the client and the site scope (Konvention 5, E-09). The
--   domain cascades, the site is RESTRICT (Konvention 19): a site with a
--   domain assignment is not deleted (E-71, kind vlan_domain_site).
-- * vlan.domain_id NULL, RESTRICT: a domain with VLANs is not deleted. A VLAN
--   of a domain with a site references the assignment (domain, site): the
--   site must belong to the domain, and a site whose VLANs of the domain
--   remain cannot leave it (RESTRICT).
-- * vlan_site_number and vlan_org_number apply to VLANs without domain
--   only; vlan_domain_number makes the number unique per domain.

CREATE TABLE vlan_domain (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    name            text NOT NULL CONSTRAINT vlan_domain_name_check CHECK (btrim(name) <> ''),
    description     text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT vlan_domain_name_key UNIQUE (organization_id, name),
    CONSTRAINT vlan_domain_id_organization_key UNIQUE (id, organization_id)
);

CREATE TRIGGER trg_vlan_domain_updated_at BEFORE UPDATE ON vlan_domain
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON vlan_domain
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE vlan_domain ENABLE ROW LEVEL SECURITY;
ALTER TABLE vlan_domain FORCE ROW LEVEL SECURITY;

CREATE POLICY vlan_domain_isolation ON vlan_domain
    USING (organization_id = current_setting('app.org_id')::uuid)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid);
CREATE POLICY vlan_domain_scoped_insert ON vlan_domain AS RESTRICTIVE FOR INSERT
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL);
CREATE POLICY vlan_domain_scoped_update ON vlan_domain AS RESTRICTIVE FOR UPDATE
    USING (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL);
CREATE POLICY vlan_domain_scoped_delete ON vlan_domain AS RESTRICTIVE FOR DELETE
    USING (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL);

CREATE TABLE vlan_domain_site (
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    domain_id       uuid NOT NULL,
    site_id         uuid NOT NULL,
    client_id       uuid NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT vlan_domain_site_pkey PRIMARY KEY (domain_id, site_id),
    CONSTRAINT vlan_domain_site_organization_key UNIQUE (domain_id, site_id, organization_id),
    CONSTRAINT vlan_domain_site_domain_fkey FOREIGN KEY (domain_id, organization_id)
        REFERENCES vlan_domain (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT vlan_domain_site_site_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id) ON DELETE RESTRICT,
    CONSTRAINT vlan_domain_site_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id) ON DELETE CASCADE
);
CREATE INDEX idx_vlan_domain_site_site ON vlan_domain_site (site_id);
CREATE INDEX idx_vlan_domain_site_client ON vlan_domain_site (client_id, organization_id);

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON vlan_domain_site
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- The client of an assignment is the client of its site.
CREATE FUNCTION vlan_domain_site_derive_client() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    SELECT s.client_id INTO NEW.client_id FROM site s
     WHERE s.id = NEW.site_id AND s.organization_id = NEW.organization_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'site % not found', NEW.site_id
            USING ERRCODE = '23503', CONSTRAINT = 'vlan_domain_site_site_fkey';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_vlan_domain_site_client BEFORE INSERT OR UPDATE OF site_id, client_id ON vlan_domain_site
    FOR EACH ROW EXECUTE FUNCTION vlan_domain_site_derive_client();

ALTER TABLE vlan_domain_site ENABLE ROW LEVEL SECURITY;
ALTER TABLE vlan_domain_site FORCE ROW LEVEL SECURITY;

-- Konvention 5 (E-09): client and site are always set, so a scoped
-- principal reads and writes the assignments of its clients and sites only.
CREATE POLICY vlan_domain_site_isolation ON vlan_domain_site
    USING (organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));

ALTER TABLE vlan ADD COLUMN domain_id uuid;
ALTER TABLE vlan ADD CONSTRAINT vlan_domain_fkey FOREIGN KEY (domain_id, organization_id)
    REFERENCES vlan_domain (id, organization_id) ON DELETE RESTRICT;
ALTER TABLE vlan ADD CONSTRAINT vlan_domain_site_fkey FOREIGN KEY (domain_id, site_id, organization_id)
    REFERENCES vlan_domain_site (domain_id, site_id, organization_id) ON DELETE RESTRICT;

DROP INDEX vlan_site_number;
DROP INDEX vlan_org_number;
CREATE UNIQUE INDEX vlan_site_number ON vlan (organization_id, site_id, vlan_id) WHERE site_id IS NOT NULL AND domain_id IS NULL;
CREATE UNIQUE INDEX vlan_org_number ON vlan (organization_id, vlan_id) WHERE site_id IS NULL AND domain_id IS NULL;
CREATE UNIQUE INDEX vlan_domain_number ON vlan (organization_id, domain_id, vlan_id) WHERE domain_id IS NOT NULL;
CREATE INDEX idx_vlan_domain_site ON vlan (domain_id, site_id) WHERE domain_id IS NOT NULL;
