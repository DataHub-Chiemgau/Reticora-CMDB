-- Reverts 000296: the ADMP accounts, their triggers and function go. The
-- unique key on credential goes only when no foreign key references it any
-- more (since 000287 others do).
DROP TABLE admp_account;
DROP FUNCTION admp_account_credential_missing();

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'credential'::regclass AND conname = 'credential_id_organization_key')
       AND NOT EXISTS (SELECT 1 FROM pg_constraint f JOIN pg_constraint k ON k.conrelid = 'credential'::regclass
                         AND k.conname = 'credential_id_organization_key'
                       WHERE f.contype = 'f' AND f.confrelid = 'credential'::regclass AND f.conindid = k.conindid) THEN
        ALTER TABLE credential DROP CONSTRAINT credential_id_organization_key;
    END IF;
END
$$;
