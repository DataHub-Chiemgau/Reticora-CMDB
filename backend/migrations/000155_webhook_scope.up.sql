-- WP-601 (TEN-09, TEN-06, WHK-01; E-98 option B): webhook subscriptions
-- carry the scope of their creator and are confined to it.
--
-- webhook_subscription (000007) carries the organization only, and its
-- policy webhook_sub_isolation (000023) checks organization_id. A principal
-- restricted to one client or site with webhook:manage listed, tested and
-- deleted every subscription of the organization, and a subscription it
-- created received the events of every client.
--
-- 1. scope_recorded, scope_clients, scope_sites and scope_teams snapshot the
--    tenant scope of the creator when the subscription is created, like job
--    (000154): NULL grants the whole organization in that dimension, an
--    array restricts it to the listed ids (empty: none). scope_recorded marks
--    subscriptions with a snapshot; webhook_subscription_scope_check keeps
--    the arrays of a subscription without one NULL. The dispatcher delivers
--    to a restricted snapshot only the events whose objects lie in it.
-- 2. webhook_sub_isolation (FOR ALL) checks in USING and WITH CHECK, besides
--    the organization, per dimension that the caller is org-wide (scope GUC
--    empty) or the subscription's snapshot lies completely in the caller's
--    scope. A subscription without snapshot belongs to the whole
--    organization: only principals without client, site and team
--    restriction read and manage it (E-09).
-- 3. The restrictive webhook_sub_scope_select applies the scope predicate to
--    every SELECT, so no permissive policy shows a scoped principal the
--    subscriptions of the whole organization; the system flag (WithSystem)
--    still passes it. The dispatcher, its retry worker, the deactivation and
--    the secret cleanup run org-wide (scope GUCs empty) and keep reaching
--    every subscription.
--
-- Existing subscriptions keep no snapshot (scope_recorded false): the scope
-- their creator had is not known, so they stay org-wide: they receive the
-- events as before and only org-wide principals see and manage them. ENABLE
-- and FORCE ROW LEVEL SECURITY stay.

ALTER TABLE webhook_subscription
    ADD COLUMN scope_recorded BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN scope_clients UUID[],
    ADD COLUMN scope_sites UUID[],
    ADD COLUMN scope_teams UUID[],
    ADD CONSTRAINT webhook_subscription_scope_check CHECK (
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

    DROP POLICY webhook_sub_isolation ON webhook_subscription;
    EXECUTE format('CREATE POLICY webhook_sub_isolation ON webhook_subscription FOR ALL '
                   'USING (%1$s AND %2$s) WITH CHECK (%1$s AND %2$s)', org_match, within);
    EXECUTE format('CREATE POLICY webhook_sub_scope_select ON webhook_subscription AS RESTRICTIVE FOR SELECT '
                   'USING (current_setting(''app.system'', true) = ''on'' OR %s)', within);
END
$$;
