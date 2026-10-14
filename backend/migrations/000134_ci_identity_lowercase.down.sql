CREATE INDEX IF NOT EXISTS idx_ci_hostname ON ci (organization_id, lower(hostname)) WHERE hostname IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ci_fqdn ON ci (organization_id, lower(fqdn)) WHERE fqdn IS NOT NULL AND deleted_at IS NULL;

DROP INDEX IF EXISTS idx_ci_fqdn_lc;
DROP INDEX IF EXISTS idx_ci_hostname_lc;

ALTER TABLE ci DROP COLUMN fqdn_lc, DROP COLUMN hostname_lc;
