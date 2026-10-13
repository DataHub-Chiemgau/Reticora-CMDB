-- Migration 000128: collector PKI (COL-04, WP-145).
--
-- collector_ca holds the internal CA that signs the collector
-- certificates: its certificate and its private key, encrypted with a data
-- key of its own wrapped by the master key. One CA is active; after a CA
-- roll-over the former one stays trusted as previous (two trust anchors)
-- until it is retired. The CA spans organizations, so the table belongs to
-- the operator context like scheduler_run.
--
-- collector_certificate records every issued certificate (CN = collector
-- id, 90 days): disabling a collector revokes its certificates, and the
-- revocation list is built from the revoked ones that have not expired;
-- it spans organizations and is read with the system flag.

CREATE TABLE collector_ca (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cert_pem       TEXT NOT NULL,
    key_ciphertext BYTEA NOT NULL,
    key_dek        BYTEA NOT NULL,
    not_after      TIMESTAMPTZ NOT NULL,
    state          TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'previous', 'retired')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX collector_ca_one_active ON collector_ca (state) WHERE state = 'active';

ALTER TABLE collector_ca ENABLE ROW LEVEL SECURITY;
ALTER TABLE collector_ca FORCE ROW LEVEL SECURITY;
CREATE POLICY collector_ca_operator ON collector_ca
    USING (current_setting('app.operator', true) = 'on')
    WITH CHECK (current_setting('app.operator', true) = 'on');

CREATE TABLE collector_certificate (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id   UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    collector_id      UUID NOT NULL REFERENCES collector (id) ON DELETE CASCADE,
    serial            TEXT NOT NULL UNIQUE,
    fingerprint       TEXT NOT NULL,
    not_before        TIMESTAMPTZ NOT NULL,
    not_after         TIMESTAMPTZ NOT NULL,
    revoked_at        TIMESTAMPTZ,
    revocation_reason TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_collector_certificate_collector ON collector_certificate (organization_id, collector_id);
CREATE INDEX idx_collector_certificate_revoked ON collector_certificate (not_after) WHERE revoked_at IS NOT NULL;

ALTER TABLE collector_certificate ENABLE ROW LEVEL SECURITY;
ALTER TABLE collector_certificate FORCE ROW LEVEL SECURITY;
CREATE POLICY collector_certificate_isolation ON collector_certificate FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
CREATE POLICY collector_certificate_system_select ON collector_certificate FOR SELECT
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid
        OR current_setting('app.system', true) = 'on');
