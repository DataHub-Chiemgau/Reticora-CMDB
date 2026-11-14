-- WP-1116 (SEC-02, EVT-03, API-06): the finding model of phase 3.
--
-- security_finding gets its origin (source: cve_feed, agent, compliance,
-- config_change, manual), the CVE fields (cve_ids, cvss_score 0-10,
-- cvss_vector, kev, advisory_id), the deduplication key fingerprint (unique
-- per organization), the reason of its status and last_seen_at. kind also
-- takes unauthorized_change (CMP-01, WP-1124) and compliance_violation
-- (WP-1125). Existing rows get the fingerprint 'legacy:' || id and
-- last_seen_at = updated_at; a row written without fingerprint (raw SQL,
-- fixtures) gets a unique one of its own, the application always sets it
-- (security.Fingerprint). Indexes: (organization_id, ci_id, status) and a
-- GIN index on cve_ids for the filter cve. RLS, FORCE RLS, the scope
-- policies (000061, 000063), the scope trigger and the truncate guard
-- (000144) stay unchanged.

ALTER TABLE security_finding
    ADD COLUMN source text NOT NULL DEFAULT 'manual'
        CONSTRAINT security_finding_source_check CHECK (source IN ('cve_feed', 'agent', 'compliance', 'config_change', 'manual')),
    ADD COLUMN cve_ids text[] NOT NULL DEFAULT '{}',
    ADD COLUMN cvss_score numeric(3,1) NULL
        CONSTRAINT security_finding_cvss_score_check CHECK (cvss_score BETWEEN 0 AND 10),
    ADD COLUMN cvss_vector text NULL,
    ADD COLUMN kev boolean NOT NULL DEFAULT false,
    ADD COLUMN advisory_id text NULL,
    ADD COLUMN fingerprint text NULL,
    ADD COLUMN status_reason text NULL
        CONSTRAINT security_finding_status_reason_check CHECK (length(status_reason) <= 1000),
    ADD COLUMN last_seen_at timestamptz NULL;

UPDATE security_finding SET fingerprint = 'legacy:' || id::text, last_seen_at = updated_at;

ALTER TABLE security_finding
    ALTER COLUMN fingerprint SET DEFAULT ('unkeyed:' || gen_random_uuid()::text),
    ALTER COLUMN fingerprint SET NOT NULL,
    ALTER COLUMN last_seen_at SET DEFAULT now(),
    ALTER COLUMN last_seen_at SET NOT NULL,
    ADD CONSTRAINT security_finding_fingerprint_key UNIQUE (organization_id, fingerprint);

ALTER TABLE security_finding DROP CONSTRAINT security_finding_kind_check;
ALTER TABLE security_finding ADD CONSTRAINT security_finding_kind_check CHECK (kind IN
    ('vulnerability', 'outdated_software', 'outdated_firmware', 'missing_patch', 'unauthorized_change', 'compliance_violation'));

CREATE INDEX idx_security_finding_ci_status ON security_finding (organization_id, ci_id, status);
CREATE INDEX idx_security_finding_cve_ids ON security_finding USING gin (cve_ids);
