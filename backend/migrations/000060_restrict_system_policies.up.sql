-- Migration 000060: system flag SELECT-only, writes only in an organization
-- context (WP-022, TEN-05, TEN-06, E-08)
--
-- The background workers ran with the app.system flag, and the policies of
-- webhook_delivery, webhook_dead_letter, export_job, alert_rule and
-- collector_enrollment_code let that flag write rows of every tenant. The
-- single ALL policy of each table is split:
--   * <table>_isolation (ALL): rows of app.org_id only, for reading and
--     writing;
--   * <table>_system_select (SELECT): additionally all rows under app.system,
--     so a worker can find due work (or an enrollment code by its hash)
--     across tenants. It then claims and changes the rows per organization in
--     a tenant transaction.
-- organization gets the same SELECT-only exception so the workers can
-- iterate organizations.

DO $$
DECLARE
    entry RECORD;
    org_match CONSTANT text := 'organization_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid';
    system_flag CONSTANT text := 'current_setting(''app.system'', true) = ''on''';
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('webhook_delivery', 'webhook_delivery_isolation'),
            ('webhook_dead_letter', 'webhook_dead_letter_isolation'),
            ('export_job', 'org_isolation'),
            ('alert_rule', 'alert_rule_isolation'),
            ('collector_enrollment_code', 'collector_enrollment_code_isolation')
        ) AS t(tbl, old_policy)
    LOOP
        EXECUTE format('DROP POLICY %I ON %I', entry.old_policy, entry.tbl);
        EXECUTE format('CREATE POLICY %I ON %I FOR ALL USING (%s) WITH CHECK (%s)',
            entry.tbl || '_isolation', entry.tbl, org_match, org_match);
        EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (%s OR %s)',
            entry.tbl || '_system_select', entry.tbl, org_match, system_flag);
    END LOOP;
END
$$;

CREATE POLICY organization_system_select ON organization FOR SELECT
    USING (id = NULLIF(current_setting('app.org_id', true), '')::uuid
           OR current_setting('app.system', true) = 'on');
