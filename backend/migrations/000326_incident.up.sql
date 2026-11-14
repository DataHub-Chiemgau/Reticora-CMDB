-- WP-1027 (ALR-03, ALR-09, E-196 option 2): the incident bundles
-- correlated alarms with root, cause and confidence.
--
-- 1. incident: root alarm and root CI (both optional), cause class
--    (topology, collector_unreachable, unknown; WP-1041 adds further
--    classes), confidence 0–100, reasons (array of lines), state open,
--    acknowledged, resolved, manual_root (a root set by hand survives the
--    automatic re-determination), affected_counts. acknowledged_by is the
--    actor id without foreign key (Konvention 16).
-- 2. Scope: with a root CI the trigger incident_scope takes client and site
--    from the CI (located-scope), without one the caller's client and site
--    stay. A CI moved to another client or site carries its incidents
--    along (incident_scope_propagation, pattern 000313).
-- 3. alarm.incident_id gets its foreign key (ON DELETE SET NULL of the
--    column): an alarm of a deleted incident keeps its own retention.
-- 4. incident_transition: one row per change (opened, root_changed,
--    merged_into, acknowledged, resolved, …) with actor (NULL = system),
--    reason and details; organization, client and site come from the
--    incident. Append-only: reject_incident_transition_mutation refuses
--    every UPDATE except the propagated scope, and a DELETE unless the
--    retention runs (app.alarm_retention = 'on') for rows older than 400
--    days, or the incident itself is gone (cascade). An incident is
--    deleted only by the retention or with its client or organization
--    (reject_incident_delete).
-- 5. Row level security with ENABLE and FORCE after Konvention 5 (E-09),
--    restrictive UPDATE and DELETE policies, truncate guard on both
--    tables, updated_at on incident.

-- ─── 1. incident ──────────────────────────────────────────────────────────────

CREATE TABLE incident (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       uuid NULL,
    site_id         uuid NULL,
    root_alarm_id   uuid NULL,
    root_ci_id      uuid NULL,
    cause_class     text NOT NULL DEFAULT 'unknown' CONSTRAINT incident_cause_class_check
                        CHECK (cause_class IN ('topology', 'collector_unreachable', 'unknown')),
    confidence      smallint NOT NULL DEFAULT 0 CONSTRAINT incident_confidence_check CHECK (confidence BETWEEN 0 AND 100),
    reasons         jsonb NOT NULL DEFAULT '[]' CONSTRAINT incident_reasons_check CHECK (jsonb_typeof(reasons) = 'array'),
    state           text NOT NULL DEFAULT 'open' CONSTRAINT incident_state_check
                        CHECK (state IN ('open', 'acknowledged', 'resolved')),
    opened_at       timestamptz NOT NULL DEFAULT now(),
    acknowledged_at timestamptz NULL,
    -- Konvention 16: the acknowledging actor as id, without foreign key.
    acknowledged_by uuid NULL,
    resolved_at     timestamptz NULL,
    manual_root     boolean NOT NULL DEFAULT false,
    affected_counts jsonb NOT NULL DEFAULT '{}' CONSTRAINT incident_affected_counts_check
                        CHECK (jsonb_typeof(affected_counts) = 'object'),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT incident_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT incident_resolved_at_check CHECK ((state = 'resolved') = (resolved_at IS NOT NULL)),
    CONSTRAINT incident_root_alarm_fkey FOREIGN KEY (root_alarm_id, organization_id)
        REFERENCES alarm (id, organization_id) ON DELETE SET NULL (root_alarm_id),
    CONSTRAINT incident_root_ci_fkey FOREIGN KEY (root_ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE SET NULL (root_ci_id),
    CONSTRAINT incident_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT incident_site_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id) ON DELETE SET NULL (site_id)
);

CREATE INDEX idx_incident_org_state ON incident (organization_id, state, opened_at DESC);
CREATE INDEX idx_incident_org_opened ON incident (organization_id, opened_at DESC, id DESC);
CREATE INDEX idx_incident_org_resolved ON incident (organization_id, resolved_at) WHERE state = 'resolved';
CREATE INDEX idx_incident_root_alarm ON incident (root_alarm_id) WHERE root_alarm_id IS NOT NULL;
CREATE INDEX idx_incident_root_ci ON incident (root_ci_id) WHERE root_ci_id IS NOT NULL;

-- ─── 2. Scope of incident ────────────────────────────────────────────────────

-- Client and site of an incident with a root CI are those of the CI, read
-- with the scope widened (000139); the policies check the row afterwards.
-- Without root CI the caller's client and site stay.
CREATE FUNCTION incident_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
BEGIN
    IF NEW.root_ci_id IS NOT NULL THEN
        previous := app_scope_widen();
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
          FROM ci c WHERE c.id = NEW.root_ci_id AND c.organization_id = NEW.organization_id FOR SHARE;
        PERFORM app_scope_restore(previous);
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER incident_scope BEFORE INSERT OR UPDATE OF root_ci_id, client_id, site_id, organization_id ON incident
    FOR EACH ROW EXECUTE FUNCTION incident_scope();
CREATE TRIGGER trg_incident_updated_at BEFORE UPDATE ON incident
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON incident
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- An incident goes only with the retention (app.alarm_retention) or with
-- its client or organization (cascade, pg_trigger_depth() > 1): a direct
-- DELETE would drop the append-only log of its transitions.
CREATE FUNCTION reject_incident_delete() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF current_setting('app.alarm_retention', true) = 'on' OR pg_trigger_depth() > 1 THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'incidents are removed only by the retention or with their client or organization'
        USING ERRCODE = 'restrict_violation';
END
$$;
CREATE TRIGGER incident_delete_guard BEFORE DELETE ON incident
    FOR EACH ROW EXECUTE FUNCTION reject_incident_delete();

-- ─── 3. alarm.incident_id ────────────────────────────────────────────────────

ALTER TABLE alarm ADD CONSTRAINT alarm_incident_fkey FOREIGN KEY (incident_id, organization_id)
    REFERENCES incident (id, organization_id) ON DELETE SET NULL (incident_id);
CREATE INDEX idx_alarm_incident ON alarm (incident_id) WHERE incident_id IS NOT NULL;

-- ─── 4. incident_transition ──────────────────────────────────────────────────

CREATE TABLE incident_transition (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       uuid NULL,
    site_id         uuid NULL,
    incident_id     uuid NOT NULL,
    from_state      text NULL CONSTRAINT incident_transition_from_state_check
                        CHECK (from_state IN ('open', 'acknowledged', 'resolved')),
    to_state        text NOT NULL CONSTRAINT incident_transition_to_state_check
                        CHECK (to_state IN ('open', 'acknowledged', 'resolved')),
    at              timestamptz NOT NULL DEFAULT now(),
    -- Konvention 16: the actor as id without foreign key; NULL is the system.
    actor_id        uuid NULL,
    reason          text NOT NULL CONSTRAINT incident_transition_reason_check CHECK (length(reason) BETWEEN 1 AND 200),
    details         jsonb NOT NULL DEFAULT '{}' CONSTRAINT incident_transition_details_check
                        CHECK (jsonb_typeof(details) = 'object'),
    CONSTRAINT incident_transition_incident_fkey FOREIGN KEY (incident_id, organization_id)
        REFERENCES incident (id, organization_id) ON DELETE CASCADE
);
CREATE INDEX idx_incident_transition_incident ON incident_transition (organization_id, incident_id, at);

-- Organization, client and site of a transition are those of its incident
-- (pattern 000059 for child tables), read with the scope widened.
CREATE FUNCTION incident_transition_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    found_org uuid;
BEGIN
    previous := app_scope_widen();
    SELECT i.organization_id, i.client_id, i.site_id INTO found_org, NEW.client_id, NEW.site_id
      FROM incident i WHERE i.id = NEW.incident_id AND i.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    IF found_org IS NOT NULL THEN
        NEW.organization_id := found_org;
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER incident_transition_scope BEFORE INSERT ON incident_transition
    FOR EACH ROW EXECUTE FUNCTION incident_transition_scope();

-- Append-only (ALR-01; pattern reject_alarm_transition_mutation of
-- 000313): no UPDATE except the propagated scope of a moved incident; a
-- DELETE only inside the retention for rows older than 400 days, or as the
-- cascade of an incident that is already gone.
CREATE FUNCTION reject_incident_transition_mutation() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
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
$$;
CREATE TRIGGER reject_incident_transition_mutation BEFORE UPDATE OR DELETE ON incident_transition
    FOR EACH ROW EXECUTE FUNCTION reject_incident_transition_mutation();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON incident_transition
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── 4a. Propagation of a moved root CI ──────────────────────────────────────

CREATE FUNCTION propagate_incident_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE incident SET root_ci_id = root_ci_id WHERE root_ci_id = NEW.id AND organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER incident_scope_propagation AFTER UPDATE ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_incident_scope();

CREATE FUNCTION propagate_incident_transition_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE incident_transition SET client_id = NEW.client_id, site_id = NEW.site_id
     WHERE incident_id = NEW.id AND organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER incident_transition_scope_propagation AFTER UPDATE ON incident
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_incident_transition_scope();

-- ─── 5. Row level security ───────────────────────────────────────────────────

ALTER TABLE incident ENABLE ROW LEVEL SECURITY;
ALTER TABLE incident FORCE ROW LEVEL SECURITY;
ALTER TABLE incident_transition ENABLE ROW LEVEL SECURITY;
ALTER TABLE incident_transition FORCE ROW LEVEL SECURITY;

-- Konvention 5 (E-09), as for alarm (000313).
DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_match CONSTANT text := 'site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[])';
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['incident', 'incident_transition'] LOOP
        EXECUTE format('CREATE POLICY %I ON %I USING (%s AND (%s OR client_id IS NULL OR %s) AND (%s OR site_id IS NULL OR %s)) '
            || 'WITH CHECK (%s AND (%s OR %s) AND (%s OR %s))',
            t || '_isolation', t, org_match, scope_all, scope_match, site_all, site_match,
            org_match, scope_all, scope_match, site_all, site_match);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR UPDATE USING (%s AND (%s OR %s) AND (%s OR %s)) '
            || 'WITH CHECK (%s AND (%s OR %s) AND (%s OR %s))',
            t || '_scoped_update', t, org_match, scope_all, scope_match, site_all, site_match,
            org_match, scope_all, scope_match, site_all, site_match);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR DELETE USING (%s AND (%s OR %s) AND (%s OR %s))',
            t || '_scoped_delete', t, org_match, scope_all, scope_match, site_all, site_match);
    END LOOP;
END
$$;
