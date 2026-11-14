-- Migration 000321: devices and IP addresses (IPM-13, WP-911, E-215 option 1).
--
-- * ip_address.ci_id binds an address to a device, also without interface
--   (VIP, anycast, a management address without known interface), with a
--   composite foreign key (Konvention 19) that clears only ci_id when the
--   CI is deleted; ip_address.role is primary, secondary, vip, anycast,
--   loopback or management. Backfill: the CI of the interface.
-- * ip_address_ci_bind (BEFORE INSERT/UPDATE OF interface_id, ci_id, before
--   ip_address_scope in name order): with an interface ci_id is the CI of
--   the interface. An empty ci_id is filled, a different one is refused
--   (ip_address_ci_matches_interface, 422 ci_id at the API), a new
--   interface (or an interface that moved to another CI) takes its CI
--   along. The interface is read with the scope widened, so its CI is
--   found whatever the writer sees.
-- * derive_ip_scope derives client and site from the interface, else from
--   the CI (ci_id), else from the subnet; propagate_ci_scope (version of
--   000218) also re-derives the addresses without interface bound to the
--   CI.
-- * network_interface_ci_rebind: an interface that moves to another CI (CI
--   merge, unmerge, a direct update) takes its addresses along, also when
--   client and site stay (network_interface_scope_propagation of 000141
--   fires only then): touching interface_id lets ip_address_ci_bind set the
--   CI of the interface.

ALTER TABLE ip_address
    ADD COLUMN ci_id uuid NULL,
    ADD COLUMN role text NOT NULL DEFAULT 'primary'
        CONSTRAINT ip_address_role_check
        CHECK (role IN ('primary', 'secondary', 'vip', 'anycast', 'loopback', 'management'));
ALTER TABLE ip_address ADD CONSTRAINT ip_address_ci_fkey FOREIGN KEY (ci_id, organization_id)
    REFERENCES ci (id, organization_id) ON DELETE SET NULL (ci_id);
CREATE INDEX idx_ip_address_org_ci ON ip_address (organization_id, ci_id) WHERE ci_id IS NOT NULL;

-- Backfill without history rows and without touching updated_at.
ALTER TABLE ip_address DISABLE TRIGGER USER;
UPDATE ip_address a SET ci_id = n.ci_id
  FROM network_interface n
 WHERE n.id = a.interface_id AND n.organization_id = a.organization_id;
ALTER TABLE ip_address ENABLE TRIGGER USER;

CREATE FUNCTION ip_address_ci_bind() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    nic_ci uuid;
BEGIN
    IF NEW.interface_id IS NULL THEN
        RETURN NEW;
    END IF;
    -- The deletion of the CI clears ci_id (foreign key action, a nested
    -- trigger): the address does not take the CI back from its interface.
    IF TG_OP = 'UPDATE' AND pg_trigger_depth() > 1 AND NEW.ci_id IS NULL AND OLD.ci_id IS NOT NULL THEN
        RETURN NEW;
    END IF;
    previous := app_scope_widen();
    SELECT n.ci_id INTO nic_ci FROM network_interface n
     WHERE n.id = NEW.interface_id AND n.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    IF nic_ci IS NULL THEN
        RETURN NEW; -- unknown interface: its foreign key refuses the row
    END IF;
    IF NEW.ci_id IS NULL THEN
        NEW.ci_id := nic_ci;
    ELSIF NEW.ci_id <> nic_ci THEN
        IF TG_OP = 'UPDATE' AND NEW.ci_id IS NOT DISTINCT FROM OLD.ci_id THEN
            -- Another interface, or the interface moved: its CI.
            NEW.ci_id := nic_ci;
        ELSE
            RAISE EXCEPTION 'the CI of the address differs from the CI of its interface'
                USING ERRCODE = '23514', CONSTRAINT = 'ip_address_ci_matches_interface';
        END IF;
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER ip_address_ci_bind BEFORE INSERT OR UPDATE OF interface_id, ci_id ON ip_address
    FOR EACH ROW EXECUTE FUNCTION ip_address_ci_bind();

CREATE OR REPLACE FUNCTION derive_ip_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    IF NEW.interface_id IS NOT NULL THEN
        SELECT n.client_id, n.site_id INTO NEW.client_id, NEW.site_id FROM network_interface n WHERE n.id = NEW.interface_id;
    ELSIF NEW.ci_id IS NOT NULL THEN
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id FROM ci c WHERE c.id = NEW.ci_id;
    ELSIF NEW.subnet_id IS NOT NULL THEN
        SELECT s.client_id, s.site_id INTO NEW.client_id, NEW.site_id FROM subnet s WHERE s.id = NEW.subnet_id;
    END IF;
    RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION propagate_ci_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE network_interface SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_contact SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_change SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_field_value SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_instance_field_definition SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE rack_mount SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE compliance_result SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE security_finding SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE discovery_result SET matched_ci_id = matched_ci_id WHERE matched_ci_id = NEW.id;
    UPDATE ci_relationship SET source_ci_id = source_ci_id WHERE source_ci_id = NEW.id OR target_ci_id = NEW.id;
    UPDATE relationship_suppression SET source_ci_id = source_ci_id WHERE source_ci_id = NEW.id OR target_ci_id = NEW.id;
    UPDATE ci_snapshot SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_tag SET ci_id = ci_id WHERE ci_id = NEW.id;
    -- Addresses bound to the CI without interface (WP-911); those with an
    -- interface follow it (000141).
    UPDATE ip_address SET ci_id = ci_id
     WHERE organization_id = NEW.organization_id AND ci_id = NEW.id AND interface_id IS NULL;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE FUNCTION propagate_interface_ci() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE ip_address SET interface_id = interface_id
     WHERE organization_id = NEW.organization_id AND interface_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER network_interface_ci_rebind AFTER UPDATE OF ci_id ON network_interface
    FOR EACH ROW WHEN (OLD.ci_id IS DISTINCT FROM NEW.ci_id)
    EXECUTE FUNCTION propagate_interface_ci();
