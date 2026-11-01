-- Migration 000186: the number of active CIs of the organization for max_cis,
-- independent of the caller's client and site scope (WP-674, F-403; ENT-03,
-- ENT-08).
--
-- max_cis caps the active CIs of the organization. POST /cis, the restore,
-- the unmerge, the review resolutions and the ingest counted them with
-- ci.List or a count(*) under the RLS of the caller, and ci_isolation filters
-- by the client and site scope: a principal restricted to client 1 counted
-- only the CIs of client 1 and created CIs past the organization's limit.
--
-- ci_active_count() returns that number over every not deleted CI of the
-- organization in app.org_id. Only the client and site scope are widened
-- (app_scope_widen, 000139) for the one aggregate and restored afterwards;
-- app.org_id stays, so no row of another organization is reached, and the
-- function returns a number, no data of a CI. Without a tenant (nil
-- organization, WithSystem) it returns 0.
--
-- No SECURITY DEFINER (like 000141 and 000181): the widened scope, not
-- another role, makes the CIs of other clients visible, so the function runs
-- with the rights of the caller and FORCE ROW LEVEL SECURITY keeps applying.
-- search_path is fixed; only reticora_app may execute it, not PUBLIC. It
-- runs in the caller's transaction, so the ingest counts under its max_cis
-- lock (WP-334) and a review resolution in its own transaction.

CREATE FUNCTION ci_active_count() RETURNS bigint
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    total bigint;
BEGIN
    previous := app_scope_widen();
    SELECT count(*) INTO total FROM ci c
     WHERE c.organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid
       AND c.deleted_at IS NULL;
    PERFORM app_scope_restore(previous);
    RETURN total;
END
$$;

REVOKE ALL ON FUNCTION ci_active_count() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ci_active_count() TO reticora_app;
