-- NFR-03 (WP-206): the projection of the field values into ci.attributes
-- (_overrides, _observed, _provenance; migration 000099) ran per row. A new
-- CI records about ten field values, and each row updated the CI again,
-- re-aggregated all of its field values and fired every trigger of ci. The
-- projection now runs once per statement for each CI the statement touched,
-- with the same result.
DROP TRIGGER trg_ci_field_value_project ON ci_field_value;

CREATE FUNCTION ci_field_value_project_rows() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        UPDATE ci SET attributes = (COALESCE(attributes, '{}'::jsonb) - '_overrides' - '_observed' - '_provenance')
            || ci_field_namespaces(ci.id)
        WHERE id IN (SELECT DISTINCT ci_id FROM projected_new);
    ELSIF TG_OP = 'UPDATE' THEN
        UPDATE ci SET attributes = (COALESCE(attributes, '{}'::jsonb) - '_overrides' - '_observed' - '_provenance')
            || ci_field_namespaces(ci.id)
        WHERE id IN (SELECT ci_id FROM projected_new UNION SELECT ci_id FROM projected_old);
    ELSE
        UPDATE ci SET attributes = (COALESCE(attributes, '{}'::jsonb) - '_overrides' - '_observed' - '_provenance')
            || ci_field_namespaces(ci.id)
        WHERE id IN (SELECT DISTINCT ci_id FROM projected_old);
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_ci_field_value_project_insert AFTER INSERT ON ci_field_value
    REFERENCING NEW TABLE AS projected_new
    FOR EACH STATEMENT EXECUTE FUNCTION ci_field_value_project_rows();
CREATE TRIGGER trg_ci_field_value_project_update AFTER UPDATE ON ci_field_value
    REFERENCING OLD TABLE AS projected_old NEW TABLE AS projected_new
    FOR EACH STATEMENT EXECUTE FUNCTION ci_field_value_project_rows();
CREATE TRIGGER trg_ci_field_value_project_delete AFTER DELETE ON ci_field_value
    REFERENCING OLD TABLE AS projected_old
    FOR EACH STATEMENT EXECUTE FUNCTION ci_field_value_project_rows();

DROP FUNCTION ci_field_value_project();
