-- Migration 000218 down: the tag assignments of the CIs return to
-- attributes.tags (the German display names, sorted); the catalog, the
-- assignments, the system tag trigger and the CHECK ci_field_value_no_tags
-- go, propagate_ci_scope returns to its version of 000209. Overrides,
-- reviews, merge snapshots and saved views are not restored. The policies,
-- the indexes, the scope trigger and the TRUNCATE guards go with the tables.

ALTER TABLE ci_field_value DROP CONSTRAINT IF EXISTS ci_field_value_no_tags;

ALTER TABLE ci DISABLE TRIGGER trg_ci_schema_version;
ALTER TABLE ci DISABLE TRIGGER trg_ci_updated_at;
UPDATE ci c SET attributes = c.attributes || jsonb_build_object('tags', a.tags)
FROM (SELECT ct.ci_id, jsonb_agg(t.display_name ->> 'de' ORDER BY t.display_name ->> 'de') AS tags
      FROM ci_tag ct JOIN tag t ON t.id = ct.tag_id
      GROUP BY ct.ci_id) a
WHERE a.ci_id = c.id;
ALTER TABLE ci ENABLE TRIGGER trg_ci_updated_at;
ALTER TABLE ci ENABLE TRIGGER trg_ci_schema_version;

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
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

DROP TRIGGER IF EXISTS trg_organization_default_tags ON organization;
DROP FUNCTION IF EXISTS organization_default_tags();
DROP TABLE IF EXISTS ci_tag;
DROP TABLE IF EXISTS tag;
DROP FUNCTION IF EXISTS tag_normalize_key(text);
