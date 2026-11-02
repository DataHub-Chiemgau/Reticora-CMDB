-- Migration 000144: TRUNCATE guard on the tenant tables (TEN-03, CH19, DB-04
-- in part; WP-352, F-192).
--
-- reticora_app may SET ROLE reticora_owner for runtime index DDL (000078).
-- Owning a table implies the TRUNCATE privilege, and TRUNCATE ignores row
-- level security, FORCE included: as reticora_owner one statement emptied a
-- tenant table for every organization. Event triggers do not fire on
-- TRUNCATE, so reticora_restrict_app_ddl did not stop it.
--
-- Every tenant table (organization and every table with organization_id in
-- schema public, the hypertable metric_sample included) gets a statement-level
-- BEFORE TRUNCATE trigger that aborts with 42501 for reticora_app and
-- reticora_owner. The materialization hypertable of a continuous aggregate in
-- public with organization_id (metric_sample_1h) gets the same trigger:
-- TRUNCATE on the aggregate's view truncates it. The migrations and the
-- maintenance login are not affected. The application roles cannot remove the
-- guard: DROP TRIGGER and ALTER TABLE ... DISABLE TRIGGER are DDL that
-- reticora_restrict_app_ddl (000078) refuses them, and the function belongs to
-- the migrating role. The startup check (internal/database/role_check.go)
-- reports every tenant table without the guard, so a later migration that
-- adds a tenant table must add the trigger as well.

CREATE FUNCTION reticora_truncate_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF current_user IN ('reticora_app', 'reticora_owner') THEN
        RAISE EXCEPTION 'role % may not truncate tenant table %', current_user, TG_TABLE_NAME
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NULL;
END
$$;

DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN
        SELECT c.oid::regclass AS rel
          FROM pg_class c
         WHERE c.relnamespace = 'public'::regnamespace
           AND c.relkind IN ('r', 'p')
           AND (c.relname = 'organization' OR EXISTS (
                SELECT 1 FROM pg_attribute a
                 WHERE a.attrelid = c.oid AND a.attname = 'organization_id' AND NOT a.attisdropped))
    LOOP
        EXECUTE format('CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON %s '
                       'FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard()', rec.rel);
    END LOOP;

    IF to_regclass('_timescaledb_catalog.continuous_agg') IS NOT NULL THEN
        FOR rec IN
            SELECT format('%I.%I', h.schema_name, h.table_name)::regclass AS rel
              FROM _timescaledb_catalog.continuous_agg ca
              JOIN _timescaledb_catalog.hypertable h ON h.id = ca.mat_hypertable_id
             WHERE ca.user_view_schema = 'public'
               AND EXISTS (
                   SELECT 1 FROM pg_attribute a
                    WHERE a.attrelid = format('%I.%I', h.schema_name, h.table_name)::regclass
                      AND a.attname = 'organization_id' AND NOT a.attisdropped)
        LOOP
            EXECUTE format('CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON %s '
                           'FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard()', rec.rel);
        END LOOP;
    END IF;
END
$$;
