-- Reverts 000294: credential without source and vault reference, ciphertext
-- NOT NULL again. The revert is refused while a vault_ref credential exists
-- (it has no secret to keep); delete it first.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM credential WHERE source = 'vault_ref') THEN
        RAISE EXCEPTION 'credential holds vault references; migration 000294 cannot be reverted'
            USING ERRCODE = 'check_violation';
    END IF;
END
$$;

DROP TRIGGER trg_credential_vault_connection ON credential;
DROP FUNCTION check_credential_vault_connection();
DROP INDEX idx_credential_vault_connection;
ALTER TABLE credential
    DROP CONSTRAINT credential_vault_connection_fkey,
    DROP CONSTRAINT credential_vault_login_stored_check,
    DROP CONSTRAINT credential_vault_ref_check,
    DROP CONSTRAINT credential_source_ciphertext_check,
    DROP COLUMN field_map,
    DROP COLUMN vault_version,
    DROP COLUMN vault_path,
    DROP COLUMN vault_connection_id,
    DROP COLUMN source;
ALTER TABLE credential ALTER COLUMN ciphertext SET NOT NULL;
