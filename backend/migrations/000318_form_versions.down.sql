-- Reverts 000318: form_submission.form_version_id, form_def.current_version_id
-- and version, the table form_def_version with its triggers and functions;
-- the key form_def_id_organization_key goes only when nothing references it
-- any more (form_submission references it since 000288).

DROP INDEX idx_form_submission_form_version;
ALTER TABLE form_submission DROP CONSTRAINT form_submission_form_version_fkey;
ALTER TABLE form_submission DROP COLUMN form_version_id;

ALTER TABLE form_def DROP CONSTRAINT form_def_current_version_fkey;
ALTER TABLE form_def DROP COLUMN current_version_id;
ALTER TABLE form_def DROP COLUMN version;

DROP TRIGGER trg_form_def_version_scope ON form_def;
DROP FUNCTION propagate_form_def_version_scope();
DROP TABLE form_def_version;
DROP FUNCTION form_def_version_immutable();
DROP FUNCTION form_def_version_scope();

-- form-def-key:begin
DO $$
DECLARE
    key_index oid;
BEGIN
    SELECT conindid INTO key_index FROM pg_constraint
     WHERE conrelid = 'form_def'::regclass AND conname = 'form_def_id_organization_key';
    IF key_index IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f WHERE f.contype = 'f' AND f.confrelid = 'form_def'::regclass AND f.conindid = key_index
    ) THEN
        ALTER TABLE form_def DROP CONSTRAINT form_def_id_organization_key;
    END IF;
END
$$;
-- form-def-key:end
