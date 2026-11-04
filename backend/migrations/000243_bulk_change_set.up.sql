-- Migration 000243: change set of the bulk operations (BLK-02, JOB-03;
-- WP-305).
--
-- Every bulk operation on CIs is a job ci.bulk (also below 100 ids). The
-- update of a CI under a bulk job writes, in the same transaction, one row
-- per changed field: the value before and after the bulk and the state of
-- its override in ci_field_value before and after the write
-- (override_before, override_after; NULL without an override). The revert
-- (POST /jobs/{id}/revert, job bulk.revert) writes the old values back and
-- records per row whether it reverted it or skipped it (a field changed
-- since the bulk): reverted_at and revert_outcome.
--
-- entity_id names a CI or an asset (entity_type) and is no reference: the
-- row outlives a hard delete of its object, whose revert is then skipped.
-- client_id and site_id are those of the object after the change and scope
-- the row like ci_merge (000131): the isolation policy reads rows without a
-- client or site org-wide, the restrictive policies keep updates and
-- deletes within the scope. The references carry the organization (K19,
-- patterns 000136 and 000194); for the job that is the new constraint
-- job_id_organization_key. A deleted site clears site_id only. The
-- retention of the rows (30 days) follows with WP-306; a deleted job takes
-- its rows with it.

ALTER TABLE job ADD CONSTRAINT job_id_organization_key UNIQUE (id, organization_id);

CREATE TABLE bulk_change_set (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       UUID,
    site_id         UUID,
    job_id          UUID NOT NULL,
    entity_type     TEXT NOT NULL,
    entity_id       UUID NOT NULL,
    op              TEXT NOT NULL,
    field_name      TEXT,
    old_value       JSONB,
    new_value       JSONB,
    override_before JSONB,
    override_after  JSONB,
    reverted_at     TIMESTAMPTZ,
    revert_outcome  TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT bulk_change_set_entity_type_check CHECK (entity_type IN ('ci', 'asset')),
    CONSTRAINT bulk_change_set_op_check CHECK (op IN ('set', 'add', 'remove')),
    CONSTRAINT bulk_change_set_revert_outcome_check CHECK (revert_outcome IN ('reverted', 'skipped')),
    CONSTRAINT bulk_change_set_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT bulk_change_set_site_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id) ON DELETE SET NULL (site_id),
    CONSTRAINT bulk_change_set_job_fkey FOREIGN KEY (job_id, organization_id)
        REFERENCES job (id, organization_id) ON DELETE CASCADE
);

CREATE INDEX idx_bulk_change_set_job ON bulk_change_set (organization_id, job_id);
CREATE INDEX idx_bulk_change_set_created ON bulk_change_set (organization_id, created_at);

ALTER TABLE bulk_change_set ENABLE ROW LEVEL SECURITY;
ALTER TABLE bulk_change_set FORCE ROW LEVEL SECURITY;

DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    client_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    client_ids CONSTANT text := 'string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_ids CONSTANT text := 'string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[]';
    readable text := format('%1$s AND (%2$s OR client_id IS NULL OR client_id = ANY (%3$s))'
                            ' AND (%4$s OR site_id IS NULL OR site_id = ANY (%5$s))',
                            org_match, client_all, client_ids, site_all, site_ids);
    writable text := format('%1$s AND (%2$s OR client_id = ANY (%3$s))'
                            ' AND (%4$s OR site_id = ANY (%5$s))',
                            org_match, client_all, client_ids, site_all, site_ids);
BEGIN
    EXECUTE format('CREATE POLICY bulk_change_set_isolation ON bulk_change_set FOR ALL USING (%s) WITH CHECK (%s)',
                   readable, writable);
    EXECUTE format('CREATE POLICY bulk_change_set_scoped_update ON bulk_change_set AS RESTRICTIVE FOR UPDATE '
                   'USING (%s) WITH CHECK (%s)', writable, writable);
    EXECUTE format('CREATE POLICY bulk_change_set_scoped_delete ON bulk_change_set AS RESTRICTIVE FOR DELETE USING (%s)',
                   writable);
END
$$;

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON bulk_change_set
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
