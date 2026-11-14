-- Reverts 000315: the findings of the new kinds go, the former kind check
-- comes back, the columns and indexes of the phase 3 model are dropped.

DELETE FROM security_finding WHERE kind IN ('unauthorized_change', 'compliance_violation');

ALTER TABLE security_finding DROP CONSTRAINT security_finding_kind_check;
ALTER TABLE security_finding ADD CONSTRAINT security_finding_kind_check CHECK (kind IN
    ('vulnerability', 'outdated_software', 'outdated_firmware', 'missing_patch'));

DROP INDEX idx_security_finding_cve_ids;
DROP INDEX idx_security_finding_ci_status;

ALTER TABLE security_finding
    DROP CONSTRAINT security_finding_fingerprint_key,
    DROP COLUMN last_seen_at,
    DROP COLUMN status_reason,
    DROP COLUMN fingerprint,
    DROP COLUMN advisory_id,
    DROP COLUMN kev,
    DROP COLUMN cvss_vector,
    DROP COLUMN cvss_score,
    DROP COLUMN cve_ids,
    DROP COLUMN source;
