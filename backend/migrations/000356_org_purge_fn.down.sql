-- Migration 000356 down: the guards as before, without purge functions,
-- rights and role.

CREATE OR REPLACE FUNCTION public.reject_audit_log_mutation()
 RETURNS trigger
 LANGUAGE plpgsql
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE
    refs TEXT[];
BEGIN
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

DROP FUNCTION IF EXISTS org_purge_batch(uuid, regclass, int);
DROP FUNCTION IF EXISTS org_purge_keys(uuid, regclass);
DROP FUNCTION IF EXISTS org_purge_rows(uuid, regclass);
DROP FUNCTION IF EXISTS org_purge_check(uuid, regclass);
DROP FUNCTION IF EXISTS org_purge_target(regclass);
DROP FUNCTION IF EXISTS org_purge_hypertable(regclass);
DROP FUNCTION IF EXISTS org_purge_active(uuid);

ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM reticora_org_purger;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE USAGE, SELECT ON SEQUENCES FROM reticora_org_purger;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE EXECUTE ON FUNCTIONS FROM reticora_org_purger;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'reticora_org_purger') THEN
        DROP OWNED BY reticora_org_purger;
        BEGIN
            DROP ROLE reticora_org_purger;
        EXCEPTION
            WHEN dependent_objects_still_exist THEN NULL; -- still used by another database
        END;
    END IF;
END
$$;
