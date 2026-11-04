-- Migration 000222: scan status of document versions (WP-733, SEC-09,
-- MGT-01, E-42).
--
-- 1. document_version.scan_status and scanned_at come from 000220 (WP-731).
--    scan_signature holds the signature name of a finding of the malware
--    scan (WP-734); NULL without a finding.
-- 2. The partial index idx_document_version_scan_open serves the open scans
--    (pending_scan, scan_failed) per organization, i.e. PendingScans and the
--    sweep of WP-734. CREATE INDEX IF NOT EXISTS with a fixed name, so the
--    operator can build it CONCURRENTLY before the update (convention
--    "Indizes ohne lange Sperren" in docs/schema-baseline.md).
-- 3. Backfill, fail-closed after E-42: every version without a scan
--    (scanned_at IS NULL) is pending_scan; the sweep of WP-734 scans it.
--    000220 already took over the content of phase 1 as pending_scan.
-- Quarantine is a status: blob and document_link stay. Row level security
-- is unchanged (policies of document and document_link from 000064, 000139,
-- 000146 and 000217, document_version_isolation from 000220).

ALTER TABLE document_version ADD COLUMN scan_signature TEXT;

CREATE INDEX IF NOT EXISTS idx_document_version_scan_open
    ON document_version (organization_id, scan_status) WHERE scan_status <> 'clean';

UPDATE document_version SET scan_status = 'pending_scan'
 WHERE scanned_at IS NULL AND scan_status <> 'pending_scan';
