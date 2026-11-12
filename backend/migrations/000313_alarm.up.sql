-- WP-981 (ALR-01, MON-04, E-196 option 2): alarm instances and their
-- state log. alert_rule stays the rule definition; an alarm is one
-- instance per source and series.
--
-- 1. alarm: one row per source (source_kind rule, check, collector, config,
--    capacity, sla; source_id) and series (series_key, labels) with the
--    deduplication key dedup_key = source_kind:source_id:series_key
--    (generated). At most one alarm per key is not resolved (unique partial
--    index): a renewed raise of the key increments occurrence_count instead
--    of opening a second alarm. incident_id carries no foreign key until
--    WP-1027 creates incident; acknowledged_by is the actor id without a
--    foreign key (Konvention 16).
-- 2. Scope: with a CI the trigger alarm_scope takes client and site from
--    the CI (located-scope); without one it keeps the client the caller set
--    (e.g. that of the collector group) and clears the site. The derived
--    site_id carries no foreign key.
-- 3. alarm_transition: one row per state change with actor (NULL = system),
--    reason and comment; organization, client and site come from the alarm
--    (trigger alarm_transition_scope). The table is append-only:
--    reject_alarm_transition_mutation refuses every UPDATE, and a DELETE
--    unless the retention runs (app.alarm_retention = 'on') and the row is
--    older than 400 days, or the alarm itself is gone (the cascade of an
--    alarm deleted with its CI, client or organization, or by the
--    retention). An alarm is deleted only by the retention or by such a
--    cascade (reject_alarm_delete), so its log cannot be dropped through it.
--    The only UPDATE let through is the re-derived client and site of a
--    moved alarm (see 3a).
-- 3a. A CI moved to another client or site carries its alarms along
--    (alarm_scope_propagation on ci, pattern 000261/000295), and an alarm
--    whose client or site changed carries its transitions along
--    (alarm_transition_scope_propagation on alarm), so neither keeps a
--    stale scope.
-- 4. Row level security with ENABLE and FORCE after Konvention 5 (E-09):
--    a restricted principal reads the rows of its clients and sites and
--    those without; it writes only rows inside its scope; restrictive
--    UPDATE and DELETE policies (pattern 000261). Truncate guard on both
--    tables, updated_at on alarm.

-- ─── 1. alarm ─────────────────────────────────────────────────────────────────

CREATE TABLE alarm (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id   uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id         uuid NULL,
    site_id           uuid NULL,
    ci_id             uuid NULL,
    source_kind       text NOT NULL CONSTRAINT alarm_source_kind_check
                          CHECK (source_kind IN ('rule', 'check', 'collector', 'config', 'capacity', 'sla')),
    source_id         uuid NOT NULL,
    series_key        text NOT NULL DEFAULT '' CONSTRAINT alarm_series_key_check CHECK (length(series_key) <= 1000),
    labels            jsonb NOT NULL DEFAULT '{}' CONSTRAINT alarm_labels_check CHECK (jsonb_typeof(labels) = 'object'),
    dedup_key         text GENERATED ALWAYS AS (source_kind || ':' || source_id::text || ':' || series_key) STORED,
    severity          text NOT NULL CONSTRAINT alarm_severity_check CHECK (severity IN ('info', 'warning', 'critical')),
    state             text NOT NULL CONSTRAINT alarm_state_check CHECK (state IN ('pending', 'open', 'acknowledged', 'resolved')),
    suppressed_reason text NULL CONSTRAINT alarm_suppressed_reason_check
                          CHECK (suppressed_reason IN ('maintenance', 'symptom', 'flapping')),
    incident_id       uuid NULL,
    occurrence_count  integer NOT NULL DEFAULT 1 CONSTRAINT alarm_occurrence_count_check CHECK (occurrence_count > 0),
    first_seen_at     timestamptz NOT NULL DEFAULT now(),
    last_seen_at      timestamptz NOT NULL DEFAULT now(),
    opened_at         timestamptz NULL,
    acknowledged_at   timestamptz NULL,
    -- Konvention 16: the acknowledging actor as id, without foreign key.
    acknowledged_by   uuid NULL,
    resolved_at       timestamptz NULL,
    summary           text NOT NULL DEFAULT '' CONSTRAINT alarm_summary_check CHECK (length(summary) <= 500),
    details           jsonb NOT NULL DEFAULT '{}' CONSTRAINT alarm_details_check CHECK (jsonb_typeof(details) = 'object'),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT alarm_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT alarm_resolved_at_check CHECK ((state = 'resolved') = (resolved_at IS NOT NULL)),
    CONSTRAINT alarm_ci_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT alarm_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id) ON DELETE CASCADE
);

-- At most one alarm per deduplication key is not resolved (ALR-01).
CREATE UNIQUE INDEX uq_alarm_dedup_open ON alarm (organization_id, dedup_key) WHERE state <> 'resolved';
CREATE INDEX idx_alarm_org_state ON alarm (organization_id, state, severity, opened_at DESC);
CREATE INDEX idx_alarm_ci_state ON alarm (ci_id, state) WHERE ci_id IS NOT NULL;
CREATE INDEX idx_alarm_org_first_seen ON alarm (organization_id, first_seen_at DESC, id DESC);
CREATE INDEX idx_alarm_org_resolved ON alarm (organization_id, resolved_at) WHERE state = 'resolved';

-- ─── 2. Scope of alarm ───────────────────────────────────────────────────────

-- Client and site of an alarm with a CI are those of the CI, read with the
-- scope widened (000139) so a scoped writer cannot keep a stale scope; the
-- policies check the row afterwards. Without CI the caller's client stays
-- and the site is cleared.
CREATE FUNCTION alarm_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
BEGIN
    IF NEW.ci_id IS NOT NULL THEN
        previous := app_scope_widen();
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
          FROM ci c WHERE c.id = NEW.ci_id AND c.organization_id = NEW.organization_id FOR SHARE;
        PERFORM app_scope_restore(previous);
    ELSE
        NEW.site_id := NULL;
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER alarm_scope BEFORE INSERT OR UPDATE OF ci_id, client_id, site_id, organization_id ON alarm
    FOR EACH ROW EXECUTE FUNCTION alarm_scope();
CREATE TRIGGER trg_alarm_updated_at BEFORE UPDATE ON alarm
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON alarm
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- An alarm goes only with the retention (app.alarm_retention) or with its
-- CI, client or organization (a cascade runs inside the trigger of the
-- foreign key, pg_trigger_depth() > 1): a direct DELETE would drop the
-- append-only log of its transitions.
CREATE FUNCTION reject_alarm_delete() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF current_setting('app.alarm_retention', true) = 'on' OR pg_trigger_depth() > 1 THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'alarms are removed only by the retention or with their CI, client or organization'
        USING ERRCODE = 'restrict_violation';
END
$$;
CREATE TRIGGER alarm_delete_guard BEFORE DELETE ON alarm
    FOR EACH ROW EXECUTE FUNCTION reject_alarm_delete();

-- ─── 3. alarm_transition ─────────────────────────────────────────────────────

CREATE TABLE alarm_transition (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       uuid NULL,
    site_id         uuid NULL,
    alarm_id        uuid NOT NULL,
    from_state      text NULL CONSTRAINT alarm_transition_from_state_check
                        CHECK (from_state IN ('pending', 'open', 'acknowledged', 'resolved')),
    to_state        text NOT NULL CONSTRAINT alarm_transition_to_state_check
                        CHECK (to_state IN ('pending', 'open', 'acknowledged', 'resolved')),
    from_severity   text NULL CONSTRAINT alarm_transition_from_severity_check
                        CHECK (from_severity IN ('info', 'warning', 'critical')),
    to_severity     text NULL CONSTRAINT alarm_transition_to_severity_check
                        CHECK (to_severity IN ('info', 'warning', 'critical')),
    at              timestamptz NOT NULL DEFAULT now(),
    -- Konvention 16: the actor as id without foreign key; NULL is the system.
    actor_id        uuid NULL,
    reason          text NOT NULL DEFAULT '' CONSTRAINT alarm_transition_reason_check CHECK (length(reason) <= 200),
    comment         text NULL CONSTRAINT alarm_transition_comment_check CHECK (length(comment) <= 1000),
    CONSTRAINT alarm_transition_alarm_fkey FOREIGN KEY (alarm_id, organization_id)
        REFERENCES alarm (id, organization_id) ON DELETE CASCADE
);
CREATE INDEX idx_alarm_transition_alarm ON alarm_transition (organization_id, alarm_id, at);

-- Organization, client and site of a transition are those of its alarm
-- (pattern 000059 for child tables), read with the scope widened; the
-- policies check the row afterwards.
CREATE FUNCTION alarm_transition_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    found_org uuid;
BEGIN
    previous := app_scope_widen();
    SELECT a.organization_id, a.client_id, a.site_id INTO found_org, NEW.client_id, NEW.site_id
      FROM alarm a WHERE a.id = NEW.alarm_id AND a.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    IF found_org IS NOT NULL THEN
        NEW.organization_id := found_org;
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER alarm_transition_scope BEFORE INSERT ON alarm_transition
    FOR EACH ROW EXECUTE FUNCTION alarm_transition_scope();

-- Append-only (ALR-01; pattern reject_audit_log_mutation of 000188): no
-- UPDATE except the propagated scope of section 3a; a DELETE only inside
-- the retention for rows older than 400 days, or as the cascade of an
-- alarm that is already gone (reject_alarm_delete guards the alarm).
CREATE FUNCTION reject_alarm_transition_mutation() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
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
$$;
CREATE TRIGGER reject_alarm_transition_mutation BEFORE UPDATE OR DELETE ON alarm_transition
    FOR EACH ROW EXECUTE FUNCTION reject_alarm_transition_mutation();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON alarm_transition
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── 3a. Propagation of a moved CI ───────────────────────────────────────────

-- A CI moved to another client or site re-derives the scope of its alarms
-- (a no-op UPDATE of ci_id re-runs alarm_scope), read and written with the
-- scope widened (000139) so a scoped writer of the CI leaves no stale
-- scope behind; the policies check the rows afterwards.
CREATE FUNCTION propagate_alarm_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE alarm SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER alarm_scope_propagation AFTER UPDATE ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_alarm_scope();

-- An alarm whose client or site changed carries its transitions along;
-- reject_alarm_transition_mutation lets exactly this update through.
CREATE FUNCTION propagate_alarm_transition_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE alarm_transition SET client_id = NEW.client_id, site_id = NEW.site_id
     WHERE alarm_id = NEW.id AND organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER alarm_transition_scope_propagation AFTER UPDATE ON alarm
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_alarm_transition_scope();

-- ─── 4. Row level security ───────────────────────────────────────────────────

ALTER TABLE alarm ENABLE ROW LEVEL SECURITY;
ALTER TABLE alarm FORCE ROW LEVEL SECURITY;
ALTER TABLE alarm_transition ENABLE ROW LEVEL SECURITY;
ALTER TABLE alarm_transition FORCE ROW LEVEL SECURITY;

-- Konvention 5 (E-09): a restricted principal reads the rows of its clients
-- and sites and those without; it writes only rows inside its scope,
-- org-wide rows only without client and site restriction. No policy uses
-- EXISTS.
DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_match CONSTANT text := 'site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[])';
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['alarm', 'alarm_transition'] LOOP
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
