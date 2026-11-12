-- Migration 000307: probe definitions, monitoring groups and monitoring
-- profiles (PRB-01, WP-935, E-178), and the binding targets device_group and
-- probe of credential_binding (CRD-01, CRD-04).
--
-- 1. probe(organization, client, site, ci NULL, type, target, params,
--    enabled, parent_probe NULL, created_by_surrogate): the central probe
--    definition. params holds only the overrides of the level probe. With a
--    CI the trigger probe_ci_scope takes client and site from the CI; without
--    one the client is required. One probe per (client, CI, type, target),
--    also without CI (NULLS NOT DISTINCT): with a CI the client is the CI's
--    and the key is that of AK1; without CI the client is the scope, so two
--    clients may check the same private target (overlapping RFC 1918
--    networks) and a conflict reveals nothing across the client boundary.
-- 2. monitoring_group (client NULL = org-wide, E-09) with priority, and
--    monitoring_group_member(group, ci) whose client and site come from the
--    member CI (trigger monitoring_group_member_scope); a CI of another
--    client than the group's is refused (23514,
--    monitoring_group_member_client_rule). The group is the device group of
--    PRB-01 and CRD-04 (E-178 (b)).
-- 3. monitoring_profile: the overrides of the levels org, client, group and
--    ci per probe type; exactly the target column of the level is set
--    (client_id for client, group_id for group, ci_id for ci, none for org).
--    client_id and site_id are also the scope of the row: at level group the
--    group's client, at level ci client and site of the CI (trigger
--    monitoring_profile_scope), so the profile follows the client and site
--    scope like its target.
-- 4. Every reference carries organization_id (Konvention 19); ENABLE and
--    FORCE RLS with policies after Konvention 5 (org-wide rows are read by
--    everyone and written only by principals without client and site
--    restriction), truncate guard.
-- 5. A moved CI re-derives client and site of its probes and memberships; a
--    group changing its client re-checks its members. The scope triggers
--    read the parent row FOR SHARE: a concurrent change of its client or
--    site waits for the insert (and then re-derives the new row), or the
--    insert waits for the change and reads the new scope (READ COMMITTED).
-- 6. credential_binding gets device_group_id (→ monitoring_group) and
--    probe_id (→ probe); the target CHECK, the scope derivation and the
--    propagation of WP-890 (000292) cover both kinds.

-- ─── 1. probe ─────────────────────────────────────────────────────────────────

CREATE TABLE probe (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id      uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id            uuid NULL,
    site_id              uuid NULL,
    ci_id                uuid NULL,
    type                 text NOT NULL CONSTRAINT probe_type_check CHECK (type ~ '^[a-z][a-z0-9_]{1,30}$'),
    target               text NOT NULL CONSTRAINT probe_target_check CHECK (btrim(target) <> '' AND length(target) <= 1024),
    params               jsonb NOT NULL DEFAULT '{}' CONSTRAINT probe_params_check CHECK (jsonb_typeof(params) = 'object'),
    enabled              boolean NOT NULL DEFAULT true,
    parent_probe_id      uuid NULL,
    -- Konvention 16: the creator as surrogate, without foreign key.
    created_by_surrogate uuid NULL,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT probe_target_key UNIQUE NULLS NOT DISTINCT (organization_id, client_id, ci_id, type, target),
    CONSTRAINT probe_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT probe_client_required CHECK (ci_id IS NOT NULL OR client_id IS NOT NULL),
    CONSTRAINT probe_parent_not_self CHECK (parent_probe_id IS DISTINCT FROM id),
    CONSTRAINT probe_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id),
    CONSTRAINT probe_site_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id),
    CONSTRAINT probe_ci_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT probe_parent_fkey FOREIGN KEY (parent_probe_id, organization_id)
        REFERENCES probe (id, organization_id) ON DELETE SET NULL (parent_probe_id)
);
CREATE INDEX idx_probe_org_ci ON probe (organization_id, ci_id);
CREATE INDEX idx_probe_org_type ON probe (organization_id, type);
CREATE INDEX idx_probe_org_client ON probe (organization_id, client_id);
CREATE INDEX idx_probe_parent ON probe (organization_id, parent_probe_id) WHERE parent_probe_id IS NOT NULL;

-- Client and site of a probe are those of its CI, read with the scope
-- widened (000139) so a scoped writer cannot keep a stale scope; the
-- policies check the row afterwards. Without CI a site must belong to the
-- probe's client.
CREATE FUNCTION probe_ci_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    site_client uuid;
BEGIN
    previous := app_scope_widen();
    IF NEW.ci_id IS NOT NULL THEN
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
          FROM ci c WHERE c.id = NEW.ci_id AND c.organization_id = NEW.organization_id FOR SHARE;
    ELSIF NEW.site_id IS NOT NULL THEN
        SELECT s.client_id INTO site_client
          FROM site s WHERE s.id = NEW.site_id AND s.organization_id = NEW.organization_id;
    END IF;
    PERFORM app_scope_restore(previous);
    IF NEW.ci_id IS NULL AND NEW.site_id IS NOT NULL AND site_client IS NOT NULL
       AND site_client IS DISTINCT FROM NEW.client_id THEN
        RAISE EXCEPTION 'the site of a probe belongs to the probe''s client'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'probe_site_client';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER probe_ci_scope BEFORE INSERT OR UPDATE OF ci_id, client_id, site_id, organization_id ON probe
    FOR EACH ROW EXECUTE FUNCTION probe_ci_scope();
CREATE TRIGGER trg_probe_updated_at BEFORE UPDATE ON probe
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON probe
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── 2. monitoring_group and monitoring_group_member ─────────────────────────

CREATE TABLE monitoring_group (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       uuid NULL,
    name            text NOT NULL CONSTRAINT monitoring_group_name_check CHECK (btrim(name) <> '' AND length(name) <= 200),
    priority        integer NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT monitoring_group_name_key UNIQUE (organization_id, name),
    CONSTRAINT monitoring_group_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT monitoring_group_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id)
);
CREATE INDEX idx_monitoring_group_client ON monitoring_group (organization_id, client_id) WHERE client_id IS NOT NULL;
CREATE TRIGGER trg_monitoring_group_updated_at BEFORE UPDATE ON monitoring_group
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON monitoring_group
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

CREATE TABLE monitoring_group_member (
    group_id        uuid NOT NULL,
    ci_id           uuid NOT NULL,
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       uuid NULL,
    site_id         uuid NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, ci_id),
    CONSTRAINT monitoring_group_member_group_fkey FOREIGN KEY (group_id, organization_id)
        REFERENCES monitoring_group (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT monitoring_group_member_ci_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT monitoring_group_member_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id),
    CONSTRAINT monitoring_group_member_site_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id)
);
CREATE INDEX idx_monitoring_group_member_ci ON monitoring_group_member (organization_id, ci_id);

-- Client and site of a membership are those of the member CI (the parent
-- row); a group of a client takes only CIs of that client, an org-wide
-- group any CI.
CREATE FUNCTION monitoring_group_member_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    group_client uuid;
BEGIN
    previous := app_scope_widen();
    SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
      FROM ci c WHERE c.id = NEW.ci_id AND c.organization_id = NEW.organization_id FOR SHARE;
    SELECT g.client_id INTO group_client
      FROM monitoring_group g WHERE g.id = NEW.group_id AND g.organization_id = NEW.organization_id FOR SHARE;
    PERFORM app_scope_restore(previous);
    IF group_client IS NOT NULL AND NEW.client_id IS DISTINCT FROM group_client THEN
        RAISE EXCEPTION 'a monitoring group of a client takes only CIs of that client'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'monitoring_group_member_client_rule';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER monitoring_group_member_scope BEFORE INSERT OR UPDATE ON monitoring_group_member
    FOR EACH ROW EXECUTE FUNCTION monitoring_group_member_scope();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON monitoring_group_member
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── 3. monitoring_profile ────────────────────────────────────────────────────

CREATE TABLE monitoring_profile (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    level           text NOT NULL CONSTRAINT monitoring_profile_level_check
                        CHECK (level IN ('org', 'client', 'group', 'ci')),
    client_id       uuid NULL,
    group_id        uuid NULL,
    ci_id           uuid NULL,
    site_id         uuid NULL,
    probe_type      text NOT NULL CONSTRAINT monitoring_profile_probe_type_check CHECK (probe_type ~ '^[a-z][a-z0-9_]{1,30}$'),
    params          jsonb NOT NULL CONSTRAINT monitoring_profile_params_check CHECK (jsonb_typeof(params) = 'object'),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    -- Exactly the target column of the level is set (org: none); at the
    -- levels group and ci client_id is the derived scope.
    CONSTRAINT monitoring_profile_level_target_check CHECK (
        (level = 'org' AND client_id IS NULL AND group_id IS NULL AND ci_id IS NULL)
        OR (level = 'client' AND client_id IS NOT NULL AND group_id IS NULL AND ci_id IS NULL)
        OR (level = 'group' AND group_id IS NOT NULL AND ci_id IS NULL)
        OR (level = 'ci' AND ci_id IS NOT NULL AND group_id IS NULL)
    ),
    CONSTRAINT monitoring_profile_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT monitoring_profile_group_fkey FOREIGN KEY (group_id, organization_id)
        REFERENCES monitoring_group (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT monitoring_profile_ci_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT monitoring_profile_site_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id)
);
-- One profile per level, target and probe type; the target is the most
-- specific column (client_id is only the scope at the levels group and ci).
CREATE UNIQUE INDEX uq_monitoring_profile_target
    ON monitoring_profile (organization_id, level, (COALESCE(ci_id, group_id, client_id)), probe_type) NULLS NOT DISTINCT;
CREATE INDEX idx_monitoring_profile_group ON monitoring_profile (organization_id, group_id) WHERE group_id IS NOT NULL;
CREATE INDEX idx_monitoring_profile_ci ON monitoring_profile (organization_id, ci_id) WHERE ci_id IS NOT NULL;
-- The scope of a profile is that of its target, read with the scope
-- widened (000139); the policies check the row afterwards.
CREATE FUNCTION monitoring_profile_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
BEGIN
    previous := app_scope_widen();
    IF NEW.level = 'group' THEN
        SELECT g.client_id, NULL::uuid INTO NEW.client_id, NEW.site_id
          FROM monitoring_group g WHERE g.id = NEW.group_id AND g.organization_id = NEW.organization_id FOR SHARE;
    ELSIF NEW.level = 'ci' THEN
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
          FROM ci c WHERE c.id = NEW.ci_id AND c.organization_id = NEW.organization_id FOR SHARE;
    ELSE
        NEW.site_id := NULL;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NEW;
END
$$;
CREATE TRIGGER monitoring_profile_scope BEFORE INSERT OR UPDATE ON monitoring_profile
    FOR EACH ROW EXECUTE FUNCTION monitoring_profile_scope();
CREATE TRIGGER trg_monitoring_profile_updated_at BEFORE UPDATE ON monitoring_profile
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON monitoring_profile
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── 4. Row level security ────────────────────────────────────────────────────

ALTER TABLE probe ENABLE ROW LEVEL SECURITY;
ALTER TABLE probe FORCE ROW LEVEL SECURITY;
ALTER TABLE monitoring_group ENABLE ROW LEVEL SECURITY;
ALTER TABLE monitoring_group FORCE ROW LEVEL SECURITY;
ALTER TABLE monitoring_group_member ENABLE ROW LEVEL SECURITY;
ALTER TABLE monitoring_group_member FORCE ROW LEVEL SECURITY;
ALTER TABLE monitoring_profile ENABLE ROW LEVEL SECURITY;
ALTER TABLE monitoring_profile FORCE ROW LEVEL SECURITY;

-- Konvention 5 (E-09): probe, monitoring_group_member and
-- monitoring_profile carry client and site: a restricted principal reads
-- the rows of its clients and sites and those without; it writes only rows
-- inside its scope (pattern 000292), org-wide rows only without client and
-- site restriction. monitoring_group carries a client only: a principal
-- restricted to clients reads its clients' groups and the org-wide ones
-- and writes only its clients' groups; a principal restricted to sites
-- writes none (pattern 000298). No policy uses EXISTS.
DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_match CONSTANT text := 'site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[])';
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['probe', 'monitoring_group_member', 'monitoring_profile'] LOOP
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
    FOREACH t IN ARRAY ARRAY['monitoring_group'] LOOP
        EXECUTE format('CREATE POLICY %I ON %I USING (%s AND (%s OR client_id IS NULL OR %s)) WITH CHECK (%s AND (%s OR %s) AND %s)',
            t || '_isolation', t, org_match, scope_all, scope_match, org_match, scope_all, scope_match, site_all);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR UPDATE USING (%s AND (%s OR %s) AND %s) WITH CHECK (%s AND (%s OR %s) AND %s)',
            t || '_scoped_update', t, org_match, scope_all, scope_match, site_all, org_match, scope_all, scope_match, site_all);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR DELETE USING (%s AND (%s OR %s) AND %s)',
            t || '_scoped_delete', t, org_match, scope_all, scope_match, site_all);
    END LOOP;
END
$$;

-- ─── 5. Propagation ───────────────────────────────────────────────────────────

-- A moved CI re-derives client and site of its probes, memberships and
-- profiles (a no-op UPDATE re-runs the derivation); a move that breaks the
-- client rule of a client's monitoring group fails. A group changing its
-- client re-checks its members and re-derives its profiles the same way.
CREATE FUNCTION propagate_monitoring_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    IF TG_TABLE_NAME = 'ci' THEN
        UPDATE probe SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
        UPDATE monitoring_group_member SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
        UPDATE monitoring_profile SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
    ELSE
        UPDATE monitoring_group_member SET group_id = group_id WHERE group_id = NEW.id AND organization_id = NEW.organization_id;
        UPDATE monitoring_profile SET group_id = group_id WHERE group_id = NEW.id AND organization_id = NEW.organization_id;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_ci_monitoring_scope
    AFTER UPDATE ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_monitoring_scope();
CREATE TRIGGER trg_monitoring_group_member_scope
    AFTER UPDATE ON monitoring_group
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION propagate_monitoring_scope();

-- ─── 6. credential_binding: targets device_group and probe ────────────────────

ALTER TABLE credential_binding ADD COLUMN device_group_id uuid NULL;
ALTER TABLE credential_binding ADD COLUMN probe_id uuid NULL;
ALTER TABLE credential_binding ADD CONSTRAINT credential_binding_device_group_fkey FOREIGN KEY (device_group_id, organization_id)
    REFERENCES monitoring_group (id, organization_id) ON DELETE CASCADE;
ALTER TABLE credential_binding ADD CONSTRAINT credential_binding_probe_fkey FOREIGN KEY (probe_id, organization_id)
    REFERENCES probe (id, organization_id) ON DELETE CASCADE;
ALTER TABLE credential_binding DROP CONSTRAINT credential_binding_target_check;
ALTER TABLE credential_binding ADD CONSTRAINT credential_binding_target_check CHECK (
    (target_kind = 'ci' AND ci_id IS NOT NULL AND subnet_id IS NULL AND device_group_id IS NULL AND probe_id IS NULL)
    OR (target_kind = 'subnet' AND subnet_id IS NOT NULL AND ci_id IS NULL AND device_group_id IS NULL AND probe_id IS NULL)
    OR (target_kind = 'device_group' AND device_group_id IS NOT NULL AND ci_id IS NULL AND subnet_id IS NULL AND probe_id IS NULL)
    OR (target_kind = 'probe' AND probe_id IS NOT NULL AND ci_id IS NULL AND subnet_id IS NULL AND device_group_id IS NULL)
);
CREATE UNIQUE INDEX uq_credential_binding_device_group ON credential_binding (organization_id, device_group_id, namespace, priority)
    WHERE device_group_id IS NOT NULL;
CREATE UNIQUE INDEX uq_credential_binding_probe ON credential_binding (organization_id, probe_id, namespace, priority)
    WHERE probe_id IS NOT NULL;
CREATE INDEX idx_credential_binding_device_group ON credential_binding (organization_id, device_group_id);
CREATE INDEX idx_credential_binding_probe ON credential_binding (organization_id, probe_id);

-- The scope of a binding is that of its target: the client of the group
-- (no site), client and site of the probe; the client rule applies as for
-- CI and subnet. The new targets are read FOR SHARE like the parents of the
-- monitoring tables (section 5).
CREATE OR REPLACE FUNCTION derive_credential_binding_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
    credential_client uuid;
BEGIN
    IF NEW.target_kind = 'ci' THEN
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
          FROM ci c WHERE c.id = NEW.ci_id AND c.organization_id = NEW.organization_id;
    ELSIF NEW.target_kind = 'subnet' THEN
        SELECT s.client_id, s.site_id INTO NEW.client_id, NEW.site_id
          FROM subnet s WHERE s.id = NEW.subnet_id AND s.organization_id = NEW.organization_id;
    ELSIF NEW.target_kind = 'device_group' THEN
        SELECT g.client_id, NULL::uuid INTO NEW.client_id, NEW.site_id
          FROM monitoring_group g WHERE g.id = NEW.device_group_id AND g.organization_id = NEW.organization_id FOR SHARE;
    ELSIF NEW.target_kind = 'probe' THEN
        SELECT p.client_id, p.site_id INTO NEW.client_id, NEW.site_id
          FROM probe p WHERE p.id = NEW.probe_id AND p.organization_id = NEW.organization_id FOR SHARE;
    END IF;
    SELECT cr.client_id INTO credential_client
      FROM credential cr WHERE cr.id = NEW.credential_id AND cr.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    IF credential_client IS NOT NULL AND NEW.client_id IS DISTINCT FROM credential_client THEN
        RAISE EXCEPTION 'a credential of a client binds only to a target of the same client'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'credential_binding_client_rule';
    END IF;
    RETURN NEW;
END
$$;

-- A moved CI, subnet or probe and a group changing its client re-derive the
-- scope of their bindings.
CREATE OR REPLACE FUNCTION propagate_credential_binding_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    IF TG_TABLE_NAME = 'ci' THEN
        UPDATE credential_binding SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
    ELSIF TG_TABLE_NAME = 'subnet' THEN
        UPDATE credential_binding SET subnet_id = subnet_id WHERE subnet_id = NEW.id AND organization_id = NEW.organization_id;
    ELSIF TG_TABLE_NAME = 'monitoring_group' THEN
        UPDATE credential_binding SET device_group_id = device_group_id WHERE device_group_id = NEW.id AND organization_id = NEW.organization_id;
    ELSE
        UPDATE credential_binding SET probe_id = probe_id WHERE probe_id = NEW.id AND organization_id = NEW.organization_id;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_monitoring_group_credential_binding_scope
    AFTER UPDATE ON monitoring_group
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION propagate_credential_binding_scope();
CREATE TRIGGER trg_probe_credential_binding_scope
    AFTER UPDATE ON probe
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_credential_binding_scope();
