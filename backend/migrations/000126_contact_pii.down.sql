-- The plaintext of sealed contacts stays in the vault; their name falls
-- back to a placeholder.
DROP INDEX IF EXISTS idx_contact_pii_subject;
UPDATE contact SET display_name = 'contact ' || id::text WHERE display_name IS NULL;
ALTER TABLE contact ALTER COLUMN display_name SET NOT NULL;
ALTER TABLE contact DROP COLUMN is_24_7;
ALTER TABLE contact DROP COLUMN pii_subject_id;

DROP INDEX IF EXISTS idx_pii_subject_tokens;
ALTER TABLE pii_subject DROP COLUMN search_tokens;
