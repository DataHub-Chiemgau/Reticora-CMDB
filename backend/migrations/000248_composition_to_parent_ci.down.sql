-- Reverts 000248: the CIs the composition takeover changed get their
-- parent_ci_id, child_mode and location_id back from the before image, and
-- the quarantine rows of the migration go. composition was not changed.
--
-- One statement per CI, newest image first: the location a restored CI
-- carries on to its configuration children (trg_ci_propagate_configuration_
-- location) would otherwise touch a row the same statement updates.

DO $$
DECLARE
    img record;
BEGIN
    FOR img IN
        SELECT q.organization_id, q.row_data FROM migration_quarantine q
        WHERE q.migration = '000248' AND q.source_table = 'ci' AND q.row_data->>'rule' = 'before_image'
        ORDER BY q.id DESC
    LOOP
        UPDATE ci
        SET parent_ci_id = (img.row_data->>'parent_ci_id')::uuid,
            child_mode = img.row_data->>'child_mode',
            location_id = (img.row_data->>'location_id')::uuid
        WHERE id = (img.row_data->>'id')::uuid AND organization_id = img.organization_id;
    END LOOP;
END
$$;

DELETE FROM migration_quarantine WHERE migration = '000248';
