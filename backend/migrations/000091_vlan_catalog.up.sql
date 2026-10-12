-- WP-081 (NET-08, NET-01, NET-02, DB-05, E-24): VLAN catalog.
--
-- * vlan(organization, site?, vlan_id 1–4094, name, description); the number
--   is unique per site, or per organization for VLANs without site — two
--   partial indexes, no sentinel (E-24). client_id follows the site and
--   carries the client scope of RLS (TEN-05).
-- * subnet.vlan_id and network_interface.vlan_id reference vlan. Existing
--   integer VLAN numbers of subnets become catalog entries ("VLAN <n>") of
--   the subnet's site; numbers outside 1–4094 go to migration_quarantine.

CREATE TABLE vlan (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       uuid REFERENCES client (id) ON DELETE CASCADE,
    site_id         uuid REFERENCES site (id) ON DELETE CASCADE,
    vlan_id         integer NOT NULL CHECK (vlan_id BETWEEN 1 AND 4094),
    name            text NOT NULL CHECK (btrim(name) <> ''),
    description     text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, organization_id)
);
CREATE UNIQUE INDEX vlan_site_number ON vlan (organization_id, site_id, vlan_id) WHERE site_id IS NOT NULL;
CREATE UNIQUE INDEX vlan_org_number ON vlan (organization_id, vlan_id) WHERE site_id IS NULL;
CREATE INDEX idx_vlan_site ON vlan (site_id) WHERE site_id IS NOT NULL;

-- The client of a VLAN is the client of its site.
CREATE FUNCTION vlan_derive_client() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.site_id IS NULL THEN
        NEW.client_id := NULL;
    ELSE
        SELECT s.client_id INTO NEW.client_id FROM site s WHERE s.id = NEW.site_id;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'site % not found', NEW.site_id
                USING ERRCODE = '23503', CONSTRAINT = 'vlan_site_id_fkey';
        END IF;
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_vlan_client BEFORE INSERT OR UPDATE OF site_id, client_id ON vlan
    FOR EACH ROW EXECUTE FUNCTION vlan_derive_client();
CREATE TRIGGER trg_vlan_updated_at BEFORE UPDATE ON vlan
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE vlan ENABLE ROW LEVEL SECURITY;
ALTER TABLE vlan FORCE ROW LEVEL SECURITY;
CREATE POLICY vlan_isolation ON vlan
    USING (organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));

-- A scoped principal neither changes nor deletes org-wide VLANs (TEN-05,
-- migration 000079).
CREATE POLICY vlan_scoped_update ON vlan AS RESTRICTIVE FOR UPDATE
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
CREATE POLICY vlan_scoped_delete ON vlan AS RESTRICTIVE FOR DELETE
    USING (organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));

-- Subnets: integer VLAN numbers become catalog references.
INSERT INTO vlan (organization_id, site_id, vlan_id, name)
SELECT DISTINCT organization_id, site_id, vlan_id, 'VLAN ' || vlan_id
FROM subnet
WHERE vlan_id BETWEEN 1 AND 4094;

ALTER TABLE subnet RENAME COLUMN vlan_id TO vlan_number;
ALTER TABLE subnet ADD COLUMN vlan_id uuid;
UPDATE subnet s SET vlan_id = v.id
FROM vlan v
WHERE v.organization_id = s.organization_id AND v.vlan_id = s.vlan_number AND v.site_id IS NOT DISTINCT FROM s.site_id;

INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT organization_id, 'subnet', jsonb_build_object('id', id, 'vlan_number', vlan_number),
       'VLAN number outside 1–4094 (NET-08)', '000091'
FROM subnet
WHERE vlan_number IS NOT NULL AND vlan_id IS NULL;

ALTER TABLE subnet DROP COLUMN vlan_number;
ALTER TABLE subnet ADD CONSTRAINT subnet_vlan_fkey FOREIGN KEY (vlan_id, organization_id)
    REFERENCES vlan (id, organization_id) ON DELETE SET NULL (vlan_id);
CREATE INDEX idx_subnet_vlan ON subnet (vlan_id) WHERE vlan_id IS NOT NULL;

ALTER TABLE network_interface ADD COLUMN vlan_id uuid;
ALTER TABLE network_interface ADD CONSTRAINT network_interface_vlan_fkey FOREIGN KEY (vlan_id, organization_id)
    REFERENCES vlan (id, organization_id) ON DELETE SET NULL (vlan_id);
CREATE INDEX idx_network_interface_vlan ON network_interface (vlan_id) WHERE vlan_id IS NOT NULL;
