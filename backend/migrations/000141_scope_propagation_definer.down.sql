-- Reverts WP-345: the derivation and propagation triggers of migration
-- 000061 under the RLS of the writer.

CREATE OR REPLACE FUNCTION derive_edge_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    SELECT c.client_id, c.site_id INTO NEW.source_client_id, NEW.source_site_id FROM ci c WHERE c.id = NEW.source_ci_id;
    SELECT c.client_id, c.site_id INTO NEW.target_client_id, NEW.target_site_id FROM ci c WHERE c.id = NEW.target_ci_id;
    RETURN NEW;
END
$$;

-- A no-op UPDATE re-runs the derivation triggers of the dependent rows.
CREATE OR REPLACE FUNCTION propagate_ci_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
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
    RETURN NULL;
END
$$;

CREATE OR REPLACE FUNCTION propagate_interface_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE ip_address SET interface_id = interface_id WHERE interface_id = NEW.id;
    RETURN NULL;
END
$$;

CREATE OR REPLACE FUNCTION propagate_subnet_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE ip_address SET subnet_id = subnet_id WHERE subnet_id = NEW.id AND interface_id IS NULL;
    RETURN NULL;
END
$$;
