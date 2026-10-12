-- WP-082 (TEN-10, CH10, NET-02, NET-03): VRFs at organization level.
--
-- * vrf(organization, name unique per organization, description,
--   is_default); exactly one default VRF "global" per organization, created
--   with the organization (trigger) and by backfill.
-- * subnet.vrf_id NOT NULL (default: the organization's default VRF);
--   subnets are unique per (organization, vrf, cidr) instead of
--   (organization, cidr).
-- * ip_address.vrf_id follows the subnet (default VRF without subnet); the
--   address indexes contain vrf_id: unique (organization, vrf, address,
--   interface).

CREATE TABLE vrf (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    name            text NOT NULL CHECK (btrim(name) <> ''),
    description     text,
    is_default      boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name),
    UNIQUE (id, organization_id)
);
CREATE UNIQUE INDEX vrf_one_default ON vrf (organization_id) WHERE is_default;
CREATE TRIGGER trg_vrf_updated_at BEFORE UPDATE ON vrf
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE vrf ENABLE ROW LEVEL SECURITY;
ALTER TABLE vrf FORCE ROW LEVEL SECURITY;
CREATE POLICY vrf_isolation ON vrf
    USING (organization_id = current_setting('app.org_id')::uuid)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid);

-- The default VRF cannot be deleted or lose its flag; deleting the
-- organization (cascade, trigger depth > 1) removes it.
CREATE FUNCTION vrf_protect_default() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.is_default AND pg_trigger_depth() = 1 AND (TG_OP = 'DELETE' OR NOT NEW.is_default) THEN
        RAISE EXCEPTION 'the default VRF of organization % cannot be removed', OLD.organization_id
            USING ERRCODE = '23514', CONSTRAINT = 'vrf_default_required';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_vrf_protect_default BEFORE UPDATE OF is_default OR DELETE ON vrf
    FOR EACH ROW EXECUTE FUNCTION vrf_protect_default();

-- Every new organization gets its default VRF "global" (CH10). The insert
-- runs in the new organization's tenant context, the caller's is restored.
CREATE FUNCTION organization_default_vrf() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    previous text := current_setting('app.org_id', true);
BEGIN
    PERFORM set_config('app.org_id', NEW.id::text, true);
    INSERT INTO vrf (organization_id, name, description, is_default)
    VALUES (NEW.id, 'global', 'Default VRF', true);
    PERFORM set_config('app.org_id', COALESCE(previous, ''), true);
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_organization_default_vrf AFTER INSERT ON organization
    FOR EACH ROW EXECUTE FUNCTION organization_default_vrf();

INSERT INTO vrf (organization_id, name, description, is_default)
SELECT id, 'global', 'Default VRF', true FROM organization;

-- Subnets belong to a VRF; without one they take the default VRF.
ALTER TABLE subnet ADD COLUMN vrf_id uuid;
UPDATE subnet s SET vrf_id = v.id FROM vrf v WHERE v.organization_id = s.organization_id AND v.is_default;
ALTER TABLE subnet ALTER COLUMN vrf_id SET NOT NULL;
ALTER TABLE subnet ADD CONSTRAINT subnet_vrf_fkey FOREIGN KEY (vrf_id, organization_id)
    REFERENCES vrf (id, organization_id);
ALTER TABLE subnet DROP CONSTRAINT subnet_organization_id_cidr_key;
ALTER TABLE subnet ADD CONSTRAINT subnet_org_vrf_cidr_key UNIQUE (organization_id, vrf_id, cidr);

CREATE FUNCTION default_vrf_of(p_org uuid) RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT id FROM vrf WHERE organization_id = p_org AND is_default
$$;

CREATE FUNCTION subnet_default_vrf() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.vrf_id IS NULL THEN
        NEW.vrf_id := default_vrf_of(NEW.organization_id);
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_subnet_default_vrf BEFORE INSERT OR UPDATE OF vrf_id ON subnet
    FOR EACH ROW EXECUTE FUNCTION subnet_default_vrf();

-- IP addresses carry the VRF of their subnet (NET-03).
ALTER TABLE ip_address ADD COLUMN vrf_id uuid;
UPDATE ip_address a SET vrf_id = COALESCE(
    (SELECT s.vrf_id FROM subnet s WHERE s.id = a.subnet_id),
    (SELECT v.id FROM vrf v WHERE v.organization_id = a.organization_id AND v.is_default));
ALTER TABLE ip_address ALTER COLUMN vrf_id SET NOT NULL;
ALTER TABLE ip_address ADD CONSTRAINT ip_address_vrf_fkey FOREIGN KEY (vrf_id, organization_id)
    REFERENCES vrf (id, organization_id);

CREATE FUNCTION ip_address_vrf() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.vrf_id := COALESCE((SELECT s.vrf_id FROM subnet s WHERE s.id = NEW.subnet_id),
                           NEW.vrf_id, default_vrf_of(NEW.organization_id));
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_ip_address_vrf BEFORE INSERT OR UPDATE OF subnet_id, vrf_id ON ip_address
    FOR EACH ROW EXECUTE FUNCTION ip_address_vrf();

-- A subnet moving to another VRF takes its addresses along.
CREATE FUNCTION subnet_propagate_vrf() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE ip_address SET vrf_id = NEW.vrf_id WHERE subnet_id = NEW.id AND vrf_id <> NEW.vrf_id;
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_subnet_propagate_vrf AFTER UPDATE OF vrf_id ON subnet
    FOR EACH ROW WHEN (OLD.vrf_id IS DISTINCT FROM NEW.vrf_id)
    EXECUTE FUNCTION subnet_propagate_vrf();

DROP INDEX uq_ip_address_org_address_no_interface;
DROP INDEX uq_ip_address_org_interface_address;
CREATE UNIQUE INDEX uq_ip_address_org_vrf_address_no_interface
    ON ip_address (organization_id, vrf_id, address) WHERE interface_id IS NULL;
CREATE UNIQUE INDEX uq_ip_address_org_vrf_interface_address
    ON ip_address (organization_id, vrf_id, interface_id, address) WHERE interface_id IS NOT NULL;
