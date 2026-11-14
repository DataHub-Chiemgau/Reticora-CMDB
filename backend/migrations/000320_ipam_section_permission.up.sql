-- Migration 000320: rights per IPAM section (IPM-10, WP-910, E-214 option 1).
--
-- * ipam_section_permission(section, team, level read|write): a section
--   with entries is read only by principals of a team with an entry and
--   written only by principals of a team with level write, in addition to
--   ipam:read/ipam:write. A section without entries behaves as before.
--   Composite foreign keys to the section and the team (Konvention 19),
--   ENABLE and FORCE row level security: the team scope applies to team_id
--   (pattern team_member_isolation, 000059), only principals without client
--   and site restriction write (pattern vrf_scoped_*, 000148), TRUNCATE
--   guard.
-- * ipam_section_denied(level) returns the sections of the organization
--   app.org_id the principal may not read (level read) or write (level
--   write): sections with at least one entry and none of a team of the
--   principal at that level (write includes read). The teams of the
--   principal are the teams of the user app.user_id (team_member) and, for
--   an API key (app.principal_type = 'api_key', set by WithTenant), the
--   teams app.team_scope names explicitly. A principal without team is shut
--   out of every section with entries (fail-closed). STABLE, SECURITY
--   DEFINER (reticora_owner, NOBYPASSRLS) with a fixed search_path; the
--   entries and memberships are read with the scope widened, so the team
--   scope of the caller hides none of them. The list is empty
--   * under app.system (system transactions see every section) and
--   * while the scope is widened (app_scope_widen): the triggers of the IPAM
--     tables (placement of the hierarchy, gateway mark, search index,
--     history, the derived section of an address) work on all rows of the
--     organization as they do across clients and sites. app_scope_widen
--     therefore also marks the widening in app.scope_widened, which
--     app_scope_restore resets.
-- * ip_address.section_id is derived from the subnet (NULL without one) by
--   a BEFORE trigger and follows a section change of the subnet (pattern
--   trg_ip_address_vrf/trg_subnet_propagate_vrf, 000092); no foreign key
--   (derived column, Konvention 19 last sentence). Backfill from the subnet.
-- * RESTRICTIVE policies: SELECT on ipam_section and subnet with level
--   read, INSERT, UPDATE and DELETE on subnet with level write, the same on
--   ip_address with "section_id IS NULL OR ...", SELECT on search_document
--   for the documents of subnets and addresses by metadata->>'section_id'.
--   The policies compare the column with the list of one sub-select, which
--   the planner runs once per statement (InitPlan), not per row.
-- * default_section_of reads the default section with the scope widened: a
--   principal shut out of the default section gets the row level security
--   refusal of its subnet (403), not a missing section.

-- ─── 1. Table ────────────────────────────────────────────────────────────────

CREATE TABLE ipam_section_permission (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    section_id      uuid NOT NULL,
    team_id         uuid NOT NULL,
    level           text NOT NULL CONSTRAINT ipam_section_permission_level_check CHECK (level IN ('read', 'write')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ipam_section_permission_section_team_key UNIQUE (section_id, team_id),
    CONSTRAINT ipam_section_permission_section_fkey FOREIGN KEY (section_id, organization_id)
        REFERENCES ipam_section (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT ipam_section_permission_team_fkey FOREIGN KEY (team_id, organization_id)
        REFERENCES team (id, organization_id) ON DELETE CASCADE
);
CREATE INDEX idx_ipam_section_permission_org_section ON ipam_section_permission (organization_id, section_id);
CREATE INDEX idx_ipam_section_permission_team ON ipam_section_permission (team_id, organization_id);

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON ipam_section_permission
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE ipam_section_permission ENABLE ROW LEVEL SECURITY;
ALTER TABLE ipam_section_permission FORCE ROW LEVEL SECURITY;

-- The team scope applies to team_id (CH25, TEN-05); entries are written
-- only by principals without client and site restriction (E-09).
CREATE POLICY ipam_section_permission_isolation ON ipam_section_permission
    USING (
        organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.team_scope', true), '') IS NULL
             OR team_id = ANY (string_to_array(current_setting('app.team_scope', true), ',')::uuid[]))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.team_scope', true), '') IS NULL
             OR team_id = ANY (string_to_array(current_setting('app.team_scope', true), ',')::uuid[]))
    );
CREATE POLICY ipam_section_permission_scoped_insert ON ipam_section_permission AS RESTRICTIVE FOR INSERT
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL);
CREATE POLICY ipam_section_permission_scoped_update ON ipam_section_permission AS RESTRICTIVE FOR UPDATE
    USING (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL);
CREATE POLICY ipam_section_permission_scoped_delete ON ipam_section_permission AS RESTRICTIVE FOR DELETE
    USING (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL);

-- ─── 2. Scope widening with marker ───────────────────────────────────────────

-- Version of 000217 that also marks the widening (app.scope_widened, fourth
-- element of the returned array): ipam_section_denied returns no section
-- while the scope is widened.
CREATE OR REPLACE FUNCTION app_scope_widen() RETURNS text[]
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
    previous text[] := ARRAY[current_setting('app.client_scope', true), current_setting('app.site_scope', true),
                             current_setting('app.team_scope', true), current_setting('app.scope_widened', true)];
BEGIN
    PERFORM set_config('app.client_scope', '', true), set_config('app.site_scope', '', true),
            set_config('app.team_scope', '', true), set_config('app.scope_widened', 'on', true);
    RETURN previous;
END
$$;

CREATE OR REPLACE FUNCTION app_scope_restore(previous text[]) RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    PERFORM set_config('app.client_scope', COALESCE(previous[1], ''), true),
            set_config('app.site_scope', COALESCE(previous[2], ''), true),
            set_config('app.team_scope', COALESCE(previous[3], ''), true),
            set_config('app.scope_widened', COALESCE(previous[4], ''), true);
END
$$;

-- ─── 3. Denied sections ──────────────────────────────────────────────────────

CREATE FUNCTION ipam_section_denied(p_level text) RETURNS uuid[]
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    org text := current_setting('app.org_id', true);
    uid text := current_setting('app.user_id', true);
    v_team_scope text := NULLIF(current_setting('app.team_scope', true), '');
    v_user uuid;
    key_teams uuid[] := '{}';
    previous text[];
    denied uuid[];
BEGIN
    IF p_level IS NULL OR p_level NOT IN ('read', 'write') THEN
        RAISE EXCEPTION 'invalid section permission level %', p_level USING ERRCODE = '22023';
    END IF;
    IF current_setting('app.system', true) = 'on' OR current_setting('app.scope_widened', true) = 'on'
       OR org IS NULL OR org = '' THEN
        RETURN '{}';
    END IF;
    IF uid ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
        v_user := uid::uuid;
    END IF;
    -- The team scope of an API key counts like memberships; only teams it
    -- names explicitly (the empty value "all teams" names none).
    IF current_setting('app.principal_type', true) = 'api_key' AND v_team_scope IS NOT NULL THEN
        key_teams := string_to_array(v_team_scope, ',')::uuid[];
    END IF;
    previous := app_scope_widen();
    BEGIN
        SELECT COALESCE(array_agg(DISTINCT p.section_id), '{}') INTO denied
          FROM ipam_section_permission p
         WHERE p.organization_id = org::uuid
           AND NOT EXISTS (
               SELECT 1 FROM ipam_section_permission q
                WHERE q.organization_id = p.organization_id AND q.section_id = p.section_id
                  AND (p_level = 'read' OR q.level = 'write')
                  AND (q.team_id = ANY (key_teams)
                       OR EXISTS (SELECT 1 FROM team_member m
                                   WHERE m.organization_id = q.organization_id AND m.team_id = q.team_id
                                     AND m.user_id = v_user)));
    EXCEPTION WHEN OTHERS THEN
        PERFORM app_scope_restore(previous);
        RAISE;
    END;
    PERFORM app_scope_restore(previous);
    RETURN denied;
END
$$;

-- Every role that reads the IPAM tables evaluates the policies, so EXECUTE
-- stays with PUBLIC; the function returns only section ids of app.org_id.
ALTER FUNCTION ipam_section_denied(text) OWNER TO reticora_owner;

-- The default section of an organization, whatever sections the caller may
-- read (version of 000298 with the scope widened).
CREATE OR REPLACE FUNCTION default_section_of(p_org uuid) RETURNS uuid
LANGUAGE plpgsql STABLE
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
    v_section uuid;
BEGIN
    SELECT id INTO v_section FROM ipam_section WHERE organization_id = p_org AND is_default;
    PERFORM app_scope_restore(previous);
    RETURN v_section;
END
$$;

-- ─── 4. Section of an address ────────────────────────────────────────────────

ALTER TABLE ip_address ADD COLUMN section_id uuid;

ALTER TABLE ip_address DISABLE TRIGGER USER;
UPDATE ip_address a SET section_id = s.section_id
  FROM subnet s WHERE s.id = a.subnet_id AND s.organization_id = a.organization_id;
ALTER TABLE ip_address ENABLE TRIGGER USER;

-- The section of the subnet, read with the scope widened: a subnet of a
-- section the writer may not read still gives its section, which the
-- policies of ip_address then refuse.
CREATE FUNCTION ip_address_section() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
BEGIN
    IF NEW.subnet_id IS NULL THEN
        NEW.section_id := NULL;
        RETURN NEW;
    END IF;
    previous := app_scope_widen();
    SELECT s.section_id INTO NEW.section_id FROM subnet s
     WHERE s.id = NEW.subnet_id AND s.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_ip_address_section BEFORE INSERT OR UPDATE OF subnet_id, section_id ON ip_address
    FOR EACH ROW EXECUTE FUNCTION ip_address_section();

-- A subnet moving to another section takes its addresses along, those of
-- every client included.
CREATE FUNCTION subnet_propagate_section() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE ip_address SET section_id = NEW.section_id
     WHERE organization_id = NEW.organization_id AND subnet_id = NEW.id
       AND section_id IS DISTINCT FROM NEW.section_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_subnet_propagate_section AFTER UPDATE OF section_id ON subnet
    FOR EACH ROW WHEN (OLD.section_id IS DISTINCT FROM NEW.section_id)
    EXECUTE FUNCTION subnet_propagate_section();

-- ─── 5. Restrictive policies ─────────────────────────────────────────────────

CREATE POLICY ipam_section_acl_read ON ipam_section AS RESTRICTIVE FOR SELECT
    USING (id <> ALL ((SELECT ipam_section_denied('read'))::uuid[]));

CREATE POLICY subnet_section_acl_read ON subnet AS RESTRICTIVE FOR SELECT
    USING (section_id <> ALL ((SELECT ipam_section_denied('read'))::uuid[]));
CREATE POLICY subnet_section_acl_insert ON subnet AS RESTRICTIVE FOR INSERT
    WITH CHECK (section_id <> ALL ((SELECT ipam_section_denied('write'))::uuid[]));
CREATE POLICY subnet_section_acl_update ON subnet AS RESTRICTIVE FOR UPDATE
    USING (section_id <> ALL ((SELECT ipam_section_denied('write'))::uuid[]))
    WITH CHECK (section_id <> ALL ((SELECT ipam_section_denied('write'))::uuid[]));
CREATE POLICY subnet_section_acl_delete ON subnet AS RESTRICTIVE FOR DELETE
    USING (section_id <> ALL ((SELECT ipam_section_denied('write'))::uuid[]));

CREATE POLICY ip_address_section_acl_read ON ip_address AS RESTRICTIVE FOR SELECT
    USING (section_id IS NULL OR section_id <> ALL ((SELECT ipam_section_denied('read'))::uuid[]));
CREATE POLICY ip_address_section_acl_insert ON ip_address AS RESTRICTIVE FOR INSERT
    WITH CHECK (section_id IS NULL OR section_id <> ALL ((SELECT ipam_section_denied('write'))::uuid[]));
CREATE POLICY ip_address_section_acl_update ON ip_address AS RESTRICTIVE FOR UPDATE
    USING (section_id IS NULL OR section_id <> ALL ((SELECT ipam_section_denied('write'))::uuid[]))
    WITH CHECK (section_id IS NULL OR section_id <> ALL ((SELECT ipam_section_denied('write'))::uuid[]));
CREATE POLICY ip_address_section_acl_delete ON ip_address AS RESTRICTIVE FOR DELETE
    USING (section_id IS NULL OR section_id <> ALL ((SELECT ipam_section_denied('write'))::uuid[]));

-- The documents of subnets and addresses carry the section in their
-- metadata (WP-909); other entity types stay untouched. Compared as text,
-- so no document's metadata is cast.
CREATE POLICY search_document_section_acl ON search_document AS RESTRICTIVE FOR SELECT
    USING (entity_type <> ALL (ARRAY['subnet', 'ip_address'])
           OR COALESCE(metadata->>'section_id', '') <> ALL ((SELECT ipam_section_denied('read'))::text[]));
