-- NFR-01/NFR-03 (WP-206): the identity lookup by hostname and FQDN compared
-- lower(hostname). lower() is not leakproof, so under the row-level policies
-- of the application role PostgreSQL may not evaluate it before the policy
-- and never used idx_ci_hostname/idx_ci_fqdn: every ingested record scanned
-- the whole inventory. Stored lower-case columns are compared with the
-- leakproof text equality and are indexable under RLS. The indexes cover
-- deleted CIs too (resurrection, CI-13).
ALTER TABLE ci
    ADD COLUMN hostname_lc TEXT GENERATED ALWAYS AS (lower(hostname)) STORED,
    ADD COLUMN fqdn_lc TEXT GENERATED ALWAYS AS (lower(fqdn)) STORED;

CREATE INDEX IF NOT EXISTS idx_ci_hostname_lc ON ci (organization_id, hostname_lc) WHERE hostname_lc IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_ci_fqdn_lc ON ci (organization_id, fqdn_lc) WHERE fqdn_lc IS NOT NULL;

DROP INDEX IF EXISTS idx_ci_hostname;
DROP INDEX IF EXISTS idx_ci_fqdn;
