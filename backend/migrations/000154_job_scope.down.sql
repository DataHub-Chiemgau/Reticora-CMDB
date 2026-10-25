-- Reverts WP-404: jobs without scope snapshot; the organization policy of
-- migration 000115.

DROP POLICY job_scope_select ON job;
DROP POLICY job_isolation ON job;
CREATE POLICY job_isolation ON job FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);

ALTER TABLE job
    DROP CONSTRAINT job_scope_check,
    DROP COLUMN scope_teams,
    DROP COLUMN scope_sites,
    DROP COLUMN scope_clients,
    DROP COLUMN scope_recorded;
