-- Reverts 000307: the binding targets device_group and probe go first (their
-- rows, columns, indexes and the extended CHECK; the functions of 000292
-- come back), then the propagation, the tables, triggers and functions of
-- probe, monitoring_group, monitoring_group_member and monitoring_profile.

DELETE FROM credential_binding WHERE target_kind IN ('device_group', 'probe');

DROP TRIGGER trg_probe_credential_binding_scope ON probe;
DROP TRIGGER trg_monitoring_group_credential_binding_scope ON monitoring_group;

CREATE OR REPLACE FUNCTION propagate_credential_binding_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    IF TG_TABLE_NAME = 'ci' THEN
        UPDATE credential_binding SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
    ELSE
        UPDATE credential_binding SET subnet_id = subnet_id WHERE subnet_id = NEW.id AND organization_id = NEW.organization_id;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE OR REPLACE FUNCTION derive_credential_binding_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
    credential_client uuid;
BEGIN
    IF NEW.target_kind = 'ci' THEN
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
          FROM ci c WHERE c.id = NEW.ci_id AND c.organization_id = NEW.organization_id;
    ELSIF NEW.target_kind = 'subnet' THEN
        SELECT s.client_id, s.site_id INTO NEW.client_id, NEW.site_id
          FROM subnet s WHERE s.id = NEW.subnet_id AND s.organization_id = NEW.organization_id;
    END IF;
    SELECT cr.client_id INTO credential_client
      FROM credential cr WHERE cr.id = NEW.credential_id AND cr.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    IF credential_client IS NOT NULL AND NEW.client_id IS DISTINCT FROM credential_client THEN
        RAISE EXCEPTION 'a credential of a client binds only to a target of the same client'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'credential_binding_client_rule';
    END IF;
    RETURN NEW;
END
$$;

DROP INDEX idx_credential_binding_probe;
DROP INDEX idx_credential_binding_device_group;
DROP INDEX uq_credential_binding_probe;
DROP INDEX uq_credential_binding_device_group;
ALTER TABLE credential_binding DROP CONSTRAINT credential_binding_target_check;
ALTER TABLE credential_binding ADD CONSTRAINT credential_binding_target_check CHECK (
    (target_kind = 'ci' AND ci_id IS NOT NULL AND subnet_id IS NULL)
    OR (target_kind = 'subnet' AND subnet_id IS NOT NULL AND ci_id IS NULL)
);
ALTER TABLE credential_binding DROP CONSTRAINT credential_binding_probe_fkey;
ALTER TABLE credential_binding DROP CONSTRAINT credential_binding_device_group_fkey;
ALTER TABLE credential_binding DROP COLUMN probe_id;
ALTER TABLE credential_binding DROP COLUMN device_group_id;

DROP TRIGGER trg_monitoring_group_member_scope ON monitoring_group;
DROP TRIGGER trg_ci_monitoring_scope ON ci;
DROP FUNCTION propagate_monitoring_scope();

DROP TABLE monitoring_profile;
DROP FUNCTION monitoring_profile_scope();
DROP TABLE monitoring_group_member;
DROP FUNCTION monitoring_group_member_scope();
DROP TABLE monitoring_group;
DROP TABLE probe;
DROP FUNCTION probe_ci_scope();
