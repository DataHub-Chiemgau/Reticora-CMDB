-- WP-404 (API-09, JOB-03, TEN-09; F-163, E-09): jobs carry the scope of
-- their creator and are confined to it.
--
-- job (000115) carries the organization only, and its policies job_isolation
-- and job_system_select check organization_id. A principal restricted to
-- client 2 listed and read the jobs of client 1 with their parameters and
-- results (the CI ids of a bulk job) and cancelled them.
--
-- 1. scope_recorded, scope_clients, scope_sites and scope_teams snapshot the
--    tenant scope of the creator when the job is queued, like export_job
--    (000068): NULL grants the whole organization in that dimension, an
--    array restricts it to the listed ids (empty: none). A job the export
--    worker tracks after the fact takes the snapshot of its export job.
--    scope_recorded marks jobs with a snapshot; jobs queued outside a
--    request (system jobs) have none, and job_scope_check keeps the arrays
--    of such a job NULL.
-- 2. job_isolation (FOR ALL) checks in USING and WITH CHECK, besides the
--    organization, per dimension that the caller is org-wide (scope GUC
--    empty) or the job's snapshot lies completely in the caller's scope. A
--    job without snapshot belongs to the whole organization: only
--    principals without client, site and team restriction read and write it
--    (E-09).
-- 3. job_system_select stays. Permissive policies are combined with OR, so
--    its organization branch alone would show every job of the organization
--    to every principal; the restrictive job_scope_select applies the scope
--    predicate to every SELECT. The system flag, which WithSystem sets with
--    an empty scope, still reads every job for claiming.
--
-- Existing jobs keep no snapshot (scope_recorded false): the scope their
-- creator had when queueing them is not known, so they stay visible to
-- org-wide principals only. ENABLE and FORCE ROW LEVEL SECURITY stay.

ALTER TABLE job
    ADD COLUMN scope_recorded BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN scope_clients UUID[],
    ADD COLUMN scope_sites UUID[],
    ADD COLUMN scope_teams UUID[],
    ADD CONSTRAINT job_scope_check CHECK (
        scope_recorded OR (scope_clients IS NULL AND scope_sites IS NULL AND scope_teams IS NULL));

DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid';
    within text := '';
    dim RECORD;
BEGIN
    FOR dim IN
        SELECT * FROM (VALUES
            ('app.client_scope', 'scope_clients'),
            ('app.site_scope', 'scope_sites'),
            ('app.team_scope', 'scope_teams')
        ) AS t(guc, col)
    LOOP
        within := within || format(
            '%3$s(NULLIF(current_setting(%1$L, true), '''') IS NULL '
                'OR (%2$I IS NOT NULL AND %2$I <@ string_to_array(current_setting(%1$L, true), '','')::uuid[]))',
            dim.guc, dim.col, CASE WHEN within = '' THEN '' ELSE ' AND ' END);
    END LOOP;

    DROP POLICY job_isolation ON job;
    EXECUTE format('CREATE POLICY job_isolation ON job FOR ALL USING (%1$s AND %2$s) WITH CHECK (%1$s AND %2$s)',
                   org_match, within);
    EXECUTE format('CREATE POLICY job_scope_select ON job AS RESTRICTIVE FOR SELECT '
                   'USING (current_setting(''app.system'', true) = ''on'' OR %s)', within);
END
$$;
