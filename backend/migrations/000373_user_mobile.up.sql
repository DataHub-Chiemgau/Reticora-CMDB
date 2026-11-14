-- Migration 000373: mobile number of a user as PII vault surrogate (NTF-02,
-- NTF-03, WP-1140, E-164).
--
-- The number itself lives only in the vault (pii_subject of kind user, data
-- {"mobile": "+49…"}); app_user keeps the surrogate. The composite foreign
-- key (Konvention 19) keeps the subject in the user's organization; deleting
-- the subject (crypto-shredding, anonymization) clears the column only.
-- pii_subject needs a unique key on (surrogate_id, organization_id): 000287
-- added pii_subject_surrogate_id_organization_key; the DO block adds
-- pii_subject_id_organization_key only when no such key exists. No column
-- holds the plaintext. app_user keeps its RLS and policies.

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'pii_subject'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['organization_id', 'surrogate_id']
    ) THEN
        ALTER TABLE pii_subject ADD CONSTRAINT pii_subject_id_organization_key UNIQUE (surrogate_id, organization_id);
    END IF;
END
$$;

ALTER TABLE app_user ADD COLUMN mobile_pii_subject_id UUID NULL;

ALTER TABLE app_user ADD CONSTRAINT app_user_mobile_pii_subject_id_fkey
    FOREIGN KEY (mobile_pii_subject_id, organization_id)
    REFERENCES pii_subject (surrogate_id, organization_id) ON DELETE SET NULL (mobile_pii_subject_id);

CREATE INDEX idx_app_user_mobile_pii_subject ON app_user (mobile_pii_subject_id)
    WHERE mobile_pii_subject_id IS NOT NULL;

COMMENT ON COLUMN app_user.mobile_pii_subject_id IS
    'Vault subject (kind user) holding the mobile number in E.164 (WP-1140); never plaintext here.';
