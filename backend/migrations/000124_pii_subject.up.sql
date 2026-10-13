-- Migration 000124: PII vault (SEC-05, CH16, WP-240).
--
-- Personal data of users and contacts lives encrypted in pii_subject; the
-- other tables reference a person only by the surrogate id (WP-241,
-- WP-242). The payload is AES-GCM under the organization's data key
-- (org_dek, envelope encryption with the master key); dek_ref is the
-- key_version of the org_dek it was encrypted with, so a DEK rotation
-- re-encrypts the vault together with the credentials. Deleting a row
-- leaves nothing to resolve (the basis of crypto-shredding; the deletion
-- workflows follow in P3).

CREATE TABLE pii_subject (
    surrogate_id      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id   UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    kind              TEXT NOT NULL CHECK (kind IN ('user', 'contact', 'person')),
    encrypted_payload BYTEA NOT NULL,
    dek_ref           INTEGER NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_pii_subject_org ON pii_subject (organization_id, kind);

CREATE TRIGGER trg_pii_subject_updated_at BEFORE UPDATE ON pii_subject
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE pii_subject ENABLE ROW LEVEL SECURITY;
ALTER TABLE pii_subject FORCE ROW LEVEL SECURITY;
CREATE POLICY pii_subject_isolation ON pii_subject FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
