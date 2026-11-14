-- Migration 000308: collector groups, their members, the probe assignments
-- and the collector group of every probe (PRB-03, PRB-11, COL-06, WP-936,
-- E-174, E-176).
--
-- 1. collector gets UNIQUE (id, organization_id) for composite foreign keys
--    (Konvention 19), only when the key is missing.
-- 2. collector_group(organization, client NULL, site NULL, name, mode
--    distributed|failover, liveness_timeout 20-300 s (default 30 s, E-176),
--    anchor_probe NULL, status reachable|unreachable): the collectors of a
--    customer network that share the probes of the group (E-174). A site
--    belongs to the group's client.
-- 3. collector_group_member(group, collector, priority, weight, status,
--    last alive, clock offset, capacity hint, config_version): a collector
--    is in exactly one group, its client equals the group's (org-wide group
--    only with collectors without client, 23514
--    collector_group_member_client_rule); client and site of the row are the
--    group's (trigger collector_group_member_scope).
-- 4. probe_assignment(probe, collector, epoch, assigned_at): the member a
--    probe is assigned to under an epoch (fencing token, PRB-11); client
--    and site are the probe's (trigger probe_assignment_scope). The
--    collector must be a member of the probe's collector group (23514
--    probe_assignment_member_rule; PRB-03: probes run only on collectors of
--    the customer network). An assignment goes when its probe changes the
--    collector group or its collector leaves the group.
--    probe.collector_group_id NOT NULL: the group of a probe is its client's
--    or an org-wide group (trigger probe_collector_group_client). The probe
--    table is empty at this point, so the column needs no backfill.
-- 5. Every existing collector gets a group of its own (name, client and
--    site of the collector, mode distributed) with itself as member; the
--    trigger collector_default_group does the same for every new collector.
--    collector.name has no length or blank check: the group name is the
--    collector's name trimmed to 200 characters, 'collector <id>' when that
--    is blank (collector_group_name).
--    The single member of a group carries it along when it moves to another
--    client or site; a group whose client or site changes re-derives its
--    members and re-checks its probes.
-- 6. ENABLE and FORCE RLS, policies after Konvention 5 over client and site
--    (no EXISTS), truncate guard.
-- The scope triggers read their parent row FOR SHARE (as in 000307): a
-- concurrent change of its client, site or group waits for the insert, or
-- the insert waits for it and reads the new values.

-- ─── 1. collector key ─────────────────────────────────────────────────────────

-- collector-key:begin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'collector'::regclass AND conname = 'collector_id_organization_key') THEN
        ALTER TABLE collector ADD CONSTRAINT collector_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
-- collector-key:end

-- ─── 2. collector_group ───────────────────────────────────────────────────────

CREATE TABLE collector_group (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id   uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id         uuid NULL,
    site_id           uuid NULL,
    name              text NOT NULL CONSTRAINT collector_group_name_check CHECK (btrim(name) <> '' AND length(name) <= 200),
    mode              text NOT NULL DEFAULT 'distributed' CONSTRAINT collector_group_mode_check
                          CHECK (mode IN ('distributed', 'failover')),
    liveness_timeout  interval NOT NULL DEFAULT '30 seconds' CONSTRAINT collector_group_liveness_timeout_check
                          CHECK (liveness_timeout BETWEEN '20 seconds' AND '300 seconds'),
    anchor_probe_id   uuid NULL,
    status            text NOT NULL DEFAULT 'reachable' CONSTRAINT collector_group_status_check
                          CHECK (status IN ('reachable', 'unreachable')),
    status_changed_at timestamptz NOT NULL DEFAULT now(),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT collector_group_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT collector_group_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id),
    CONSTRAINT collector_group_site_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id),
    CONSTRAINT collector_group_anchor_probe_fkey FOREIGN KEY (anchor_probe_id, organization_id)
        REFERENCES probe (id, organization_id) ON DELETE SET NULL (anchor_probe_id)
);
CREATE INDEX idx_collector_group_client ON collector_group (organization_id, client_id) WHERE client_id IS NOT NULL;
CREATE INDEX idx_collector_group_anchor ON collector_group (organization_id, anchor_probe_id) WHERE anchor_probe_id IS NOT NULL;

-- A site of a group belongs to the group's client.
CREATE FUNCTION collector_group_site_client() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    site_client uuid;
BEGIN
    IF NEW.site_id IS NULL THEN
        RETURN NEW;
    END IF;
    previous := app_scope_widen();
    SELECT s.client_id INTO site_client FROM site s WHERE s.id = NEW.site_id AND s.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    IF site_client IS NOT NULL AND site_client IS DISTINCT FROM NEW.client_id THEN
        RAISE EXCEPTION 'the site of a collector group belongs to the group''s client'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'collector_group_site_client';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER collector_group_site_client BEFORE INSERT OR UPDATE OF client_id, site_id ON collector_group
    FOR EACH ROW EXECUTE FUNCTION collector_group_site_client();
CREATE TRIGGER trg_collector_group_updated_at BEFORE UPDATE ON collector_group
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON collector_group
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── 3. collector_group_member ────────────────────────────────────────────────

CREATE TABLE collector_group_member (
    group_id        uuid NOT NULL,
    collector_id    uuid NOT NULL,
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       uuid NULL,
    site_id         uuid NULL,
    priority        integer NOT NULL DEFAULT 0,
    weight          integer NOT NULL DEFAULT 100 CONSTRAINT collector_group_member_weight_check CHECK (weight > 0),
    member_status   text NOT NULL DEFAULT 'healthy' CONSTRAINT collector_group_member_status_check
                        CHECK (member_status IN ('healthy', 'degraded', 'down')),
    last_alive_at   timestamptz NULL,
    clock_offset_ms integer NULL,
    capacity_hint   integer NULL,
    config_version  bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (group_id, collector_id),
    -- A collector is in exactly one group.
    CONSTRAINT collector_group_member_collector_key UNIQUE (collector_id),
    CONSTRAINT collector_group_member_group_fkey FOREIGN KEY (group_id, organization_id)
        REFERENCES collector_group (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT collector_group_member_collector_fkey FOREIGN KEY (collector_id, organization_id)
        REFERENCES collector (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT collector_group_member_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id),
    CONSTRAINT collector_group_member_site_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id)
);
CREATE INDEX idx_collector_group_member_org_collector ON collector_group_member (organization_id, collector_id);

-- Client and site of a member are those of its group (the parent row); the
-- collector's client must be the group's.
CREATE FUNCTION collector_group_member_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    collector_client uuid;
BEGIN
    previous := app_scope_widen();
    SELECT g.client_id, g.site_id INTO NEW.client_id, NEW.site_id
      FROM collector_group g WHERE g.id = NEW.group_id AND g.organization_id = NEW.organization_id FOR SHARE;
    SELECT c.client_id INTO collector_client
      FROM collector c WHERE c.id = NEW.collector_id AND c.organization_id = NEW.organization_id FOR SHARE;
    PERFORM app_scope_restore(previous);
    IF collector_client IS DISTINCT FROM NEW.client_id THEN
        RAISE EXCEPTION 'a collector joins only a group of its own client'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'collector_group_member_client_rule';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER collector_group_member_scope BEFORE INSERT OR UPDATE ON collector_group_member
    FOR EACH ROW EXECUTE FUNCTION collector_group_member_scope();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON collector_group_member
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── 4. probe_assignment and probe.collector_group_id ─────────────────────────

CREATE TABLE probe_assignment (
    probe_id        uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       uuid NULL,
    site_id         uuid NULL,
    collector_id    uuid NOT NULL,
    epoch           bigint NOT NULL CONSTRAINT probe_assignment_epoch_check CHECK (epoch > 0),
    assigned_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT probe_assignment_probe_fkey FOREIGN KEY (probe_id, organization_id)
        REFERENCES probe (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT probe_assignment_collector_fkey FOREIGN KEY (collector_id, organization_id)
        REFERENCES collector (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT probe_assignment_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id),
    CONSTRAINT probe_assignment_site_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id)
);
CREATE INDEX idx_probe_assignment_org_collector ON probe_assignment (organization_id, collector_id);

-- Client and site of an assignment are those of its probe (the parent row);
-- the collector must be a member of the probe's collector group. The
-- probe's group column is added below; the function body is resolved at
-- run time.
CREATE FUNCTION probe_assignment_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    probe_group uuid;
    is_member boolean;
BEGIN
    previous := app_scope_widen();
    SELECT p.client_id, p.site_id, p.collector_group_id INTO NEW.client_id, NEW.site_id, probe_group
      FROM probe p WHERE p.id = NEW.probe_id AND p.organization_id = NEW.organization_id FOR SHARE;
    PERFORM 1 FROM collector_group_member m
     WHERE m.collector_id = NEW.collector_id AND m.group_id = probe_group AND m.organization_id = NEW.organization_id
       FOR SHARE;
    is_member := FOUND;
    -- An unknown probe or collector is left to the foreign keys (23503).
    IF NOT is_member AND NOT EXISTS (SELECT 1 FROM collector c WHERE c.id = NEW.collector_id AND c.organization_id = NEW.organization_id) THEN
        probe_group := NULL;
    END IF;
    PERFORM app_scope_restore(previous);
    IF probe_group IS NOT NULL AND NOT is_member THEN
        RAISE EXCEPTION 'a probe is assigned only to a member of its collector group'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'probe_assignment_member_rule';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER probe_assignment_scope BEFORE INSERT OR UPDATE ON probe_assignment
    FOR EACH ROW EXECUTE FUNCTION probe_assignment_scope();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON probe_assignment
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE probe ADD COLUMN collector_group_id uuid NOT NULL;
ALTER TABLE probe ADD CONSTRAINT probe_collector_group_fkey FOREIGN KEY (collector_group_id, organization_id)
    REFERENCES collector_group (id, organization_id) ON DELETE RESTRICT;
CREATE INDEX idx_probe_org_collector_group ON probe (organization_id, collector_group_id);

-- The collector group of a probe is one of the probe's client or an
-- org-wide group. The trigger runs after probe_ci_scope (name order), so
-- the client is already that of the CI.
CREATE FUNCTION probe_collector_group_client() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    group_client uuid;
BEGIN
    previous := app_scope_widen();
    SELECT g.client_id INTO group_client
      FROM collector_group g WHERE g.id = NEW.collector_group_id AND g.organization_id = NEW.organization_id FOR SHARE;
    PERFORM app_scope_restore(previous);
    IF group_client IS NOT NULL AND group_client IS DISTINCT FROM NEW.client_id THEN
        RAISE EXCEPTION 'the collector group of a probe belongs to the probe''s client or the whole organization'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'probe_collector_group_client';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER probe_collector_group_client BEFORE INSERT OR UPDATE OF collector_group_id, ci_id, client_id, organization_id ON probe
    FOR EACH ROW EXECUTE FUNCTION probe_collector_group_client();

-- ─── 5. Groups of the collectors and propagation ─────────────────────────────

-- The name of a collector's own group: collector.name has no length or
-- blank check, collector_group.name has both.
CREATE FUNCTION collector_group_name(collector_name text, collector_id uuid) RETURNS text
LANGUAGE sql IMMUTABLE
SET search_path = pg_catalog, pg_temp
AS $$
    SELECT COALESCE(NULLIF(btrim(left(collector_name, 200)), ''), 'collector ' || collector_id::text)
$$;

-- Every new collector gets a group of its own with itself as member, in the
-- collector's tenant context (pattern organization_default_vrf, 000092);
-- the caller's organization and scope are restored.
CREATE FUNCTION collector_default_group() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous_org text := current_setting('app.org_id', true);
    previous text[];
    gid uuid;
BEGIN
    PERFORM set_config('app.org_id', NEW.organization_id::text, true);
    previous := app_scope_widen();
    INSERT INTO collector_group (organization_id, client_id, site_id, name, mode)
    VALUES (NEW.organization_id, NEW.client_id, NEW.site_id, collector_group_name(NEW.name, NEW.id), 'distributed')
    RETURNING id INTO gid;
    INSERT INTO collector_group_member (group_id, collector_id, organization_id)
    VALUES (gid, NEW.id, NEW.organization_id);
    PERFORM app_scope_restore(previous);
    PERFORM set_config('app.org_id', COALESCE(previous_org, ''), true);
    RETURN NULL;
END
$$;
CREATE TRIGGER collector_default_group AFTER INSERT ON collector
    FOR EACH ROW EXECUTE FUNCTION collector_default_group();

-- default-groups:begin
-- The group of an existing collector takes the collector's id.
INSERT INTO collector_group (id, organization_id, client_id, site_id, name, mode)
SELECT c.id, c.organization_id, c.client_id, c.site_id, collector_group_name(c.name, c.id), 'distributed'
  FROM collector c
 WHERE NOT EXISTS (SELECT 1 FROM collector_group_member m WHERE m.collector_id = c.id);
INSERT INTO collector_group_member (group_id, collector_id, organization_id)
SELECT c.id, c.id, c.organization_id
  FROM collector c
 WHERE NOT EXISTS (SELECT 1 FROM collector_group_member m WHERE m.collector_id = c.id);
-- default-groups:end

-- A collector moving to another client or site carries its group along
-- when it is the only member; then the members are re-derived (a no-op
-- UPDATE re-runs the client rule), so a move out of the client of a shared
-- group fails.
CREATE FUNCTION collector_group_follow_collector() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
    gid uuid;
    members integer;
BEGIN
    SELECT m.group_id INTO gid FROM collector_group_member m
     WHERE m.collector_id = NEW.id AND m.organization_id = NEW.organization_id;
    IF gid IS NOT NULL THEN
        SELECT count(*) INTO members FROM collector_group_member m WHERE m.group_id = gid;
        IF members = 1 THEN
            UPDATE collector_group SET client_id = NEW.client_id, site_id = NEW.site_id
             WHERE id = gid AND organization_id = NEW.organization_id
               AND (client_id IS DISTINCT FROM NEW.client_id OR site_id IS DISTINCT FROM NEW.site_id);
        END IF;
        UPDATE collector_group_member SET collector_id = collector_id
         WHERE collector_id = NEW.id AND organization_id = NEW.organization_id;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER collector_group_follow_collector
    AFTER UPDATE ON collector
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION collector_group_follow_collector();

-- A group changing client or site re-derives its members and re-checks the
-- client rule of its probes.
CREATE FUNCTION propagate_collector_group_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE collector_group_member SET group_id = group_id WHERE group_id = NEW.id AND organization_id = NEW.organization_id;
    UPDATE probe SET collector_group_id = collector_group_id WHERE collector_group_id = NEW.id AND organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_collector_group_scope
    AFTER UPDATE ON collector_group
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_collector_group_scope();

-- A probe with a new client or site re-derives its assignment; a probe in
-- another collector group loses it (its collector is no member of the new
-- group; the assignment of WP-947 assigns it anew with a new epoch).
CREATE FUNCTION propagate_probe_assignment_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    IF OLD.collector_group_id IS DISTINCT FROM NEW.collector_group_id THEN
        DELETE FROM probe_assignment WHERE probe_id = NEW.id AND organization_id = NEW.organization_id;
    ELSE
        UPDATE probe_assignment SET probe_id = probe_id WHERE probe_id = NEW.id AND organization_id = NEW.organization_id;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_probe_assignment_scope
    AFTER UPDATE ON probe
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id
        OR OLD.collector_group_id IS DISTINCT FROM NEW.collector_group_id)
    EXECUTE FUNCTION propagate_probe_assignment_scope();

-- A collector that leaves a group (member deleted or moved to another
-- group) loses the assignments of the group's probes.
CREATE FUNCTION drop_member_probe_assignments() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    DELETE FROM probe_assignment a USING probe p
     WHERE a.collector_id = OLD.collector_id AND a.organization_id = OLD.organization_id
       AND p.id = a.probe_id AND p.organization_id = a.organization_id AND p.collector_group_id = OLD.group_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_collector_group_member_left
    AFTER DELETE ON collector_group_member
    FOR EACH ROW EXECUTE FUNCTION drop_member_probe_assignments();
CREATE TRIGGER trg_collector_group_member_moved
    AFTER UPDATE ON collector_group_member
    FOR EACH ROW WHEN (OLD.group_id IS DISTINCT FROM NEW.group_id OR OLD.collector_id IS DISTINCT FROM NEW.collector_id)
    EXECUTE FUNCTION drop_member_probe_assignments();

-- ─── 6. Row level security ────────────────────────────────────────────────────

ALTER TABLE collector_group ENABLE ROW LEVEL SECURITY;
ALTER TABLE collector_group FORCE ROW LEVEL SECURITY;
ALTER TABLE collector_group_member ENABLE ROW LEVEL SECURITY;
ALTER TABLE collector_group_member FORCE ROW LEVEL SECURITY;
ALTER TABLE probe_assignment ENABLE ROW LEVEL SECURITY;
ALTER TABLE probe_assignment FORCE ROW LEVEL SECURITY;

-- Konvention 5 (E-09): a restricted principal reads the rows of its clients
-- and sites and those without; it writes only rows inside its scope,
-- org-wide rows only without client and site restriction (pattern 000292).
DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_match CONSTANT text := 'site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[])';
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['collector_group', 'collector_group_member', 'probe_assignment'] LOOP
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
