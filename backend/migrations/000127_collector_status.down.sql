DROP POLICY collector_isolation ON collector;
DROP POLICY collector_scoped_update ON collector;
DROP POLICY collector_scoped_delete ON collector;
CREATE POLICY collector_isolation ON collector FOR ALL
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])));
CREATE POLICY collector_scoped_update ON collector AS RESTRICTIVE FOR UPDATE
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])));
CREATE POLICY collector_scoped_delete ON collector AS RESTRICTIVE FOR DELETE
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])));
DROP TRIGGER IF EXISTS trg_collector_site_binding ON collector;
DROP FUNCTION IF EXISTS collector_site_binding();
DROP INDEX IF EXISTS idx_collector_heartbeat;
ALTER TABLE collector DROP COLUMN status_changed_at, DROP COLUMN cert_not_after, DROP COLUMN cert_fingerprint, DROP COLUMN site_id;
ALTER TABLE collector RENAME COLUMN last_heartbeat_at TO last_heartbeat;
ALTER TABLE collector DROP CONSTRAINT collector_status_check;
UPDATE collector SET status = CASE WHEN status IN ('offline', 'disabled') THEN 'offline' ELSE 'online' END;
ALTER TABLE collector ALTER COLUMN status SET DEFAULT 'online';
ALTER TABLE collector ADD CONSTRAINT collector_status_check CHECK (status IN ('online', 'offline', 'degraded'));
