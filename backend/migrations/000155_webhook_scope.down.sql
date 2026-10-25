-- Reverts WP-601: webhook subscriptions without scope snapshot; the
-- organization policy of migration 000023.

DROP POLICY webhook_sub_scope_select ON webhook_subscription;
DROP POLICY webhook_sub_isolation ON webhook_subscription;
CREATE POLICY webhook_sub_isolation ON webhook_subscription
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

ALTER TABLE webhook_subscription
    DROP CONSTRAINT webhook_subscription_scope_check,
    DROP COLUMN scope_teams,
    DROP COLUMN scope_sites,
    DROP COLUMN scope_clients,
    DROP COLUMN scope_recorded;
