-- Migration 000294: the source of a credential (CRD-02, WP-894, E-220).
-- stored keeps the envelope-encrypted secret (COL-02); vault_ref keeps a
-- reference into a vault connection (000293): path, optional version and
-- the map of the kind's fields to fields of the vault entry, and no secret.
-- The logins of the vault connections (vault_approle, vault_token) are only
-- stored. A reference uses a connection of the credential's client or an
-- org-wide one (credential_vault_connection_rule, 23514). RLS of credential
-- is unchanged.

ALTER TABLE credential
    ADD COLUMN source text NOT NULL DEFAULT 'stored' CONSTRAINT credential_source_check
        CHECK (source IN ('stored', 'vault_ref')),
    ADD COLUMN vault_connection_id uuid NULL,
    ADD COLUMN vault_path text NULL,
    ADD COLUMN vault_version integer NULL CONSTRAINT credential_vault_version_check CHECK (vault_version >= 1),
    ADD COLUMN field_map jsonb NULL;

ALTER TABLE credential ALTER COLUMN ciphertext DROP NOT NULL;

ALTER TABLE credential
    ADD CONSTRAINT credential_source_ciphertext_check CHECK ((source = 'stored') = (ciphertext IS NOT NULL)),
    ADD CONSTRAINT credential_vault_ref_check CHECK (
        (source = 'stored' AND vault_connection_id IS NULL AND vault_path IS NULL AND vault_version IS NULL AND field_map IS NULL)
        OR (source = 'vault_ref' AND vault_connection_id IS NOT NULL AND vault_path IS NOT NULL AND field_map IS NOT NULL)
    ),
    ADD CONSTRAINT credential_vault_login_stored_check CHECK (source = 'stored' OR kind NOT IN ('vault_approle', 'vault_token')),
    ADD CONSTRAINT credential_vault_connection_fkey FOREIGN KEY (vault_connection_id, organization_id)
        REFERENCES vault_connection (id, organization_id) ON DELETE RESTRICT;

CREATE INDEX idx_credential_vault_connection ON credential (vault_connection_id, organization_id)
    WHERE vault_connection_id IS NOT NULL;

-- A reference uses a connection of the credential's client or an org-wide
-- one; read with the scope widened (000141), the policies check the row.
CREATE FUNCTION check_credential_vault_connection() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
    connection_client uuid;
BEGIN
    IF NEW.vault_connection_id IS NULL THEN
        PERFORM app_scope_restore(previous);
        RETURN NEW;
    END IF;
    SELECT v.client_id INTO connection_client
      FROM vault_connection v WHERE v.id = NEW.vault_connection_id AND v.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    IF connection_client IS NOT NULL AND connection_client IS DISTINCT FROM NEW.client_id THEN
        RAISE EXCEPTION 'a vault reference uses a connection of the credential''s client or an org-wide one'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'credential_vault_connection_rule';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_credential_vault_connection
    BEFORE INSERT OR UPDATE OF vault_connection_id, client_id ON credential
    FOR EACH ROW EXECUTE FUNCTION check_credential_vault_connection();
