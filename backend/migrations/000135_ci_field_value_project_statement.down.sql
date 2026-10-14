DROP TRIGGER trg_ci_field_value_project_delete ON ci_field_value;
DROP TRIGGER trg_ci_field_value_project_update ON ci_field_value;
DROP TRIGGER trg_ci_field_value_project_insert ON ci_field_value;
DROP FUNCTION ci_field_value_project_rows();

CREATE FUNCTION ci_field_value_project() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_ci uuid := CASE WHEN TG_OP = 'DELETE' THEN OLD.ci_id ELSE NEW.ci_id END;
BEGIN
    UPDATE ci SET attributes = (COALESCE(attributes, '{}'::jsonb) - '_overrides' - '_observed' - '_provenance')
        || ci_field_namespaces(v_ci)
    WHERE id = v_ci;
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_ci_field_value_project AFTER INSERT OR UPDATE OR DELETE ON ci_field_value
    FOR EACH ROW EXECUTE FUNCTION ci_field_value_project();
