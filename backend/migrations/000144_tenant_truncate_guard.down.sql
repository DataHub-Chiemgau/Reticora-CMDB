-- Reverts WP-352: removes the TRUNCATE guard from every table that carries it
-- (tenant tables in public and the materialization hypertables of the
-- continuous aggregates) and the guard function.

DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN
        SELECT t.tgrelid::regclass AS rel, t.tgname
          FROM pg_trigger t
         WHERE t.tgfoid = 'reticora_truncate_guard()'::regprocedure
    LOOP
        EXECUTE format('DROP TRIGGER %I ON %s', rec.tgname, rec.rel);
    END LOOP;
END
$$;

DROP FUNCTION reticora_truncate_guard();
