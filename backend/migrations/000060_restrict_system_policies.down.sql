-- Migration 000060 down: restore the writable system policies.

DROP POLICY IF EXISTS organization_system_select ON organization;

DO $$
DECLARE
    entry RECORD;
    old_expr CONSTANT text := '(organization_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid) OR (current_setting(''app.system'', true) = ''on'')';
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
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', entry.tbl || '_system_select', entry.tbl);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', entry.tbl || '_isolation', entry.tbl);
        EXECUTE format('CREATE POLICY %I ON %I FOR ALL USING (%s) WITH CHECK (%s)',
            entry.old_policy, entry.tbl, old_expr, old_expr);
    END LOOP;
END
$$;
