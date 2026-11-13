-- Down of WP-1071: removes the NAT translations, restores
-- propagate_ci_scope as of 000321 and drops the keys (id, organization_id)
-- of ip_address and subnet while no foreign key refers to them.

DROP TABLE nat_translation;
DROP FUNCTION derive_nat_translation_scope();

-- propagate_ci_scope as of 000321.
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

DO $$
DECLARE
    k record;
BEGIN
    FOR k IN SELECT c.conrelid, c.conname, c.conindid FROM pg_constraint c
              WHERE (c.conrelid, c.conname) IN (('ip_address'::regclass, 'ip_address_id_organization_key'),
                                                ('subnet'::regclass, 'subnet_id_organization_key')) LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_constraint f WHERE f.contype = 'f' AND f.conindid = k.conindid) THEN
            EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', k.conrelid::regclass, k.conname);
        END IF;
    END LOOP;
END
$$;
