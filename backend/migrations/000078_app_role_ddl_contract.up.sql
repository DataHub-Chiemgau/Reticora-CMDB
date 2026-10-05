-- Migration 000078: DDL contract of the application role (CH19, TEN-03,
-- DB-04 in part; WP-064).
--
-- CH19 lets the application create indexes at runtime, so the application
-- role needs DDL rights on the application schema, without SUPERUSER and
-- without BYPASSRLS. Creating an index requires owning the table, and until
-- now every table was owned by the migrating superuser.
--
-- The tables move to a dedicated owner role reticora_owner (NOLOGIN,
-- NOSUPERUSER, NOBYPASSRLS) with CREATE on schema public. reticora_app may
-- SET ROLE to it but does not inherit its privileges, so ordinary requests run
-- without DDL rights and only the runtime DDL path switches roles explicitly.
-- Every tenant table keeps ENABLE and FORCE ROW LEVEL SECURITY, so the policies
-- apply to the owner as well. The application's startup check
-- (internal/database/role_check.go) verifies this contract and refuses to
-- start on any deviation.
--
-- Two event triggers keep the contract for later migrations and at runtime:
-- tables created in schema public are handed to reticora_owner, and the two
-- application roles may only run index DDL (CREATE/ALTER/DROP INDEX); they can
-- neither turn row level security off nor change policies, grants or tables.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'reticora_owner') THEN
        CREATE ROLE reticora_owner NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION;
    END IF;
END
$$;
ALTER ROLE reticora_owner NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION;

GRANT USAGE, CREATE ON SCHEMA public TO reticora_owner;

-- SET without INHERIT: SET ROLE reticora_owner is allowed, its privileges
-- (ownership, schema CREATE) are not part of an ordinary reticora_app session.
GRANT reticora_owner TO reticora_app WITH INHERIT FALSE, SET TRUE;

-- The migrating role must be able to hand objects to reticora_owner.
DO $$
BEGIN
    EXECUTE format('GRANT reticora_owner TO %I WITH INHERIT FALSE, SET TRUE', current_user);
EXCEPTION
    WHEN OTHERS THEN NULL; -- already a member, or current_user is a superuser
END
$$;

-- Tenant tables: ENABLE and FORCE row level security on every table with an
-- organization_id column (and organization itself). TimescaleDB hypertables
-- cannot carry row level security and are protected by a security-barrier
-- view instead (WP-040); they keep their owner, because TimescaleDB runs their
-- background jobs as the hypertable owner, which needs LOGIN.
CREATE TEMP TABLE m078_hypertables (relid oid) ON COMMIT DROP;
DO $$
BEGIN
    IF to_regclass('_timescaledb_catalog.hypertable') IS NOT NULL THEN
        INSERT INTO m078_hypertables
        SELECT format('%I.%I', schema_name, table_name)::regclass::oid FROM _timescaledb_catalog.hypertable;
    END IF;
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
           AND c.oid NOT IN (SELECT relid FROM m078_hypertables)
           AND (c.relname = 'organization' OR EXISTS (
                SELECT 1 FROM pg_attribute a
                 WHERE a.attrelid = c.oid AND a.attname = 'organization_id' AND NOT a.attisdropped))
           AND NOT (c.relrowsecurity AND c.relforcerowsecurity)
    LOOP
        EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', rec.rel);
        EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', rec.rel);
    END LOOP;
END
$$;

-- Ownership of every table of schema public (owned sequences follow their
-- table) except the hypertables and the bookkeeping of the migration tool.
DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN
        SELECT c.oid::regclass AS rel
          FROM pg_class c
         WHERE c.relnamespace = 'public'::regnamespace
           AND c.relkind IN ('r', 'p')
           AND c.relname <> 'schema_migrations'
           AND c.oid NOT IN (SELECT relid FROM m078_hypertables)
    LOOP
        EXECUTE format('ALTER TABLE %s OWNER TO reticora_owner', rec.rel);
    END LOOP;
END
$$;

-- Tables that later migrations create in schema public go to reticora_owner.
CREATE FUNCTION reticora_assign_owner() RETURNS event_trigger
LANGUAGE plpgsql AS $$
DECLARE
    obj RECORD;
BEGIN
    FOR obj IN
        SELECT c.oid::regclass AS rel
          FROM pg_event_trigger_ddl_commands() d
          JOIN pg_class c ON c.oid = d.objid
         WHERE d.classid = 'pg_class'::regclass
           AND d.schema_name = 'public'
           AND c.relkind IN ('r', 'p')
           AND c.relname <> 'schema_migrations'
           AND pg_get_userbyid(c.relowner) <> 'reticora_owner'
    LOOP
        EXECUTE format('ALTER TABLE %s OWNER TO reticora_owner', obj.rel);
    END LOOP;
END
$$;

CREATE EVENT TRIGGER reticora_assign_owner ON ddl_command_end
    WHEN TAG IN ('CREATE TABLE', 'CREATE TABLE AS', 'SELECT INTO')
    EXECUTE FUNCTION reticora_assign_owner();

-- The application roles may only run index DDL. Everything else (ALTER TABLE
-- ... NO FORCE ROW LEVEL SECURITY, policies, grants, new tables) stays with
-- the migrations.
CREATE FUNCTION reticora_restrict_app_ddl() RETURNS event_trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF current_user IN ('reticora_app', 'reticora_owner')
       AND tg_tag NOT IN ('CREATE INDEX', 'ALTER INDEX', 'DROP INDEX') THEN
        RAISE EXCEPTION 'role % may only run index DDL, not %', current_user, tg_tag
            USING ERRCODE = 'insufficient_privilege';
    END IF;
END
$$;

CREATE EVENT TRIGGER reticora_restrict_app_ddl ON ddl_command_start
    EXECUTE FUNCTION reticora_restrict_app_ddl();
