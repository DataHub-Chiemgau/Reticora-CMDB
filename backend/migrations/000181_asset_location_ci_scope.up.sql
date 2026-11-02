-- Migration 000181: an asset moves only when all of its CIs follow
-- (WP-650, RCK-03, TEN-02; F-380).
--
-- asset_propagate_location (000088) mirrors a new location of an asset to
-- its CIs under the row level security of the writer. A principal
-- restricted to client 1 moving an asset of client 1 that also carries a CI
-- of client 2 (invisible) or a CI without client (visible, but only
-- org-wide principals update it, ci_scoped_update) moved the asset and its
-- writable CIs; the other CI kept its old location without an error, and
-- asset.location_id no longer matched its ci.location_id. A rack placement
-- (rack/placement.go) reported the mount as done.
--
-- The trigger still updates the CIs under the writer's RLS: a scoped
-- principal changes no row outside its scope. It then checks, with the
-- client and site scope widened (app_scope_widen/app_scope_restore, 000139,
-- like derive_location_site in 000151; app.org_id stays), whether a CI of
-- the asset still has another location, and refuses the update with 42501
-- (constraint asset_location_ci_scope): the statement and its transaction
-- fail, nothing diverges. The check returns no row and no value to the
-- caller, only the refusal; that an asset the writer may update carries a CI
-- outside its scope is what the refusal discloses (as for documents with
-- links outside the scope, 000139). Org-wide writers update every CI and are
-- never refused. No SECURITY DEFINER; search_path is fixed.

CREATE OR REPLACE FUNCTION asset_propagate_location() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    diverged boolean;
BEGIN
    UPDATE ci SET location_id = NEW.location_id
    WHERE asset_id = NEW.id AND location_id IS DISTINCT FROM NEW.location_id;
    previous := app_scope_widen();
    SELECT EXISTS (SELECT 1 FROM ci c
                    WHERE c.asset_id = NEW.id AND c.organization_id = NEW.organization_id
                      AND c.location_id IS DISTINCT FROM NEW.location_id)
      INTO diverged;
    PERFORM app_scope_restore(previous);
    IF diverged THEN
        RAISE EXCEPTION 'a CI of asset % is outside the scope of the writer', NEW.id
            USING ERRCODE = '42501', CONSTRAINT = 'asset_location_ci_scope';
    END IF;
    RETURN NULL;
END
$$;
