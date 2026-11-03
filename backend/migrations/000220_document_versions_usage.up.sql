-- Migration 000220: document versions and storage usage per organization
-- (WP-731, MGT-01, API-06, E-38, E-44).
--
-- 1. document_version holds one row per uploaded content of a document:
--    version number (unique per document), storage key, size, MIME type,
--    sha256 checksum (NULL only for the versions of the backfill), the scan
--    status of SEC-09 (pending_scan, clean, infected, scan_failed; WP-733
--    and WP-734 evaluate and change it), scanned_at and the uploader as a
--    surrogate without foreign key, like job.created_by (convention 16).
--    document.uploaded_by stays as it is. The reference to document carries
--    the organization, (document_id, organization_id) REFERENCES document
--    (id, organization_id) (K19), so document gets UNIQUE (id,
--    organization_id).
-- 2. document.version, storage_key, file_size and mime_type mirror the
--    current version (the highest number); document.version is the version
--    number, no ETag.
-- 3. Backfill before the triggers: every document with content becomes its
--    version document.version (size file_size, MIME mime_type, checksum NULL,
--    pending_scan, surrogate uploaded_by), legacy keys included.
-- 4. Row level security with ENABLE and FORCE, truncate guard of WP-352. The
--    policy of document_version follows training_assignment_isolation
--    (000065): the organization and, as soon as a client, site or team scope
--    is set, the visibility of the document (its policies of 000217). The
--    BEFORE trigger require_visible_parent (000139, with the team argument of
--    000217) lets a principal restricted in the client, site or team scope
--    add versions only to linked documents. document_version_storage_key
--    checks the key like document_storage_key_org_prefix (000066), but for
--    the document of the row: org/<org>/documents/<document>/... or the
--    legacy documents/<org>/<document>/..., never "..". It is a trigger and
--    no CHECK because the RLS matrix synthesizes rows under
--    session_replication_role = replica.
-- 5. organization_usage counts the stored bytes of the organization
--    (storage_bytes, never negative). Its policy checks only app.org_id, so
--    the usage is counted org-wide whatever the scope of the caller; the
--    document upload checks it against max_storage_mb in the transaction
--    of the new version. Backfill: the sum of the version sizes per
--    organization.

-- ─── 1. Versions ───────────────────────────────────────────────────────────────

ALTER TABLE document ADD CONSTRAINT document_id_organization_key UNIQUE (id, organization_id);

CREATE TABLE document_version (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id       UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    document_id           UUID NOT NULL,
    version               INTEGER NOT NULL
        CONSTRAINT document_version_version_check CHECK (version > 0),
    storage_key           TEXT NOT NULL,
    size                  BIGINT NOT NULL
        CONSTRAINT document_version_size_check CHECK (size >= 0),
    mime                  TEXT NOT NULL,
    checksum_sha256       TEXT
        CONSTRAINT document_version_checksum_check CHECK (checksum_sha256 ~ '^[0-9a-f]{64}$'),
    scan_status           TEXT NOT NULL DEFAULT 'pending_scan'
        CONSTRAINT document_version_scan_status_check CHECK (scan_status IN ('pending_scan', 'clean', 'infected', 'scan_failed')),
    scanned_at            TIMESTAMPTZ,
    uploaded_by_surrogate UUID,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT document_version_document_id_fkey FOREIGN KEY (document_id, organization_id)
        REFERENCES document (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT document_version_document_id_version_key UNIQUE (document_id, version)
);
CREATE INDEX idx_document_version_org ON document_version (organization_id);

INSERT INTO document_version (organization_id, document_id, version, storage_key, size, mime,
                              checksum_sha256, scan_status, uploaded_by_surrogate, created_at)
SELECT organization_id, id, version, storage_key, file_size, mime_type,
       NULL, 'pending_scan', uploaded_by, updated_at
  FROM document
 WHERE storage_key <> '';

-- ─── 2. Usage ──────────────────────────────────────────────────────────────────

CREATE TABLE organization_usage (
    organization_id UUID PRIMARY KEY REFERENCES organization (id) ON DELETE CASCADE,
    storage_bytes   BIGINT NOT NULL DEFAULT 0
        CONSTRAINT organization_usage_storage_bytes_check CHECK (storage_bytes >= 0),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO organization_usage (organization_id, storage_bytes)
SELECT organization_id, sum(size) FROM document_version GROUP BY organization_id;

-- ─── 3. Triggers ───────────────────────────────────────────────────────────────

CREATE FUNCTION document_version_storage_key() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF NOT ((NEW.storage_key LIKE 'org/' || NEW.organization_id::text || '/documents/' || NEW.document_id::text || '/%'
             OR NEW.storage_key LIKE 'documents/' || NEW.organization_id::text || '/' || NEW.document_id::text || '/%')
            AND position('..' IN NEW.storage_key) = 0) THEN
        RAISE EXCEPTION 'storage key of document version % is outside the document %', NEW.id, NEW.document_id
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER document_version_storage_key BEFORE INSERT OR UPDATE OF storage_key, document_id ON document_version
    FOR EACH ROW EXECUTE FUNCTION document_version_storage_key();
CREATE TRIGGER document_version_parent_visible BEFORE INSERT OR UPDATE ON document_version
    FOR EACH ROW EXECUTE FUNCTION require_visible_parent('document', 'document_id', 'link_count', 'team');

-- ─── 4. Row level security ─────────────────────────────────────────────────────

ALTER TABLE document_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE document_version FORCE ROW LEVEL SECURITY;
CREATE POLICY document_version_isolation ON document_version
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND ((NULLIF(current_setting('app.client_scope', true), '') IS NULL
              AND NULLIF(current_setting('app.site_scope', true), '') IS NULL
              AND NULLIF(current_setting('app.team_scope', true), '') IS NULL)
             OR EXISTS (SELECT 1 FROM document d WHERE d.id = document_version.document_id))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::UUID
        AND ((NULLIF(current_setting('app.client_scope', true), '') IS NULL
              AND NULLIF(current_setting('app.site_scope', true), '') IS NULL
              AND NULLIF(current_setting('app.team_scope', true), '') IS NULL)
             OR EXISTS (SELECT 1 FROM document d WHERE d.id = document_version.document_id))
    );
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON document_version
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE organization_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE organization_usage FORCE ROW LEVEL SECURITY;
CREATE POLICY organization_usage_isolation ON organization_usage
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON organization_usage
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
