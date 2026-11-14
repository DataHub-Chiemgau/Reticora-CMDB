-- Down of 000321: removes the device binding of IP addresses (WP-911).

DROP TRIGGER network_interface_ci_rebind ON network_interface;
DROP FUNCTION propagate_interface_ci();

-- propagate_ci_scope as of 000218.
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
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

-- derive_ip_scope as of 000061.
CREATE OR REPLACE FUNCTION derive_ip_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    IF NEW.interface_id IS NOT NULL THEN
        SELECT n.client_id, n.site_id INTO NEW.client_id, NEW.site_id FROM network_interface n WHERE n.id = NEW.interface_id;
    ELSIF NEW.subnet_id IS NOT NULL THEN
        SELECT s.client_id, s.site_id INTO NEW.client_id, NEW.site_id FROM subnet s WHERE s.id = NEW.subnet_id;
    END IF;
    RETURN NEW;
END
$$;

DROP TRIGGER ip_address_ci_bind ON ip_address;
DROP FUNCTION ip_address_ci_bind();
DROP INDEX idx_ip_address_org_ci;
ALTER TABLE ip_address DROP CONSTRAINT ip_address_ci_fkey;
ALTER TABLE ip_address DROP COLUMN role, DROP COLUMN ci_id;
