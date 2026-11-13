-- Migration 000356 (provisional number): the purge of an organization after
-- the grace period of its deletion (TLC-06, WP-1154).
--
-- The runtime role cannot remove every row of an organization: append-only
-- triggers refuse a DELETE (audit_log, stock_movement, completed
-- stocktakes, disposal_record, assignment_signature_legacy,
-- alarm_transition, incident_transition, alarms, incidents, form and
-- workflow definition versions, the default IPAM section and VRF). The
-- purge goes through org_purge_batch, a SECURITY DEFINER function of the
-- NOLOGIN role reticora_org_purger (NOSUPERUSER, NOBYPASSRLS, no member: no
-- login reaches it, the startup check reports one that does,
-- database.OrgPurgerRole). The role holds the table, sequence and
-- function rights of reticora_app (so the triggers a delete fires, history
-- and search index, work as for the application) and SELECT and DELETE on
-- every table with organization_id, also future ones. Row level security binds it to app.org_id like every
-- role.
--
-- org_purge_batch(org, table, limit) deletes at most limit rows of the
-- organization from the table and returns the number; it refuses (42501)
-- unless the organization is in purging, app.org_id names it and the table
-- has the column organization_id. org_purge_keys(org, table) lists the keys
-- of those rows (the primary key as text, without one the MD5 of the row),
-- sorted, under the same checks: the purge hashes them before it deletes.
-- A hypertable (metric_sample, probe_result) is emptied with one plain
-- DELETE by organization: TimescaleDB refuses ctid on compressed chunks
-- (transparent decompression only knows tableoid). A continuous aggregate
-- (metric_sample_1h, probe_result_5m/1h/1d) is a view in public; its rows
-- lie in a materialization hypertable of _timescaledb_internal, which the
-- functions empty in its place (org_purge_target): a refresh only
-- recomputes its window and would keep the older buckets.
-- Every guard trigger lets a DELETE pass only for that role and only for a
-- row of an organization in purging (org_purge_active); everything else
-- behaves as before. operator_audit already knows the kind system
-- (000290).

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'reticora_org_purger') THEN
        CREATE ROLE reticora_org_purger NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION;
    END IF;
END
$$;
ALTER ROLE reticora_org_purger NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION;
-- A migrating login without SUPERUSER must be able to hand the functions to
-- the role (as for reticora_audit_sealer, 000188).
DO $$
BEGIN
    EXECUTE format('GRANT reticora_org_purger TO %I WITH INHERIT FALSE, SET TRUE', current_user);
EXCEPTION
    WHEN OTHERS THEN NULL; -- already a member, or current_user is a superuser
END
$$;

-- The rights of the application (the triggers of a delete write history
-- and search rows: the role has the table, sequence and function rights
-- reticora_app has, not its membership, so no role of the application can
-- reach it), plus SELECT and DELETE on every table with organization_id,
-- also where the application has none (assignment_signature_legacy, the
-- hypertables behind their views).
GRANT USAGE ON SCHEMA public TO reticora_org_purger;
DO $$
DECLARE
    r record;
    p text;
BEGIN
    FOR r IN
        SELECT c.oid, c.oid::regclass AS name,
               EXISTS (SELECT 1 FROM pg_attribute a
                        WHERE a.attrelid = c.oid AND a.attname = 'organization_id' AND NOT a.attisdropped) AS tenant
          FROM pg_class c
         WHERE c.relnamespace = 'public'::regnamespace AND c.relkind IN ('r', 'p', 'v', 'm') AND NOT c.relispartition
    LOOP
        FOREACH p IN ARRAY ARRAY['SELECT', 'INSERT', 'UPDATE', 'DELETE'] LOOP
            IF has_table_privilege('reticora_app', r.oid, p) OR (r.tenant AND p IN ('SELECT', 'DELETE')) THEN
                EXECUTE format('GRANT %s ON %s TO reticora_org_purger', p, r.name);
            END IF;
        END LOOP;
    END LOOP;
    FOR r IN
        SELECT p.oid::regprocedure AS name FROM pg_proc p
         WHERE p.pronamespace = 'public'::regnamespace AND p.prokind = 'f'
           AND has_function_privilege('reticora_app', p.oid, 'EXECUTE')
           AND p.proname <> 'audit_seal_pii'
    LOOP
        EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO reticora_org_purger', r.name);
    END LOOP;
END
$$;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO reticora_org_purger;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO reticora_org_purger;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO reticora_org_purger;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT EXECUTE ON FUNCTIONS TO reticora_org_purger;
-- The materialization hypertables of the continuous aggregates with
-- organization_id: SELECT and DELETE (they have no row level security; the
-- functions filter by the organization they checked).
DO $$
DECLARE
    r record;
BEGIN
    IF to_regclass('timescaledb_information.continuous_aggregates') IS NULL THEN
        RETURN;
    END IF;
    EXECUTE 'GRANT USAGE ON SCHEMA _timescaledb_internal TO reticora_org_purger';
    FOR r IN EXECUTE $q$
        SELECT format('%I.%I', ca.materialization_hypertable_schema, ca.materialization_hypertable_name) AS mat
          FROM timescaledb_information.continuous_aggregates ca
         WHERE ca.view_schema = 'public'
           AND EXISTS (SELECT 1 FROM pg_attribute a
                        WHERE a.attrelid = format('%I.%I', ca.view_schema, ca.view_name)::regclass
                          AND a.attname = 'organization_id' AND NOT a.attisdropped)$q$
    LOOP
        EXECUTE format('GRANT SELECT, DELETE ON %s TO reticora_org_purger', r.mat);
    END LOOP;
END
$$;

-- org_purge_active reports whether the current role is the purger and the
-- organization of a row is in purging: the exception of the guards.
CREATE FUNCTION org_purge_active(p_org uuid) RETURNS boolean
LANGUAGE sql STABLE
SET search_path = pg_catalog, public, pg_temp
AS $$
    SELECT current_user = 'reticora_org_purger'
       AND EXISTS (SELECT 1 FROM organization o WHERE o.id = p_org AND o.deletion_state = 'purging')
$$;

-- org_purge_target is the relation that holds the rows of p_table: the
-- materialization hypertable of a continuous aggregate, else the table.
CREATE FUNCTION org_purge_target(p_table regclass) RETURNS regclass
LANGUAGE plpgsql STABLE
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    mat regclass;
BEGIN
    IF to_regclass('timescaledb_information.continuous_aggregates') IS NULL THEN
        RETURN p_table;
    END IF;
    EXECUTE $q$SELECT format('%I.%I', ca.materialization_hypertable_schema, ca.materialization_hypertable_name)::regclass
                 FROM timescaledb_information.continuous_aggregates ca
                WHERE format('%I.%I', ca.view_schema, ca.view_name)::regclass = $1$q$
       INTO mat USING p_table;
    RETURN COALESCE(mat, p_table);
END
$$;

-- org_purge_hypertable reports whether p_rel is a hypertable, the
-- materialization hypertables of continuous aggregates included (the view
-- timescaledb_information.hypertables leaves them out).
CREATE FUNCTION org_purge_hypertable(p_rel regclass) RETURNS boolean
LANGUAGE plpgsql STABLE
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    found boolean;
BEGIN
    IF to_regclass('_timescaledb_catalog.hypertable') IS NULL THEN
        RETURN false;
    END IF;
    EXECUTE $q$SELECT EXISTS (SELECT 1 FROM _timescaledb_catalog.hypertable h
                 WHERE to_regclass(format('%I.%I', h.schema_name, h.table_name)) = $1)$q$
       INTO found USING p_rel;
    RETURN found;
END
$$;

-- org_purge_check refuses (42501) unless p_org is in purging, app.org_id
-- names it and p_table is a table or a continuous aggregate of public with
-- the column organization_id.
CREATE FUNCTION org_purge_check(p_org uuid, p_table regclass) RETURNS void
LANGUAGE plpgsql STABLE
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF p_org IS NULL OR NULLIF(current_setting('app.org_id', true), '') IS DISTINCT FROM p_org::text THEN
        RAISE EXCEPTION 'org purge: app.org_id does not name the organization' USING ERRCODE = '42501';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM organization o WHERE o.id = p_org AND o.deletion_state = 'purging') THEN
        RAISE EXCEPTION 'org purge: the organization is not in purging' USING ERRCODE = '42501';
    END IF;
    IF p_table IS NULL OR NOT EXISTS (SELECT 1 FROM pg_attribute a
            WHERE a.attrelid = p_table AND a.attname = 'organization_id' AND NOT a.attisdropped)
       OR (SELECT c.relnamespace FROM pg_class c WHERE c.oid = p_table) <> 'public'::regnamespace
       OR ((SELECT c.relkind FROM pg_class c WHERE c.oid = p_table) NOT IN ('r', 'p')
           AND org_purge_target(p_table) = p_table) THEN
        RAISE EXCEPTION 'org purge: % has no column organization_id', p_table USING ERRCODE = '42501';
    END IF;
END
$$;

-- org_purge_batch deletes at most p_limit rows of the organization from
-- p_table and returns their number.
CREATE FUNCTION org_purge_batch(p_org uuid, p_table regclass, p_limit int) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    n bigint;
    previous text[];
    target regclass;
BEGIN
    PERFORM org_purge_check(p_org, p_table);
    target := org_purge_target(p_table);
    -- Every row of the organization, whatever the client, site and team
    -- scope or the section rights (000139); restored on every path.
    previous := app_scope_widen();
    BEGIN
        IF org_purge_hypertable(target) THEN
            -- No ctid on compressed chunks: one DELETE by organization.
            EXECUTE format('DELETE FROM %s WHERE organization_id = $1', target) USING p_org;
        ELSE
            EXECUTE format('DELETE FROM %s WHERE (tableoid, ctid) IN (SELECT tableoid, ctid FROM %s WHERE organization_id = $1 LIMIT $2)',
                           target, target)
              USING p_org, GREATEST(p_limit, 1);
        END IF;
        GET DIAGNOSTICS n = ROW_COUNT;
    EXCEPTION WHEN OTHERS THEN
        PERFORM app_scope_restore(previous);
        RAISE;
    END;
    PERFORM app_scope_restore(previous);
    RETURN n;
END
$$;

-- org_purge_rows deletes the rows of the organization in p_table one by
-- one and skips those a restricting foreign key still holds (a cycle of
-- restricting keys such as credential and vault_connection, a key of a
-- table to itself, a cascade into a row another one refers to); it returns
-- the number deleted. The purge calls it only when a batch fails with a
-- foreign key violation; a later pass deletes the rows skipped.
CREATE FUNCTION org_purge_rows(p_org uuid, p_table regclass) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    n bigint := 0;
    r record;
    previous text[];
BEGIN
    PERFORM org_purge_check(p_org, p_table);
    IF org_purge_hypertable(org_purge_target(p_table)) THEN
        -- No ctid on compressed chunks and no foreign key to skip.
        RETURN org_purge_batch(p_org, p_table, 1);
    END IF;
    previous := app_scope_widen();
    BEGIN
        FOR r IN EXECUTE format('SELECT tableoid, ctid FROM %s WHERE organization_id = $1', p_table) USING p_org LOOP
            BEGIN
                EXECUTE format('DELETE FROM %s WHERE tableoid = $1 AND ctid = $2', p_table) USING r.tableoid, r.ctid;
                n := n + 1;
            EXCEPTION WHEN foreign_key_violation THEN
                NULL; -- still referenced; a later pass deletes it
            END;
        END LOOP;
    EXCEPTION WHEN OTHERS THEN
        PERFORM app_scope_restore(previous);
        RAISE;
    END;
    PERFORM app_scope_restore(previous);
    RETURN n;
END
$$;

-- org_purge_keys lists the keys of the rows of the organization in p_table,
-- sorted bytewise: the primary key as text, without one the MD5 of the row.
CREATE FUNCTION org_purge_keys(p_org uuid, p_table regclass) RETURNS SETOF text
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    pk text;
    previous text[];
    target regclass;
BEGIN
    PERFORM org_purge_check(p_org, p_table);
    target := org_purge_target(p_table);
    SELECT string_agg(format('t.%I', a.attname), ', ' ORDER BY k.ord) INTO pk
      FROM pg_index i
      CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
      JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
     WHERE i.indrelid = target AND i.indisprimary;
    IF pk IS NULL THEN
        pk := 'md5(t::text)';
    ELSE
        pk := format('(ROW(%s))::text', pk);
    END IF;
    previous := app_scope_widen();
    BEGIN
        RETURN QUERY EXECUTE format('SELECT s.k FROM (SELECT %s AS k FROM %s t WHERE t.organization_id = $1) s ORDER BY s.k COLLATE "C"',
                                    pk, target)
          USING p_org;
    EXCEPTION WHEN OTHERS THEN
        PERFORM app_scope_restore(previous);
        RAISE;
    END;
    PERFORM app_scope_restore(previous);
END
$$;

REVOKE ALL ON FUNCTION org_purge_batch(uuid, regclass, int) FROM PUBLIC;
REVOKE ALL ON FUNCTION org_purge_keys(uuid, regclass) FROM PUBLIC;
REVOKE ALL ON FUNCTION org_purge_rows(uuid, regclass) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION org_purge_rows(uuid, regclass) TO reticora_app;
REVOKE ALL ON FUNCTION org_purge_check(uuid, regclass) FROM PUBLIC;
REVOKE ALL ON FUNCTION org_purge_target(regclass) FROM PUBLIC;
REVOKE ALL ON FUNCTION org_purge_hypertable(regclass) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION org_purge_target(regclass) TO reticora_app, reticora_org_purger;
GRANT EXECUTE ON FUNCTION org_purge_hypertable(regclass) TO reticora_app, reticora_org_purger;
GRANT EXECUTE ON FUNCTION org_purge_batch(uuid, regclass, int) TO reticora_app;
GRANT EXECUTE ON FUNCTION org_purge_keys(uuid, regclass) TO reticora_app;
GRANT EXECUTE ON FUNCTION org_purge_check(uuid, regclass) TO reticora_app, reticora_org_purger;
-- The owner of a function needs CREATE on its schema; the role keeps it only
-- for the hand-over. The grants above pass to the new owner.
GRANT CREATE ON SCHEMA public TO reticora_org_purger;
ALTER FUNCTION org_purge_batch(uuid, regclass, int) OWNER TO reticora_org_purger;
ALTER FUNCTION org_purge_keys(uuid, regclass) OWNER TO reticora_org_purger;
ALTER FUNCTION org_purge_rows(uuid, regclass) OWNER TO reticora_org_purger;
REVOKE CREATE ON SCHEMA public FROM reticora_org_purger;

-- The guards: a DELETE of the purger for an organization in purging
-- passes, everything else as before.
CREATE OR REPLACE FUNCTION public.reject_audit_log_mutation()
 RETURNS trigger
 LANGUAGE plpgsql
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE
    refs TEXT[];
BEGIN
    -- The purge of an organization (TLC-06, WP-1154): only the role
    -- reticora_org_purger, only for a row of an organization in purging.
    IF TG_OP = 'DELETE' AND public.org_purge_active(OLD.organization_id) THEN
        RETURN OLD;
    END IF;
    IF TG_OP = 'DELETE'
       AND current_setting('app.audit_retention', true) = 'on'
       AND OLD.timestamp < now() - interval '10 years' THEN
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE'
       AND current_user = 'reticora_audit_sealer'
       AND (NEW.id, NEW.organization_id, NEW.seq, NEW.timestamp, NEW.actor_id, NEW.actor_type, NEW.action,
            NEW.resource_type, NEW.resource_id, NEW.source, NEW.request_id, NEW.correlation_id, NEW.created_at,
            NEW.previous_hash, NEW.hash, NEW.chained_digest, NEW.sealed_refs, NEW.sealed_hash)
           IS NOT DISTINCT FROM
           (OLD.id, OLD.organization_id, OLD.seq, OLD.timestamp, OLD.actor_id, OLD.actor_type, OLD.action,
            OLD.resource_type, OLD.resource_id, OLD.source, OLD.request_id, OLD.correlation_id, OLD.created_at,
            OLD.previous_hash, OLD.hash, OLD.chained_digest, OLD.sealed_refs, OLD.sealed_hash) THEN
        refs := audit_changes_seal(OLD.changes, NEW.changes, OLD.organization_id, false);
        IF cardinality(refs) > 0 THEN
            NEW.chained_digest := COALESCE(OLD.chained_digest,
                audit_entry_digest(OLD.organization_id, OLD.seq, OLD.timestamp, OLD.actor_id, OLD.actor_type,
                    OLD.action, OLD.resource_type, OLD.resource_id, OLD.changes, OLD.source, OLD.request_id,
                    OLD.correlation_id));
            NEW.sealed_refs := COALESCE(OLD.sealed_refs, '{}') || refs;
            NEW.sealed_hash := audit_chain_link(OLD.hash,
                audit_entry_digest(NEW.organization_id, NEW.seq, NEW.timestamp, NEW.actor_id, NEW.actor_type,
                    NEW.action, NEW.resource_type, NEW.resource_id, NEW.changes, NEW.source, NEW.request_id,
                    NEW.correlation_id));
            RETURN NEW;
        END IF;
    END IF;
    RAISE EXCEPTION 'audit_log rows are append-only';
END;
$function$;

CREATE OR REPLACE FUNCTION public.reject_stock_movement_mutation()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
BEGIN
    -- The purge of an organization (TLC-06, WP-1154): only the role
    -- reticora_org_purger, only for a row of an organization in purging.
    IF TG_OP = 'DELETE' AND public.org_purge_active(OLD.organization_id) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'stock_movement rows are append-only';
END
$function$;

CREATE OR REPLACE FUNCTION public.reject_completed_stocktake_mutation()
 RETURNS trigger
 LANGUAGE plpgsql
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE
    previous text[];
    st_status TEXT;
    ids UUID[];
BEGIN
    -- The purge of an organization (TLC-06, WP-1154): only the role
    -- reticora_org_purger, only for a row of an organization in purging.
    IF TG_OP = 'DELETE' AND public.org_purge_active(OLD.organization_id) THEN
        RETURN OLD;
    END IF;
    -- The scope refresh (location_refresh_stocktake, stocktake_refresh_items,
    -- stocktake_refresh_scans) re-derives only client_id and site_id after a
    -- location changed its client or site; the derivation triggers run
    -- before this one (trigger names in order), so NEW carries the new
    -- scope. Every other column stays as it was.
    IF TG_OP = 'UPDATE'
       AND (to_jsonb(NEW) - 'client_id' - 'site_id') = (to_jsonb(OLD) - 'client_id' - 'site_id') THEN
        RETURN NEW;
    END IF;
    IF TG_TABLE_NAME = 'stocktake' THEN
        st_status := OLD.status;
    ELSE
        -- Nested: OLD.ci_id is no field of stocktake_item, and an
        -- expression is planned as a whole.
        IF TG_OP = 'UPDATE' AND TG_TABLE_NAME = 'stock_scan' THEN
            IF OLD.ci_id IS NOT NULL AND NEW.ci_id IS NULL
               AND (to_jsonb(NEW) - 'ci_id') = (to_jsonb(OLD) - 'ci_id') THEN
                RETURN NEW;
            END IF;
        END IF;
        IF TG_OP = 'INSERT' THEN
            ids := ARRAY[NEW.stocktake_id];
        ELSIF TG_OP = 'DELETE' THEN
            ids := ARRAY[OLD.stocktake_id];
        ELSE
            ids := ARRAY[OLD.stocktake_id, NEW.stocktake_id];
        END IF;
        previous := app_scope_widen();
        SELECT s.status INTO st_status
          FROM stocktake s
         WHERE s.id = ANY (ids) AND s.status IN ('completed', 'cancelled')
         LIMIT 1;
        PERFORM app_scope_restore(previous);
    END IF;
    IF st_status IN ('completed', 'cancelled') THEN
        RAISE EXCEPTION 'the stocktake is % and its result is immutable', st_status
            USING ERRCODE = '23514', CONSTRAINT = 'stocktake_immutable';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$function$;

CREATE OR REPLACE FUNCTION public.disposal_record_immutable()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
BEGIN
    -- The purge of an organization (TLC-06, WP-1154): only the role
    -- reticora_org_purger, only for a row of an organization in purging.
    IF TG_OP = 'DELETE' AND public.org_purge_active(OLD.organization_id) THEN
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE' AND pg_trigger_depth() > 1
       AND (to_jsonb(NEW) - 'client_id' - 'site_id') = (to_jsonb(OLD) - 'client_id' - 'site_id') THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'disposal_record is append-only (revision-safe)';
END;
$function$;

CREATE OR REPLACE FUNCTION public.guard_assignment_signature_legacy()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
DECLARE
    uuid_pattern constant text := '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';
BEGIN
    -- The purge of an organization (TLC-06, WP-1154): only the role
    -- reticora_org_purger, only for a row of an organization in purging.
    IF TG_OP = 'DELETE' AND public.org_purge_active(OLD.organization_id) THEN
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE'
       AND NEW.id = OLD.id AND NEW.organization_id = OLD.organization_id
       AND NEW.movement_id = OLD.movement_id AND NEW.kind = OLD.kind
       AND NEW.payload_sha256 = OLD.payload_sha256 AND NEW.created_at = OLD.created_at THEN
        IF NEW.payload IS NULL THEN
            RETURN NEW;
        END IF;
        IF jsonb_typeof(OLD.payload) = 'object' AND NOT OLD.payload ? 'document_id'
           AND jsonb_typeof(NEW.payload) = 'object'
           AND (SELECT array_agg(k ORDER BY k) FROM jsonb_object_keys(NEW.payload) AS k)
               = ARRAY['document_id', 'method', 'signed_at', 'signer_surrogate']
           AND jsonb_typeof(NEW.payload -> 'document_id') = 'string'
           AND NEW.payload ->> 'document_id' ~ uuid_pattern
           AND (jsonb_typeof(NEW.payload -> 'signer_surrogate') = 'null'
                OR (jsonb_typeof(NEW.payload -> 'signer_surrogate') = 'string'
                    AND NEW.payload ->> 'signer_surrogate' ~ uuid_pattern))
           AND (jsonb_typeof(NEW.payload -> 'signed_at') = 'null'
                OR (jsonb_typeof(NEW.payload -> 'signed_at') = 'string' AND length(NEW.payload ->> 'signed_at') <= 64))
           AND (jsonb_typeof(NEW.payload -> 'method') = 'null'
                OR (jsonb_typeof(NEW.payload -> 'method') = 'string' AND length(NEW.payload ->> 'method') <= 64)) THEN
            RETURN NEW;
        END IF;
    END IF;
    RAISE EXCEPTION 'assignment_signature_legacy rows are kept; payload may only be cleared or replaced once by its image-free form'
        USING ERRCODE = '42501';
END
$function$;

CREATE OR REPLACE FUNCTION public.reject_alarm_transition_mutation()
 RETURNS trigger
 LANGUAGE plpgsql
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE
    previous text[];
    alarm_org uuid;
    alarm_client uuid;
    alarm_site uuid;
BEGIN
    -- The purge of an organization (TLC-06, WP-1154): only the role
    -- reticora_org_purger, only for a row of an organization in purging.
    IF TG_OP = 'DELETE' AND public.org_purge_active(OLD.organization_id) THEN
        RETURN OLD;
    END IF;
    -- The propagation of a moved alarm (section 3a) re-derives client and
    -- site; nothing else of the row changes, and the values are those of
    -- the alarm.
    IF TG_OP = 'UPDATE' AND pg_trigger_depth() > 1
       AND (NEW.id, NEW.organization_id, NEW.alarm_id, NEW.from_state, NEW.to_state, NEW.from_severity, NEW.to_severity,
            NEW.at, NEW.actor_id, NEW.reason, NEW.comment)
           IS NOT DISTINCT FROM
           (OLD.id, OLD.organization_id, OLD.alarm_id, OLD.from_state, OLD.to_state, OLD.from_severity, OLD.to_severity,
            OLD.at, OLD.actor_id, OLD.reason, OLD.comment) THEN
        previous := app_scope_widen();
        SELECT a.organization_id, a.client_id, a.site_id INTO alarm_org, alarm_client, alarm_site
          FROM alarm a WHERE a.id = NEW.alarm_id AND a.organization_id = NEW.organization_id;
        PERFORM app_scope_restore(previous);
        IF alarm_org IS NOT NULL AND NEW.client_id IS NOT DISTINCT FROM alarm_client AND NEW.site_id IS NOT DISTINCT FROM alarm_site THEN
            RETURN NEW;
        END IF;
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF current_setting('app.alarm_retention', true) = 'on' AND OLD.at < now() - interval '400 days' THEN
            RETURN OLD;
        END IF;
        IF pg_trigger_depth() > 1 AND NOT EXISTS (
            SELECT 1 FROM alarm a WHERE a.id = OLD.alarm_id AND a.organization_id = OLD.organization_id) THEN
            RETURN OLD;
        END IF;
    END IF;
    RAISE EXCEPTION 'alarm_transition rows are append-only'
        USING ERRCODE = 'restrict_violation';
END
$function$;

CREATE OR REPLACE FUNCTION public.reject_incident_transition_mutation()
 RETURNS trigger
 LANGUAGE plpgsql
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE
    previous text[];
    incident_org uuid;
    incident_client uuid;
    incident_site uuid;
BEGIN
    -- The purge of an organization (TLC-06, WP-1154): only the role
    -- reticora_org_purger, only for a row of an organization in purging.
    IF TG_OP = 'DELETE' AND public.org_purge_active(OLD.organization_id) THEN
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE' AND pg_trigger_depth() > 1
       AND (NEW.id, NEW.organization_id, NEW.incident_id, NEW.from_state, NEW.to_state, NEW.at, NEW.actor_id,
            NEW.reason, NEW.details)
           IS NOT DISTINCT FROM
           (OLD.id, OLD.organization_id, OLD.incident_id, OLD.from_state, OLD.to_state, OLD.at, OLD.actor_id,
            OLD.reason, OLD.details) THEN
        previous := app_scope_widen();
        SELECT i.organization_id, i.client_id, i.site_id INTO incident_org, incident_client, incident_site
          FROM incident i WHERE i.id = NEW.incident_id AND i.organization_id = NEW.organization_id;
        PERFORM app_scope_restore(previous);
        IF incident_org IS NOT NULL AND NEW.client_id IS NOT DISTINCT FROM incident_client
           AND NEW.site_id IS NOT DISTINCT FROM incident_site THEN
            RETURN NEW;
        END IF;
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF current_setting('app.alarm_retention', true) = 'on' AND OLD.at < now() - interval '400 days' THEN
            RETURN OLD;
        END IF;
        IF pg_trigger_depth() > 1 AND NOT EXISTS (
            SELECT 1 FROM incident i WHERE i.id = OLD.incident_id AND i.organization_id = OLD.organization_id) THEN
            RETURN OLD;
        END IF;
    END IF;
    RAISE EXCEPTION 'incident_transition rows are append-only'
        USING ERRCODE = 'restrict_violation';
END
$function$;

CREATE OR REPLACE FUNCTION public.reject_alarm_delete()
 RETURNS trigger
 LANGUAGE plpgsql
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
BEGIN
    -- The purge of an organization (TLC-06, WP-1154): only the role
    -- reticora_org_purger, only for a row of an organization in purging.
    IF TG_OP = 'DELETE' AND public.org_purge_active(OLD.organization_id) THEN
        RETURN OLD;
    END IF;
    IF current_setting('app.alarm_retention', true) = 'on' OR pg_trigger_depth() > 1 THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'alarms are removed only by the retention or with their CI, client or organization'
        USING ERRCODE = 'restrict_violation';
END
$function$;

CREATE OR REPLACE FUNCTION public.reject_incident_delete()
 RETURNS trigger
 LANGUAGE plpgsql
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
BEGIN
    -- The purge of an organization (TLC-06, WP-1154): only the role
    -- reticora_org_purger, only for a row of an organization in purging.
    IF TG_OP = 'DELETE' AND public.org_purge_active(OLD.organization_id) THEN
        RETURN OLD;
    END IF;
    IF current_setting('app.alarm_retention', true) = 'on' OR pg_trigger_depth() > 1 THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'incidents are removed only by the retention or with their client or organization'
        USING ERRCODE = 'restrict_violation';
END
$function$;

CREATE OR REPLACE FUNCTION public.form_def_version_immutable()
 RETURNS trigger
 LANGUAGE plpgsql
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
BEGIN
    -- The purge of an organization (TLC-06, WP-1154): only the role
    -- reticora_org_purger, only for a row of an organization in purging.
    IF TG_OP = 'DELETE' AND public.org_purge_active(OLD.organization_id) THEN
        RETURN OLD;
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF pg_trigger_depth() > 1 THEN
            RETURN OLD;
        END IF;
    ELSIF pg_trigger_depth() > 1
        AND NEW.id = OLD.id AND NEW.organization_id = OLD.organization_id AND NEW.form_id = OLD.form_id
        AND NEW.version = OLD.version AND NEW.schema = OLD.schema AND NEW.ui_hints = OLD.ui_hints
        AND NEW.created_at = OLD.created_at THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'a form definition version cannot be changed'
        USING ERRCODE = 'check_violation', CONSTRAINT = 'form_def_version_immutable';
END
$function$;

CREATE OR REPLACE FUNCTION public.workflow_def_version_immutable()
 RETURNS trigger
 LANGUAGE plpgsql
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
BEGIN
    -- The purge of an organization (TLC-06, WP-1154): only the role
    -- reticora_org_purger, only for a row of an organization in purging.
    IF TG_OP = 'DELETE' AND public.org_purge_active(OLD.organization_id) THEN
        RETURN OLD;
    END IF;
    IF pg_trigger_depth() > 1 THEN
        IF TG_OP = 'DELETE' THEN
            RETURN OLD;
        END IF;
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'a workflow definition version cannot be changed'
        USING ERRCODE = 'check_violation', CONSTRAINT = 'workflow_def_version_immutable';
END
$function$;

CREATE OR REPLACE FUNCTION public.ipam_section_protect_default()
 RETURNS trigger
 LANGUAGE plpgsql
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
BEGIN
    -- The purge of an organization (TLC-06, WP-1154): only the role
    -- reticora_org_purger, only for a row of an organization in purging.
    IF TG_OP = 'DELETE' AND public.org_purge_active(OLD.organization_id) THEN
        RETURN OLD;
    END IF;
    IF OLD.is_default AND pg_trigger_depth() = 1 AND (TG_OP = 'DELETE' OR NOT NEW.is_default) THEN
        RAISE EXCEPTION 'the default IPAM section of an organization cannot be removed'
            USING ERRCODE = '23514', CONSTRAINT = 'ipam_section_default_required';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$function$;

CREATE OR REPLACE FUNCTION public.vrf_protect_default()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
BEGIN
    -- The purge of an organization (TLC-06, WP-1154): only the role
    -- reticora_org_purger, only for a row of an organization in purging.
    IF TG_OP = 'DELETE' AND public.org_purge_active(OLD.organization_id) THEN
        RETURN OLD;
    END IF;
    IF OLD.is_default AND pg_trigger_depth() = 1 AND (TG_OP = 'DELETE' OR NOT NEW.is_default) THEN
        RAISE EXCEPTION 'the default VRF of organization % cannot be removed', OLD.organization_id
            USING ERRCODE = '23514', CONSTRAINT = 'vrf_default_required';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$function$;
