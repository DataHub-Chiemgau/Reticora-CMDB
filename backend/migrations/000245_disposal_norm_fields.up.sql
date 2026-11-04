-- Migration 000245: disposal records after E-55 (MGT-02, WP-751): standard
-- fields of DIN 66399, person surrogates, record_hash and references that
-- refuse the deletion of the documented objects.
--
-- 1. New columns, all nullable: protection_class (DIN 66399, 1-3),
--    security_level (^[POTEFH]-[1-7]$), supplier_id (service provider,
--    E-41), evidence_document_id (evidence document), performer_surrogate
--    and witness_surrogate (vault subjects of kind person; no foreign key on
--    pii_subject: SET NULL would be an UPDATE of the append-only row and
--    RESTRICT would block crypto-shredding) and record_hash. The handler
--    checks the required fields per method; there are no CHECKs for them.
-- 2. record_hash is SHA-256 (hex) of the canonical JSON of every business
--    field incl. id, organization_id, created_at, supplier_id,
--    evidence_document_id and the surrogate ids, never a plaintext name
--    (performed_by of former rows stays outside), hashed with
--    convert_to(..., 'UTF8') like audit_entry_hash since 000158 (WP-423).
--    client_id and site_id (000145) are left out: the scope propagation
--    changes them later. A BEFORE INSERT trigger sets the hash and
--    overwrites any value the writer passed; it stays verifiable after the
--    vault subjects are deleted.
-- 3. Former rows get their hash with the append-only guard lifted for this
--    run only (pattern 000145); their standard fields stay empty. Then
--    record_hash is NOT NULL.
-- 4. The references of asset and CI stay composite (K19, 000194) but refuse
--    the deletion with 23503 (ON DELETE RESTRICT) instead of an UPDATE the
--    append-only guard rejected with P0001. Supplier and evidence document
--    are referenced with the organization and RESTRICT as well. A record
--    names an asset or a CI (NOT VALID: former rows lost theirs through SET
--    NULL).
--
-- Scope columns, scope trigger, propagation and the policies of 000145 stay
-- unchanged, as do ENABLE/FORCE RLS and the append-only guard.

-- ─── 1. Columns ────────────────────────────────────────────────────────────────

ALTER TABLE disposal_record
    ADD COLUMN protection_class SMALLINT
        CONSTRAINT disposal_record_protection_class_check CHECK (protection_class BETWEEN 1 AND 3),
    ADD COLUMN security_level TEXT
        CONSTRAINT disposal_record_security_level_check CHECK (security_level ~ '^[POTEFH]-[1-7]$'),
    ADD COLUMN supplier_id UUID,
    ADD COLUMN evidence_document_id UUID,
    ADD COLUMN performer_surrogate UUID,
    ADD COLUMN witness_surrogate UUID,
    ADD COLUMN record_hash TEXT;

-- ─── 2. record_hash ────────────────────────────────────────────────────────────

CREATE FUNCTION disposal_record_hash(
    p_id UUID,
    p_organization_id UUID,
    p_asset_id UUID,
    p_ci_id UUID,
    p_method TEXT,
    p_protection_class SMALLINT,
    p_security_level TEXT,
    p_data_carrier TEXT,
    p_certificate_ref TEXT,
    p_supplier_id UUID,
    p_evidence_document_id UUID,
    p_performer_surrogate UUID,
    p_witness_surrogate UUID,
    p_performed_at TIMESTAMPTZ,
    p_notes TEXT,
    p_created_at TIMESTAMPTZ
) RETURNS TEXT
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT encode(digest(convert_to(
        '{"id":' || COALESCE(to_jsonb(p_id::text)::text, 'null')
        || ',"organization_id":' || COALESCE(to_jsonb(p_organization_id::text)::text, 'null')
        || ',"asset_id":' || COALESCE(to_jsonb(p_asset_id::text)::text, 'null')
        || ',"ci_id":' || COALESCE(to_jsonb(p_ci_id::text)::text, 'null')
        || ',"method":' || COALESCE(to_jsonb(p_method)::text, 'null')
        || ',"protection_class":' || COALESCE(p_protection_class::text, 'null')
        || ',"security_level":' || COALESCE(to_jsonb(p_security_level)::text, 'null')
        || ',"data_carrier":' || COALESCE(to_jsonb(p_data_carrier)::text, 'null')
        || ',"certificate_ref":' || COALESCE(to_jsonb(p_certificate_ref)::text, 'null')
        || ',"supplier_id":' || COALESCE(to_jsonb(p_supplier_id::text)::text, 'null')
        || ',"evidence_document_id":' || COALESCE(to_jsonb(p_evidence_document_id::text)::text, 'null')
        || ',"performer_surrogate":' || COALESCE(to_jsonb(p_performer_surrogate::text)::text, 'null')
        || ',"witness_surrogate":' || COALESCE(to_jsonb(p_witness_surrogate::text)::text, 'null')
        || ',"performed_at":' || COALESCE(to_jsonb(to_char(p_performed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'))::text, 'null')
        || ',"notes":' || COALESCE(to_jsonb(p_notes)::text, 'null')
        || ',"created_at":' || COALESCE(to_jsonb(to_char(p_created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'))::text, 'null')
        || '}', 'UTF8'), 'sha256'), 'hex');
$$;

CREATE FUNCTION disposal_record_set_hash() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    NEW.record_hash := disposal_record_hash(NEW.id, NEW.organization_id, NEW.asset_id, NEW.ci_id, NEW.method,
        NEW.protection_class, NEW.security_level, NEW.data_carrier, NEW.certificate_ref, NEW.supplier_id,
        NEW.evidence_document_id, NEW.performer_surrogate, NEW.witness_surrogate, NEW.performed_at, NEW.notes,
        NEW.created_at);
    RETURN NEW;
END
$$;

CREATE TRIGGER disposal_record_set_hash BEFORE INSERT ON disposal_record
    FOR EACH ROW EXECUTE FUNCTION disposal_record_set_hash();

-- ─── 3. Former rows ────────────────────────────────────────────────────────────

ALTER TABLE disposal_record DISABLE TRIGGER disposal_record_no_update;
UPDATE disposal_record SET record_hash = disposal_record_hash(id, organization_id, asset_id, ci_id, method,
    protection_class, security_level, data_carrier, certificate_ref, supplier_id, evidence_document_id,
    performer_surrogate, witness_surrogate, performed_at, notes, created_at);
ALTER TABLE disposal_record ENABLE TRIGGER disposal_record_no_update;

ALTER TABLE disposal_record ALTER COLUMN record_hash SET NOT NULL;

-- ─── 4. References ─────────────────────────────────────────────────────────────

ALTER TABLE disposal_record
    DROP CONSTRAINT disposal_record_asset_id_fkey,
    ADD CONSTRAINT disposal_record_asset_id_fkey FOREIGN KEY (asset_id, organization_id)
        REFERENCES asset (id, organization_id) ON DELETE RESTRICT,
    DROP CONSTRAINT disposal_record_ci_id_fkey,
    ADD CONSTRAINT disposal_record_ci_id_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE RESTRICT,
    ADD CONSTRAINT disposal_record_supplier_id_fkey FOREIGN KEY (supplier_id, organization_id)
        REFERENCES supplier (id, organization_id) ON DELETE RESTRICT,
    ADD CONSTRAINT disposal_record_evidence_document_id_fkey FOREIGN KEY (evidence_document_id, organization_id)
        REFERENCES document (id, organization_id) ON DELETE RESTRICT,
    ADD CONSTRAINT disposal_record_object_check CHECK (asset_id IS NOT NULL OR ci_id IS NOT NULL) NOT VALID;

-- The RESTRICT checks of a deleted asset, CI, supplier or document.
CREATE INDEX idx_disposal_record_asset ON disposal_record (asset_id) WHERE asset_id IS NOT NULL;
CREATE INDEX idx_disposal_record_ci ON disposal_record (ci_id) WHERE ci_id IS NOT NULL;
CREATE INDEX idx_disposal_record_supplier ON disposal_record (supplier_id) WHERE supplier_id IS NOT NULL;
CREATE INDEX idx_disposal_record_evidence_document ON disposal_record (evidence_document_id)
    WHERE evidence_document_id IS NOT NULL;
