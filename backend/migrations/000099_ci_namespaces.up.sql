-- WP-091 (CI-10, GLO-13): attributes holds the business keys without prefix
-- and the namespaces _overrides, _observed, _provenance and _instance.
--   _overrides   {field: {value, by, at, reason}}   active manual overrides
--   _observed    {field: {value, source, rank, at}} observed value held back
--                                                   by an active override
--   _provenance  {field: {source, rank, at}}        source of each field
--   _instance    {key: value}                       instance attributes (MET-14)
-- The first three are projected from ci_field_value (the write path of
-- reconciliation and overrides) by trigger; _instance is written by the CI
-- repository.

-- The namespaces of a CI from its field provenance.
CREATE FUNCTION ci_field_namespaces(p_ci uuid) RETURNS jsonb
LANGUAGE sql STABLE AS $$
    SELECT COALESCE(jsonb_object_agg(k, v), '{}'::jsonb) FROM (VALUES
        ('_overrides', (SELECT jsonb_object_agg(field_name, jsonb_build_object(
                'value', override_value, 'by', override_author, 'at', override_at, 'reason', override_reason))
            FROM ci_field_value WHERE ci_id = p_ci AND override_at IS NOT NULL)),
        ('_observed', (SELECT jsonb_object_agg(field_name, jsonb_build_object(
                'value', discovered_value, 'source', discovered_source, 'rank', discovered_rank,
                'at', COALESCE(observed_at, discovered_at)))
            FROM ci_field_value WHERE ci_id = p_ci AND override_at IS NOT NULL AND discovered_at IS NOT NULL)),
        ('_provenance', (SELECT jsonb_object_agg(field_name, jsonb_build_object(
                'source', discovered_source, 'rank', discovered_rank, 'at', COALESCE(observed_at, discovered_at)))
            FROM ci_field_value WHERE ci_id = p_ci AND discovered_at IS NOT NULL))
    ) AS ns(k, v) WHERE v IS NOT NULL
$$;

-- The projected namespaces present in an attributes document; a write of
-- the business attributes keeps them.
CREATE FUNCTION ci_attribute_namespaces(p_attributes jsonb) RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$
    SELECT COALESCE(jsonb_object_agg(key, value), '{}'::jsonb)
    FROM jsonb_each(COALESCE(p_attributes, '{}'::jsonb))
    WHERE key IN ('_overrides', '_observed', '_provenance')
$$;

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

-- Existing data: project the provenance ...
UPDATE ci SET attributes = (COALESCE(attributes, '{}'::jsonb) - '_overrides' - '_observed' - '_provenance')
    || ci_field_namespaces(ci.id)
WHERE EXISTS (SELECT 1 FROM ci_field_value v WHERE v.ci_id = ci.id);

-- ... and move the instance attribute values into _instance.
UPDATE ci SET attributes = (ci.attributes - inst.names)
    || jsonb_build_object('_instance', COALESCE(ci.attributes -> '_instance', '{}'::jsonb) || inst.vals)
FROM (
    SELECT d.ci_id, array_agg(d.name) AS names, jsonb_object_agg(d.name, c.attributes -> d.name) AS vals
    FROM ci_instance_field_definition d JOIN ci c ON c.id = d.ci_id
    WHERE c.attributes ? d.name
    GROUP BY d.ci_id
) AS inst
WHERE ci.id = inst.ci_id;
