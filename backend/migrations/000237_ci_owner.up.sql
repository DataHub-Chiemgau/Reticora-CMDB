-- WP-280 (CI-07, CI-01, TEN-05, DB-05, TLC-04): the owner of a CI, a user
-- and a team of the organization, mirrored from the asset of a linked CI,
-- and the team scope on ci and asset.
--
-- * ci.owner_user_id is the surrogate app_user.id (convention 16, E-124: no
--   personal data at the CI). ci_owner_user_fkey has no delete action
--   (NO ACTION, not RESTRICT: deleting an organization removes ci and
--   app_user in the same statement); a user who owns a CI cannot be deleted
--   (TLC-04). ci.owner_team_id is team.id; deleting the team clears it
--   (ON DELETE SET NULL (owner_team_id)), as for asset.owner_team_id
--   (000229). Both references carry the organization (K19); the keys
--   app_user_id_organization_key (000177) and team_id_organization_key
--   (000150) exist.
-- * DB-05: the owner of a linked CI is the owner of its asset. Existing
--   linked CIs take it. ci_mirror_asset_owner sets it on every insert and
--   every change of asset_id, and refuses a direct write of another owner
--   (pattern ci_mirror_asset_location, 000088): on insert a set owner other
--   than the asset's, on update a changed owner column while asset_id stays
--   (23514, constraint ci_owner_from_asset, COLUMN names the field). Nested
--   writes with an unchanged asset_id (asset_propagate_owner, the
--   ON DELETE SET NULL of the team reference) keep the written value.
--   Unlinking keeps the last mirrored owner.
-- * asset_propagate_owner carries an owner change of an asset to its CIs
--   under the row level security of the writer and refuses the change with
--   42501 (constraint asset_owner_ci_scope) when a CI of the asset outside
--   the scope keeps another owner, as asset_propagate_location does since
--   000181 (WP-650); the check widens client, site and team scope.
-- * TEN-05 (E-11): ci_isolation and asset_tenant_isolation take the team
--   predicate on owner_team_id like ticket_tenant_isolation (000065): USING
--   shows rows without team, WITH CHECK requires the team of the scope. The
--   restrictive *_scoped_update/*_scoped_delete policies (000079) are
--   created again from the new WITH CHECK. The child tables of a CI do not
--   inherit the team scope in phase 2 (E-11).

-- 1. Columns, references and indexes.
ALTER TABLE ci
    ADD COLUMN owner_user_id uuid,
    ADD COLUMN owner_team_id uuid;

ALTER TABLE ci
    ADD CONSTRAINT ci_owner_user_fkey FOREIGN KEY (owner_user_id, organization_id)
        REFERENCES app_user (id, organization_id),
    ADD CONSTRAINT ci_owner_team_fkey FOREIGN KEY (owner_team_id, organization_id)
        REFERENCES team (id, organization_id) ON DELETE SET NULL (owner_team_id);

CREATE INDEX idx_ci_owner_user ON ci (organization_id, owner_user_id) WHERE owner_user_id IS NOT NULL;
CREATE INDEX idx_ci_owner_team ON ci (organization_id, owner_team_id) WHERE owner_team_id IS NOT NULL;

-- 2. Existing linked CIs take the owner of their asset.
UPDATE ci c SET owner_user_id = a.owner_user_id, owner_team_id = a.owner_team_id
FROM asset a
WHERE a.id = c.asset_id AND a.organization_id = c.organization_id
  AND (a.owner_user_id IS NOT NULL OR a.owner_team_id IS NOT NULL);

-- 3. The owner of a linked CI is the owner of its asset.
CREATE FUNCTION ci_mirror_asset_owner() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    asset_user uuid;
    asset_team uuid;
    field text;
BEGIN
    IF NEW.asset_id IS NULL THEN
        RETURN NEW;
    END IF;
    -- A nested write with the same asset (asset_propagate_owner, the
    -- ON DELETE SET NULL of the team reference) keeps the written value.
    IF TG_OP = 'UPDATE' AND pg_trigger_depth() > 1 AND NEW.asset_id IS NOT DISTINCT FROM OLD.asset_id THEN
        RETURN NEW;
    END IF;
    SELECT a.owner_user_id, a.owner_team_id INTO asset_user, asset_team FROM asset a WHERE a.id = NEW.asset_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'asset % not found', NEW.asset_id
            USING ERRCODE = '23503', CONSTRAINT = 'ci_asset_id_fkey';
    END IF;
    -- A direct write (not the mirror cascade) of another owner on a linked
    -- CI is refused.
    IF pg_trigger_depth() = 1 THEN
        IF TG_OP = 'INSERT' THEN
            IF NEW.owner_user_id IS NOT NULL AND NEW.owner_user_id IS DISTINCT FROM asset_user THEN
                field := 'owner_user_id';
            ELSIF NEW.owner_team_id IS NOT NULL AND NEW.owner_team_id IS DISTINCT FROM asset_team THEN
                field := 'owner_team_id';
            END IF;
        ELSIF NEW.asset_id IS NOT DISTINCT FROM OLD.asset_id THEN
            IF NEW.owner_user_id IS DISTINCT FROM OLD.owner_user_id AND NEW.owner_user_id IS DISTINCT FROM asset_user THEN
                field := 'owner_user_id';
            ELSIF NEW.owner_team_id IS DISTINCT FROM OLD.owner_team_id AND NEW.owner_team_id IS DISTINCT FROM asset_team THEN
                field := 'owner_team_id';
            END IF;
        END IF;
        IF field IS NOT NULL THEN
            RAISE EXCEPTION 'the owner of CI % is the owner of its asset', NEW.id
                USING ERRCODE = '23514', CONSTRAINT = 'ci_owner_from_asset', COLUMN = field;
        END IF;
    END IF;
    NEW.owner_user_id := asset_user;
    NEW.owner_team_id := asset_team;
    RETURN NEW;
END
$$;

-- Fires after trg_ci_asset_client and trg_ci_asset_location (name order):
-- an invisible asset fails there with ci_asset_id_fkey first.
CREATE TRIGGER trg_ci_asset_owner BEFORE INSERT OR UPDATE OF owner_user_id, owner_team_id, asset_id ON ci
    FOR EACH ROW EXECUTE FUNCTION ci_mirror_asset_owner();

-- 4. A new owner of an asset reaches all of its CIs, or the change fails.
CREATE FUNCTION asset_propagate_owner() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    previous_team text;
    diverged boolean;
BEGIN
    UPDATE ci SET owner_user_id = NEW.owner_user_id, owner_team_id = NEW.owner_team_id
    WHERE asset_id = NEW.id
      AND (owner_user_id IS DISTINCT FROM NEW.owner_user_id OR owner_team_id IS DISTINCT FROM NEW.owner_team_id);
    previous := app_scope_widen();
    previous_team := current_setting('app.team_scope', true);
    PERFORM set_config('app.team_scope', '', true);
    SELECT EXISTS (SELECT 1 FROM ci c
                    WHERE c.asset_id = NEW.id AND c.organization_id = NEW.organization_id
                      AND (c.owner_user_id IS DISTINCT FROM NEW.owner_user_id
                           OR c.owner_team_id IS DISTINCT FROM NEW.owner_team_id))
      INTO diverged;
    PERFORM set_config('app.team_scope', COALESCE(previous_team, ''), true);
    PERFORM app_scope_restore(previous);
    IF diverged THEN
        RAISE EXCEPTION 'a CI of asset % is outside the scope of the writer', NEW.id
            USING ERRCODE = '42501', CONSTRAINT = 'asset_owner_ci_scope';
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_asset_propagate_owner AFTER UPDATE OF owner_user_id, owner_team_id ON asset
    FOR EACH ROW WHEN (OLD.owner_user_id IS DISTINCT FROM NEW.owner_user_id
                       OR OLD.owner_team_id IS DISTINCT FROM NEW.owner_team_id)
    EXECUTE FUNCTION asset_propagate_owner();

-- 5. Team scope on ci and asset (TEN-05, E-11).
DROP POLICY ci_scoped_update ON ci;
DROP POLICY ci_scoped_delete ON ci;
DROP POLICY ci_isolation ON ci;
CREATE POLICY ci_isolation ON ci
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.team_scope', true), '') IS NULL OR owner_team_id IS NULL
             OR owner_team_id = ANY (string_to_array(current_setting('app.team_scope', true), ',')::uuid[]))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.team_scope', true), '') IS NULL
             OR owner_team_id = ANY (string_to_array(current_setting('app.team_scope', true), ',')::uuid[]))
    );

DROP POLICY asset_scoped_update ON asset;
DROP POLICY asset_scoped_delete ON asset;
DROP POLICY asset_tenant_isolation ON asset;
CREATE POLICY asset_tenant_isolation ON asset
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.team_scope', true), '') IS NULL OR owner_team_id IS NULL
             OR owner_team_id = ANY (string_to_array(current_setting('app.team_scope', true), ',')::uuid[]))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.team_scope', true), '') IS NULL
             OR owner_team_id = ANY (string_to_array(current_setting('app.team_scope', true), ',')::uuid[]))
    );

-- The restrictive write policies from the new WITH CHECK (000079).
DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN
        SELECT p.tablename, p.with_check
          FROM pg_policies p
         WHERE p.schemaname = 'public'
           AND (p.tablename, p.policyname) IN (('ci', 'ci_isolation'), ('asset', 'asset_tenant_isolation'))
         ORDER BY p.tablename
    LOOP
        EXECUTE format('CREATE POLICY %I ON public.%I AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                       rec.tablename || '_scoped_update', rec.tablename, rec.with_check, rec.with_check);
        EXECUTE format('CREATE POLICY %I ON public.%I AS RESTRICTIVE FOR DELETE USING (%s)',
                       rec.tablename || '_scoped_delete', rec.tablename, rec.with_check);
    END LOOP;
END
$$;
