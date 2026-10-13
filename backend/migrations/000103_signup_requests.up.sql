-- Migration 000103: self-signup requests (TLC-01, API-05, WP-111).
--
-- A signup request exists before its organization does, so the table has no
-- tenant column. Like operator_audit it is an operator table: row level
-- security binds it to the operator context (app.operator,
-- database.WithOperator / WithOrganizationSetup); tenant transactions see and
-- write nothing (documented exception, rls.OperatorTables). Only the SHA-256
-- hash of the verification token is stored; the password is never stored
-- (it is set directly in the IdP on verification).

CREATE TABLE signup_request (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email             TEXT NOT NULL CHECK (email <> ''),
    organization_name TEXT NOT NULL CHECK (organization_name <> ''),
    display_name      TEXT NOT NULL,
    token_hash        TEXT NOT NULL UNIQUE,
    status            TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'completed')),
    expires_at        TIMESTAMPTZ NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at      TIMESTAMPTZ,
    -- The organization the request created; not a tenant key.
    completed_org_id  UUID REFERENCES organization(id) ON DELETE SET NULL,
    CHECK ((status = 'completed') = (completed_at IS NOT NULL))
);
CREATE INDEX idx_signup_request_email ON signup_request (lower(email), created_at);

ALTER TABLE signup_request ENABLE ROW LEVEL SECURITY;
ALTER TABLE signup_request FORCE ROW LEVEL SECURITY;
CREATE POLICY signup_request_operator ON signup_request
    USING (current_setting('app.operator', true) = 'on')
    WITH CHECK (current_setting('app.operator', true) = 'on');

-- Self-signup records its organization creation in operator_audit under the
-- operator kind 'signup' (operator_id = the signup request).
ALTER TABLE operator_audit DROP CONSTRAINT operator_audit_operator_kind_check;
ALTER TABLE operator_audit ADD CONSTRAINT operator_audit_operator_kind_check
    CHECK (operator_kind IN ('oidc', 'break_glass', 'signup'));
