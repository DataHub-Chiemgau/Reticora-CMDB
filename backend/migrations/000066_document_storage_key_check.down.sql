-- Reverts WP-030: drops the prefix check and the report of the cleared keys
-- (WP-560). Keys cleared by the up migration stay empty: they may point at
-- another tenant's object.

ALTER TABLE document DROP CONSTRAINT IF EXISTS document_storage_key_org_prefix;

-- Databases that applied this migration before WP-560 and 000056 before E-26
-- have no migration_quarantine below version 89 (F-378, E-100).
DO $$
BEGIN
    IF to_regclass('public.migration_quarantine') IS NOT NULL THEN
        DELETE FROM migration_quarantine WHERE migration = '000066' AND source_table = 'document';
    END IF;
END
$$;
