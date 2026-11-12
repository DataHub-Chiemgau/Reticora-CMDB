-- Migration 000333: the global vulnerability catalog (SEC-02, WP-1117,
-- E-234 option 3).
--
-- The catalog belongs to the installation, not to an organization: the
-- advisories of CSAF 2.0 (BSI and vendors), NVD 2.0 with CISA KEV and the
-- offline bundle (WP-1118 to WP-1120) and their affected product and
-- version ranges. The tables have no organization_id and reference no
-- tenant table (exception to Konvention 5 and 19 with its own rule,
-- rls.SystemCatalogTables, rule system-catalog):
--   - every context reads them (the version match runs in the tenant
--     context, WP-1121): one SELECT policy USING (true);
--   - only the system context writes them (database.WithSystemWrite, the
--     importers): INSERT, UPDATE and DELETE policies require app.system in
--     USING and WITH CHECK; a tenant transaction is refused;
--   - an UPDATE or DELETE of a tenant transaction would only see no row to
--     change (the SELECT policy shows the rows, the USING of the write
--     policies hides them); the statement trigger
--     reticora_vuln_catalog_write_guard refuses it for reticora_app
--     outright with insufficient_privilege (42501), like the INSERT policy
--     (migrations and maintenance are not concerned, as with the TRUNCATE
--     guard);
--   - TRUNCATE ignores row level security: the TRUNCATE guard (000144).
--
-- vuln_source     one feed (csaf:<provider URL>, nvd, kev, bundle) with its
--                 sync state (ETag, Last-Modified, cursor, counters);
-- vuln_advisory   one advisory with its CVE ids, severity, CVSS and KEV;
--                 an advisory is replaced as a whole, there is no history;
-- vuln_affected   the affected ranges of an advisory per vendor and product
--                 (both normalized: lower case, white space as _).

CREATE TABLE vuln_source (
    id              text PRIMARY KEY,
    kind            text NOT NULL CONSTRAINT vuln_source_kind_check CHECK (kind IN ('csaf', 'nvd', 'kev', 'bundle')),
    url             text NOT NULL DEFAULT '',
    etag            text NOT NULL DEFAULT '',
    last_modified   text NOT NULL DEFAULT '',
    cursor          text NOT NULL DEFAULT '',
    synced_at       timestamptz NULL,
    documents_total bigint NOT NULL DEFAULT 0 CONSTRAINT vuln_source_documents_total_check CHECK (documents_total >= 0),
    errors_total    bigint NOT NULL DEFAULT 0 CONSTRAINT vuln_source_errors_total_check CHECK (errors_total >= 0),
    last_error      text NOT NULL DEFAULT '',
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE vuln_advisory (
    id           text PRIMARY KEY,
    source_id    text NOT NULL REFERENCES vuln_source (id) ON DELETE CASCADE,
    cve_ids      text[] NOT NULL DEFAULT '{}',
    title        text NOT NULL DEFAULT '',
    severity     text NULL CONSTRAINT vuln_advisory_severity_check CHECK (severity IN ('low', 'medium', 'high', 'critical')),
    cvss_score   numeric(3,1) NULL CONSTRAINT vuln_advisory_cvss_score_check CHECK (cvss_score BETWEEN 0 AND 10),
    cvss_vector  text NOT NULL DEFAULT '',
    kev          boolean NOT NULL DEFAULT false,
    published_at timestamptz NULL,
    modified_at  timestamptz NULL,
    url          text NOT NULL DEFAULT '',
    withdrawn    boolean NOT NULL DEFAULT false
);
CREATE INDEX idx_vuln_advisory_source ON vuln_advisory (source_id);
CREATE INDEX idx_vuln_advisory_cve_ids ON vuln_advisory USING gin (cve_ids);

CREATE TABLE vuln_affected (
    id                      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    advisory_id             text NOT NULL REFERENCES vuln_advisory (id) ON DELETE CASCADE,
    vendor                  text NOT NULL CONSTRAINT vuln_affected_vendor_check CHECK (vendor <> '' AND vendor = lower(vendor) AND vendor !~ '\s'),
    product                 text NOT NULL CONSTRAINT vuln_affected_product_check CHECK (product <> '' AND product = lower(product) AND product !~ '\s'),
    version_scheme          text NOT NULL DEFAULT 'generic',
    version_exact           text NULL,
    version_start           text NULL,
    version_start_inclusive boolean NOT NULL DEFAULT true,
    version_end             text NULL,
    version_end_inclusive   boolean NOT NULL DEFAULT false,
    fixed_version           text NULL,
    cpe                     text NULL
);
CREATE INDEX idx_vuln_affected_product ON vuln_affected (vendor, product);
CREATE INDEX idx_vuln_affected_advisory ON vuln_affected (advisory_id);

-- vuln-catalog-policies:begin
CREATE FUNCTION reticora_vuln_catalog_write_guard() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF current_user = 'reticora_app' AND current_setting('app.system', true) IS DISTINCT FROM 'on' THEN
        RAISE EXCEPTION '% on % requires the system context', TG_OP, TG_TABLE_NAME
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NULL;
END
$$;

ALTER TABLE vuln_source ENABLE ROW LEVEL SECURITY;
ALTER TABLE vuln_source FORCE ROW LEVEL SECURITY;
CREATE POLICY vuln_source_read ON vuln_source FOR SELECT USING (true);
CREATE POLICY vuln_source_system_insert ON vuln_source FOR INSERT
    WITH CHECK (current_setting('app.system', true) = 'on');
CREATE POLICY vuln_source_system_update ON vuln_source FOR UPDATE
    USING (current_setting('app.system', true) = 'on')
    WITH CHECK (current_setting('app.system', true) = 'on');
CREATE POLICY vuln_source_system_delete ON vuln_source FOR DELETE
    USING (current_setting('app.system', true) = 'on');
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON vuln_source
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
CREATE TRIGGER reticora_vuln_catalog_write_guard BEFORE UPDATE OR DELETE ON vuln_source
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_vuln_catalog_write_guard();

ALTER TABLE vuln_advisory ENABLE ROW LEVEL SECURITY;
ALTER TABLE vuln_advisory FORCE ROW LEVEL SECURITY;
CREATE POLICY vuln_advisory_read ON vuln_advisory FOR SELECT USING (true);
CREATE POLICY vuln_advisory_system_insert ON vuln_advisory FOR INSERT
    WITH CHECK (current_setting('app.system', true) = 'on');
CREATE POLICY vuln_advisory_system_update ON vuln_advisory FOR UPDATE
    USING (current_setting('app.system', true) = 'on')
    WITH CHECK (current_setting('app.system', true) = 'on');
CREATE POLICY vuln_advisory_system_delete ON vuln_advisory FOR DELETE
    USING (current_setting('app.system', true) = 'on');
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON vuln_advisory
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
CREATE TRIGGER reticora_vuln_catalog_write_guard BEFORE UPDATE OR DELETE ON vuln_advisory
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_vuln_catalog_write_guard();

ALTER TABLE vuln_affected ENABLE ROW LEVEL SECURITY;
ALTER TABLE vuln_affected FORCE ROW LEVEL SECURITY;
CREATE POLICY vuln_affected_read ON vuln_affected FOR SELECT USING (true);
CREATE POLICY vuln_affected_system_insert ON vuln_affected FOR INSERT
    WITH CHECK (current_setting('app.system', true) = 'on');
CREATE POLICY vuln_affected_system_update ON vuln_affected FOR UPDATE
    USING (current_setting('app.system', true) = 'on')
    WITH CHECK (current_setting('app.system', true) = 'on');
CREATE POLICY vuln_affected_system_delete ON vuln_affected FOR DELETE
    USING (current_setting('app.system', true) = 'on');
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON vuln_affected
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
CREATE TRIGGER reticora_vuln_catalog_write_guard BEFORE UPDATE OR DELETE ON vuln_affected
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_vuln_catalog_write_guard();
-- vuln-catalog-policies:end
