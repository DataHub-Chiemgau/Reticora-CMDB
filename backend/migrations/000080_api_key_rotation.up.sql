-- Migration 000080: API keys after AUT-04 (WP-067).
--
-- environment: rk_live_ or rk_test_ keys.
-- rotated_from: the key a rotation replaced; the old key stays valid until
-- its expires_at (overlap, SEC-06).
-- A hash index on key_prefix serves the identification of a presented key.
-- The identification runs before any tenant is known, in a read-only system
-- transaction (database.WithSystem, E-08): the SELECT-only policy below lets
-- the system flag find the row by its prefix; every other access runs in the
-- tenant transaction of the key's organization.

ALTER TABLE api_key
    ADD COLUMN environment TEXT NOT NULL DEFAULT 'live',
    ADD COLUMN rotated_from UUID REFERENCES api_key(id) ON DELETE SET NULL,
    ADD CONSTRAINT api_key_environment_check CHECK (environment IN ('live', 'test'));

CREATE INDEX idx_api_key_prefix_hash ON api_key USING hash (key_prefix);

CREATE POLICY api_key_system_select ON api_key FOR SELECT
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid
           OR current_setting('app.system', true) = 'on');
