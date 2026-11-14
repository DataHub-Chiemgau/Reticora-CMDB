-- Migration 000373 down: removes the mobile surrogate of app_user. The
-- vault subjects stay (they are deleted with the user's anonymization or
-- the organization); the unique key on pii_subject goes only when this
-- migration added it and no foreign key uses it anymore.

DROP INDEX IF EXISTS idx_app_user_mobile_pii_subject;
ALTER TABLE app_user DROP CONSTRAINT IF EXISTS app_user_mobile_pii_subject_id_fkey;
ALTER TABLE app_user DROP COLUMN IF EXISTS mobile_pii_subject_id;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'pii_subject'::regclass AND conname = 'pii_subject_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'pii_subject'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['organization_id', 'surrogate_id']
    ) THEN
        ALTER TABLE pii_subject DROP CONSTRAINT pii_subject_id_organization_key;
    END IF;
END
$$;
