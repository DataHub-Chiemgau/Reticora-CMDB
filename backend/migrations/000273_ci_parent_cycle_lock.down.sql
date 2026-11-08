-- Down of 000273: ci_check_parent_cycle gets its body of 000089 back, the
-- cycle check without the advisory lock.
CREATE OR REPLACE FUNCTION ci_check_parent_cycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.parent_ci_id IS NULL THEN
        RETURN NEW;
    END IF;
    IF EXISTS (
        WITH RECURSIVE up (id) AS (
            SELECT NEW.parent_ci_id
            UNION
            SELECT c.parent_ci_id FROM ci c JOIN up ON c.id = up.id WHERE c.parent_ci_id IS NOT NULL
        )
        SELECT 1 FROM up WHERE id = NEW.id
    ) THEN
        RAISE EXCEPTION 'CI % would contain itself', NEW.id
            USING ERRCODE = '23514', CONSTRAINT = 'ci_parent_cycle';
    END IF;
    RETURN NEW;
END
$$;
