-- Reverts WP-353: organization-only policy and append-only guard of
-- migration 000049, no derived scope columns.

DROP POLICY disposal_record_scoped_delete ON disposal_record;
DROP POLICY disposal_record_scoped_update ON disposal_record;
DROP POLICY disposal_record_isolation ON disposal_record;
CREATE POLICY disposal_record_isolation ON disposal_record
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DROP TRIGGER ci_disposal_scope_propagation ON ci;
DROP TRIGGER asset_disposal_scope_propagation ON asset;
DROP FUNCTION propagate_disposal_record_scope();
DROP TRIGGER disposal_record_scope ON disposal_record;
DROP FUNCTION derive_disposal_record_scope();

CREATE OR REPLACE FUNCTION disposal_record_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'disposal_record is append-only (revision-safe)';
END;
$$;

DROP INDEX idx_disposal_record_client;
ALTER TABLE disposal_record DROP COLUMN site_id, DROP COLUMN client_id;
