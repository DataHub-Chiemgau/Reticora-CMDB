-- Migration 000374: the address of the deletion protocol (TLC-06, WP-1156,
-- E-166).
--
-- When an organization's deletion is requested, the address and language
-- of the requester are kept here until the deletion protocol is sent: the
-- purge deletes app_user and the vault of the organization, so the address
-- has to survive outside it. The address is encrypted with a data key of
-- its own (recipient_enc), which the master key wraps (wrapped_dek); SQL
-- never sees the plaintext. A cancelled request, a sent protocol and a
-- protocol given up after three attempts delete the row.
--
-- Like signup_request the table is an operator table without
-- organization_id (rls.OperatorTables, RuleOperatorOnly): row level
-- security binds it to the operator context (app.operator,
-- database.WithOperatorTenant on the request, database.WithOperator in the
-- purge); tenant transactions see and write nothing. target_org has no
-- foreign key, so the row outlives the purge of the organization.

CREATE TABLE org_deletion_notice (
    target_org    UUID PRIMARY KEY,
    recipient_enc BYTEA NOT NULL CHECK (octet_length(recipient_enc) > 0),
    wrapped_dek   BYTEA NOT NULL CHECK (octet_length(wrapped_dek) > 0),
    locale        TEXT NOT NULL DEFAULT 'de' CHECK (locale <> ''),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts      INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0)
);

ALTER TABLE org_deletion_notice ENABLE ROW LEVEL SECURITY;
ALTER TABLE org_deletion_notice FORCE ROW LEVEL SECURITY;
CREATE POLICY org_deletion_notice_operator ON org_deletion_notice
    USING (current_setting('app.operator', true) = 'on')
    WITH CHECK (current_setting('app.operator', true) = 'on');

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON org_deletion_notice
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

COMMENT ON TABLE org_deletion_notice IS
    'Encrypted address and language of the requester of an organization deletion until the protocol is sent (WP-1156); operator table.';
