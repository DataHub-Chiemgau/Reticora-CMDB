-- Migration 000298: IPAM sections (IPM-01, WP-900, E-209). A section groups
-- the subnets of an organization with settings of its own (strict_mode,
-- show_vlan, show_vrf), as the sections of phpIPAM do; the rights per
-- section follow with WP-910, the overlap check of strict_mode with WP-901.
--
-- * ipam_section(organization, client NULL, name unique per organization,
--   description, strict_mode, show_vlan, show_vrf, is_default); exactly one
--   org-wide default section "default" per organization, created with the
--   organization (trigger) and by backfill (pattern of the default VRF,
--   000092). The default section cannot be deleted nor lose its flag
--   (ipam_section_default_required); the organization cascade removes it.
-- * subnet.section_id NOT NULL: every existing subnet moves into the default
--   section; a subnet written without section takes the default section
--   (BEFORE trigger). A section with subnets cannot be deleted (RESTRICT).
-- * Row level security after Konvention 5 (E-09): a principal restricted to
--   clients reads the sections of its clients and the org-wide ones and
--   writes only sections of its clients; a principal restricted to sites
--   writes no section.

CREATE TABLE ipam_section (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       uuid NULL,
    name            text NOT NULL CONSTRAINT ipam_section_name_check CHECK (btrim(name) <> ''),
    description     text,
    strict_mode     boolean NOT NULL DEFAULT false,
    show_vlan       boolean NOT NULL DEFAULT true,
    show_vrf        boolean NOT NULL DEFAULT true,
    is_default      boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ipam_section_name_key UNIQUE (organization_id, name),
    CONSTRAINT ipam_section_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT ipam_section_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id),
    -- The default section belongs to the whole organization.
    CONSTRAINT ipam_section_default_org_wide CHECK (NOT is_default OR client_id IS NULL)
);
CREATE UNIQUE INDEX ipam_section_one_default ON ipam_section (organization_id) WHERE is_default;
CREATE INDEX idx_ipam_section_client ON ipam_section (client_id, organization_id) WHERE client_id IS NOT NULL;

CREATE TRIGGER trg_ipam_section_updated_at BEFORE UPDATE ON ipam_section
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON ipam_section
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- The default section cannot be deleted or lose its flag; deleting the
-- organization (cascade, trigger depth > 1) removes it.
CREATE FUNCTION ipam_section_protect_default() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF OLD.is_default AND pg_trigger_depth() = 1 AND (TG_OP = 'DELETE' OR NOT NEW.is_default) THEN
        RAISE EXCEPTION 'the default IPAM section of an organization cannot be removed'
            USING ERRCODE = '23514', CONSTRAINT = 'ipam_section_default_required';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_ipam_section_protect_default BEFORE UPDATE OF is_default OR DELETE ON ipam_section
    FOR EACH ROW EXECUTE FUNCTION ipam_section_protect_default();

-- Every new organization gets its default section "default". The insert
-- runs in the new organization's tenant context, the caller's is restored.
CREATE FUNCTION organization_default_section() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text := current_setting('app.org_id', true);
BEGIN
    PERFORM set_config('app.org_id', NEW.id::text, true);
    INSERT INTO ipam_section (organization_id, name, description, is_default)
    VALUES (NEW.id, 'default', 'Default section', true);
    PERFORM set_config('app.org_id', COALESCE(previous, ''), true);
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_organization_default_section AFTER INSERT ON organization
    FOR EACH ROW EXECUTE FUNCTION organization_default_section();

INSERT INTO ipam_section (organization_id, name, description, is_default)
SELECT id, 'default', 'Default section', true FROM organization;

ALTER TABLE ipam_section ENABLE ROW LEVEL SECURITY;
ALTER TABLE ipam_section FORCE ROW LEVEL SECURITY;

-- Konvention 5 (E-09): a principal restricted to clients reads the sections
-- of its clients and the org-wide ones; it creates, changes and deletes only
-- sections of its clients. A section has no site, so a principal restricted
-- to sites writes none (pattern vlan_scoped_update/vlan_scoped_delete of
-- 000091 with site_id always NULL); org-wide sections are written only by
-- principals without client and site restriction.
DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
BEGIN
    EXECUTE format('CREATE POLICY ipam_section_isolation ON ipam_section USING (%s AND (%s OR client_id IS NULL OR %s)) WITH CHECK (%s AND (%s OR %s) AND %s)',
        org_match, scope_all, scope_match, org_match, scope_all, scope_match, site_all);
    EXECUTE format('CREATE POLICY ipam_section_scoped_update ON ipam_section AS RESTRICTIVE FOR UPDATE USING (%s AND (%s OR %s) AND %s) WITH CHECK (%s AND (%s OR %s) AND %s)',
        org_match, scope_all, scope_match, site_all, org_match, scope_all, scope_match, site_all);
    EXECUTE format('CREATE POLICY ipam_section_scoped_delete ON ipam_section AS RESTRICTIVE FOR DELETE USING (%s AND (%s OR %s) AND %s)',
        org_match, scope_all, scope_match, site_all);
END
$$;

-- Subnets belong to a section; without one they take the default section.
CREATE FUNCTION default_section_of(p_org uuid) RETURNS uuid
LANGUAGE sql STABLE
SET search_path = pg_catalog, public, pg_temp
AS $$
    SELECT id FROM ipam_section WHERE organization_id = p_org AND is_default
$$;

ALTER TABLE subnet ADD COLUMN section_id uuid;
ALTER TABLE subnet DISABLE TRIGGER trg_subnet_updated_at;
UPDATE subnet s SET section_id = sec.id FROM ipam_section sec
 WHERE sec.organization_id = s.organization_id AND sec.is_default;
ALTER TABLE subnet ENABLE TRIGGER trg_subnet_updated_at;
ALTER TABLE subnet ALTER COLUMN section_id SET NOT NULL;
ALTER TABLE subnet ADD CONSTRAINT subnet_section_fkey FOREIGN KEY (section_id, organization_id)
    REFERENCES ipam_section (id, organization_id) ON DELETE RESTRICT;
CREATE INDEX idx_subnet_org_section ON subnet (organization_id, section_id);

CREATE FUNCTION subnet_default_section() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF NEW.section_id IS NULL THEN
        NEW.section_id := default_section_of(NEW.organization_id);
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_subnet_default_section BEFORE INSERT OR UPDATE OF section_id ON subnet
    FOR EACH ROW EXECUTE FUNCTION subnet_default_section();
