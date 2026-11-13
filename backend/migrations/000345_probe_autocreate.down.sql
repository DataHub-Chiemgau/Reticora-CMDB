-- propagate_monitoring_scope as of 000307, without the suppression table.
CREATE OR REPLACE FUNCTION propagate_monitoring_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    IF TG_TABLE_NAME = 'ci' THEN
        UPDATE probe SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
        UPDATE monitoring_group_member SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
        UPDATE monitoring_profile SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
    ELSE
        UPDATE monitoring_group_member SET group_id = group_id WHERE group_id = NEW.id AND organization_id = NEW.organization_id;
        UPDATE monitoring_profile SET group_id = group_id WHERE group_id = NEW.id AND organization_id = NEW.organization_id;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
DROP TABLE IF EXISTS probe_autocreate_suppression;
DROP FUNCTION IF EXISTS probe_autocreate_suppression_scope();
ALTER TABLE probe DROP COLUMN IF EXISTS origin;
