-- Restores ci_schema_version of 000107: only an INSERT, a change of
-- ci_type_id or of attributes sets the schema version.
CREATE OR REPLACE FUNCTION ci_schema_version() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' OR NEW.ci_type_id IS DISTINCT FROM OLD.ci_type_id
       OR NEW.attributes IS DISTINCT FROM OLD.attributes THEN
        NEW.schema_version := COALESCE((SELECT t.schema_version FROM ci_type t WHERE t.id = NEW.ci_type_id), 1);
    ELSE
        NEW.schema_version := OLD.schema_version;
    END IF;
    RETURN NEW;
END
$$;
