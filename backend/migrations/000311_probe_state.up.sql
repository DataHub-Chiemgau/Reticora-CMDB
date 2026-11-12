-- Migration 000311: probe state, state history and ingest cursor (PRB-05,
-- PRB-07, WP-944, E-175, E-180).
--
-- 1. probe_state(probe PK, organization, client, site, state, reason,
--    since, last_result_at, last_seq, epoch, collector NULL,
--    consecutive_fail, consecutive_ok, updated_at): the current state of a
--    probe (up, degraded, down, unknown). site_id is carried so the site
--    scope works without a join on probe.
-- 2. probe_state_change(id, organization, client, site, probe, ci NULL,
--    from_state, to_state, reason, observed_at, received_at, epoch, seq,
--    collector NULL, late, notify, root_event_id NULL): every state change.
--    observed_at is the business time of the collector (E-175), received_at
--    the arrival on the server. UNIQUE (probe_id, collector_id, epoch, seq)
--    is the idempotency key (PRB-07): a second delivery with ON CONFLICT DO
--    NOTHING adds no row. late marks a change delivered after the state had
--    ended, notify whether it may notify (E-180; set by WP-961). The
--    history is kept 400 days by the job of WP-974.
-- 3. probe_ingest_cursor(organization, collector, stream state|results,
--    epoch, last_seq, updated_at), PRIMARY KEY (collector_id, stream): the
--    position of the ingest per collector and stream. No API route reads
--    it; the ingest worker finds cursors across organizations with the
--    system flag (SELECT only, pattern alert_rule_system_select, 000060).
-- 4. Client and site of probe_state and probe_state_change are those of
--    the probe (trigger probe_state_scope, read with the scope widened,
--    000139), as is the CI of a change: an insert without CI takes the
--    probe's CI, another CI is refused (23514
--    probe_state_change_ci_of_probe), an update keeps the recorded CI or
--    clears it; a probe with a new client or site re-derives them. collector_id references collector with
--    (collector_id, organization_id) ON DELETE SET NULL (collector_id);
--    collector_id_organization_key is created conditionally (same name as
--    in 000308). Every reference carries organization_id (Konvention 19).
-- 5. ENABLE and FORCE RLS: probe_state and probe_state_change with the
--    client and site policies of Konvention 5 (E-09, pattern 000308),
--    probe_ingest_cursor with the organization policy; truncate guard on
--    all three, set_updated_at on probe_state and probe_ingest_cursor.
--    Normal tables, no hypertables.

-- ─── collector key ────────────────────────────────────────────────────────────

-- collector-key:begin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'collector'::regclass AND conname = 'collector_id_organization_key') THEN
        ALTER TABLE collector ADD CONSTRAINT collector_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
-- collector-key:end

-- ─── 1. probe_state ───────────────────────────────────────────────────────────

CREATE TABLE probe_state (
    probe_id         uuid PRIMARY KEY,
    organization_id  uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id        uuid NULL,
    site_id          uuid NULL,
    state            text NOT NULL CONSTRAINT probe_state_state_check
                         CHECK (state IN ('up', 'degraded', 'down', 'unknown')),
    reason           text NOT NULL DEFAULT '' CONSTRAINT probe_state_reason_check CHECK (length(reason) <= 200),
    since            timestamptz NOT NULL,
    last_result_at   timestamptz NULL,
    last_seq         bigint NOT NULL DEFAULT 0 CONSTRAINT probe_state_last_seq_check CHECK (last_seq >= 0),
    epoch            bigint NOT NULL DEFAULT 0 CONSTRAINT probe_state_epoch_check CHECK (epoch >= 0),
    collector_id     uuid NULL,
    consecutive_fail integer NOT NULL DEFAULT 0 CONSTRAINT probe_state_consecutive_fail_check CHECK (consecutive_fail >= 0),
    consecutive_ok   integer NOT NULL DEFAULT 0 CONSTRAINT probe_state_consecutive_ok_check CHECK (consecutive_ok >= 0),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT probe_state_probe_fkey FOREIGN KEY (probe_id, organization_id)
        REFERENCES probe (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT probe_state_collector_fkey FOREIGN KEY (collector_id, organization_id)
        REFERENCES collector (id, organization_id) ON DELETE SET NULL (collector_id),
    CONSTRAINT probe_state_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id)
);
CREATE INDEX idx_probe_state_org_state ON probe_state (organization_id, state);
CREATE INDEX idx_probe_state_collector ON probe_state (organization_id, collector_id) WHERE collector_id IS NOT NULL;
CREATE TRIGGER trg_probe_state_updated_at BEFORE UPDATE ON probe_state
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON probe_state
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── 2. probe_state_change ────────────────────────────────────────────────────

CREATE TABLE probe_state_change (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       uuid NULL,
    site_id         uuid NULL,
    probe_id        uuid NOT NULL,
    ci_id           uuid NULL,
    from_state      text NOT NULL CONSTRAINT probe_state_change_from_state_check
                        CHECK (from_state IN ('up', 'degraded', 'down', 'unknown')),
    to_state        text NOT NULL CONSTRAINT probe_state_change_to_state_check
                        CHECK (to_state IN ('up', 'degraded', 'down', 'unknown')),
    reason          text NOT NULL DEFAULT '' CONSTRAINT probe_state_change_reason_check CHECK (length(reason) <= 200),
    observed_at     timestamptz NOT NULL,
    received_at     timestamptz NOT NULL DEFAULT now(),
    epoch           bigint NOT NULL CONSTRAINT probe_state_change_epoch_check CHECK (epoch >= 0),
    seq             bigint NOT NULL CONSTRAINT probe_state_change_seq_check CHECK (seq >= 0),
    collector_id    uuid NULL,
    late            boolean NOT NULL DEFAULT false,
    notify          boolean NOT NULL DEFAULT true,
    root_event_id   uuid NULL,
    CONSTRAINT probe_state_change_idempotency_key UNIQUE (probe_id, collector_id, epoch, seq),
    CONSTRAINT probe_state_change_probe_fkey FOREIGN KEY (probe_id, organization_id)
        REFERENCES probe (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT probe_state_change_ci_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE SET NULL (ci_id),
    CONSTRAINT probe_state_change_collector_fkey FOREIGN KEY (collector_id, organization_id)
        REFERENCES collector (id, organization_id) ON DELETE SET NULL (collector_id),
    CONSTRAINT probe_state_change_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id)
);
CREATE INDEX idx_probe_state_change_probe_observed ON probe_state_change (probe_id, observed_at DESC);
CREATE INDEX idx_probe_state_change_org_observed ON probe_state_change (organization_id, observed_at);
CREATE INDEX idx_probe_state_change_ci ON probe_state_change (organization_id, ci_id) WHERE ci_id IS NOT NULL;
CREATE INDEX idx_probe_state_change_collector ON probe_state_change (organization_id, collector_id) WHERE collector_id IS NOT NULL;
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON probe_state_change
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- Client and site of a state and a state change are those of the probe
-- (the parent row), read with the scope widened (000139) so a scoped
-- writer cannot keep a stale scope; the policies check the row afterwards.
-- A change carries the probe's CI; another CI is refused.
CREATE FUNCTION probe_state_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    probe_ci uuid;
BEGIN
    previous := app_scope_widen();
    SELECT p.client_id, p.site_id, p.ci_id INTO NEW.client_id, NEW.site_id, probe_ci
      FROM probe p WHERE p.id = NEW.probe_id AND p.organization_id = NEW.organization_id FOR SHARE;
    PERFORM app_scope_restore(previous);
    -- Nested: probe_state has no ci_id, and plpgsql resolves NEW.ci_id
    -- only when the inner statement runs. A change belongs to the CI of its
    -- probe: an insert takes it (another CI is refused before the foreign
    -- key runs, so the answer does not tell whether that CI exists); an
    -- update keeps the CI recorded at the time or clears it (ON DELETE SET
    -- NULL after the probe moved to another CI).
    IF TG_TABLE_NAME = 'probe_state_change' THEN
        IF TG_OP = 'INSERT' THEN
            IF NEW.ci_id IS NULL THEN
                NEW.ci_id := probe_ci;
            ELSIF NEW.ci_id IS DISTINCT FROM probe_ci THEN
                RAISE EXCEPTION 'probe_state_change: ci_id must be the CI of the probe'
                    USING ERRCODE = 'check_violation', CONSTRAINT = 'probe_state_change_ci_of_probe';
            END IF;
        ELSIF NEW.ci_id IS NOT NULL AND NEW.ci_id IS DISTINCT FROM OLD.ci_id THEN
            RAISE EXCEPTION 'probe_state_change: ci_id cannot be changed'
                USING ERRCODE = 'check_violation', CONSTRAINT = 'probe_state_change_ci_of_probe';
        END IF;
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER probe_state_scope BEFORE INSERT OR UPDATE ON probe_state
    FOR EACH ROW EXECUTE FUNCTION probe_state_scope();
CREATE TRIGGER probe_state_scope BEFORE INSERT OR UPDATE ON probe_state_change
    FOR EACH ROW EXECUTE FUNCTION probe_state_scope();

-- A probe with a new client or site re-derives its state and history.
CREATE FUNCTION propagate_probe_state_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE probe_state SET probe_id = probe_id WHERE probe_id = NEW.id AND organization_id = NEW.organization_id;
    UPDATE probe_state_change SET probe_id = probe_id WHERE probe_id = NEW.id AND organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_probe_state_scope
    AFTER UPDATE ON probe
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_probe_state_scope();

-- ─── 3. probe_ingest_cursor ───────────────────────────────────────────────────

CREATE TABLE probe_ingest_cursor (
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    collector_id    uuid NOT NULL,
    stream          text NOT NULL CONSTRAINT probe_ingest_cursor_stream_check CHECK (stream IN ('state', 'results')),
    epoch           bigint NOT NULL DEFAULT 0 CONSTRAINT probe_ingest_cursor_epoch_check CHECK (epoch >= 0),
    last_seq        bigint NOT NULL DEFAULT 0 CONSTRAINT probe_ingest_cursor_last_seq_check CHECK (last_seq >= 0),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (collector_id, stream),
    CONSTRAINT probe_ingest_cursor_collector_fkey FOREIGN KEY (collector_id, organization_id)
        REFERENCES collector (id, organization_id) ON DELETE CASCADE
);
CREATE INDEX idx_probe_ingest_cursor_org ON probe_ingest_cursor (organization_id);
CREATE TRIGGER trg_probe_ingest_cursor_updated_at BEFORE UPDATE ON probe_ingest_cursor
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON probe_ingest_cursor
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── 4. Row level security ────────────────────────────────────────────────────

ALTER TABLE probe_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE probe_state FORCE ROW LEVEL SECURITY;
ALTER TABLE probe_state_change ENABLE ROW LEVEL SECURITY;
ALTER TABLE probe_state_change FORCE ROW LEVEL SECURITY;
ALTER TABLE probe_ingest_cursor ENABLE ROW LEVEL SECURITY;
ALTER TABLE probe_ingest_cursor FORCE ROW LEVEL SECURITY;

-- Konvention 5 (E-09): a restricted principal reads the rows of its clients
-- and sites and those without; it writes only rows inside its scope,
-- org-wide rows only without client and site restriction (pattern 000308).
DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_match CONSTANT text := 'site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[])';
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['probe_state', 'probe_state_change'] LOOP
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

-- The cursor belongs to the organization; the ingest worker finds cursors
-- across organizations with the system flag, which only reads (000060).
CREATE POLICY probe_ingest_cursor_isolation ON probe_ingest_cursor FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
CREATE POLICY probe_ingest_cursor_system_select ON probe_ingest_cursor FOR SELECT
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid
           OR current_setting('app.system', true) = 'on');
