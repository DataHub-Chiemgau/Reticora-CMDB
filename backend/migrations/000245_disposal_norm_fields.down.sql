-- Down of 000245: references of asset and CI with ON DELETE SET NULL (the
-- composite form of 000194), no supplier and evidence references, no object
-- CHECK, no hash trigger and function, no standard columns.

DROP INDEX IF EXISTS idx_disposal_record_evidence_document;
DROP INDEX IF EXISTS idx_disposal_record_supplier;
DROP INDEX IF EXISTS idx_disposal_record_ci;
DROP INDEX IF EXISTS idx_disposal_record_asset;

ALTER TABLE disposal_record
    DROP CONSTRAINT disposal_record_object_check,
    DROP CONSTRAINT disposal_record_evidence_document_id_fkey,
    DROP CONSTRAINT disposal_record_supplier_id_fkey,
    DROP CONSTRAINT disposal_record_ci_id_fkey,
    ADD CONSTRAINT disposal_record_ci_id_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE SET NULL (ci_id),
    DROP CONSTRAINT disposal_record_asset_id_fkey,
    ADD CONSTRAINT disposal_record_asset_id_fkey FOREIGN KEY (asset_id, organization_id)
        REFERENCES asset (id, organization_id) ON DELETE SET NULL (asset_id);

DROP TRIGGER disposal_record_set_hash ON disposal_record;
DROP FUNCTION disposal_record_set_hash();
DROP FUNCTION disposal_record_hash(UUID, UUID, UUID, UUID, TEXT, SMALLINT, TEXT, TEXT, TEXT, UUID, UUID, UUID, UUID,
    TIMESTAMPTZ, TEXT, TIMESTAMPTZ);

ALTER TABLE disposal_record
    DROP COLUMN record_hash,
    DROP COLUMN witness_surrogate,
    DROP COLUMN performer_surrogate,
    DROP COLUMN evidence_document_id,
    DROP COLUMN supplier_id,
    DROP COLUMN security_level,
    DROP COLUMN protection_class;
