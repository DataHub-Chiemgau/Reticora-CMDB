-- Migration 000346 (provisional number): the assignment set of a collector
-- (PRB-11, WP-948) carries the credential ids of its probes (bindings of
-- target kind probe) and reports a probe whose credential is missing from
-- the bundle. Its version is collector_group_member.config_version, so
-- every write of such a binding raises the version of the collectors the
-- probe is assigned to (probe_assignment), also a delete through the
-- cascade of a probe: a collector asking with if_none_match
-- gets the new set instead of not_modified, and a version never stands for
-- two different sets. The update runs with the scope widened (000139): a
-- client-scoped writer of a binding cannot see an org-wide collector group,
-- the versions of its collectors rise all the same.

CREATE FUNCTION credential_binding_probe_version() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    org uuid;
    probes uuid[];
BEGIN
    IF TG_OP = 'DELETE' THEN
        org := OLD.organization_id;
        probes := ARRAY[OLD.probe_id];
    ELSIF TG_OP = 'INSERT' THEN
        org := NEW.organization_id;
        probes := ARRAY[NEW.probe_id];
    ELSE
        org := NEW.organization_id;
        probes := ARRAY[OLD.probe_id, NEW.probe_id];
    END IF;
    probes := array_remove(probes, NULL);
    IF cardinality(probes) = 0 THEN
        RETURN NULL;
    END IF;
    previous := app_scope_widen();
    UPDATE collector_group_member m SET config_version = m.config_version + 1
     WHERE m.organization_id = org
       AND m.collector_id IN (SELECT a.collector_id FROM probe_assignment a
                               WHERE a.organization_id = org AND a.probe_id = ANY (probes));
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER credential_binding_probe_version
    AFTER INSERT OR UPDATE OR DELETE ON credential_binding
    FOR EACH ROW EXECUTE FUNCTION credential_binding_probe_version();
