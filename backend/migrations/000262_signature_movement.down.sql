-- Reverts 000262 (WP-276). It refuses while signatures exist: dropping the
-- columns would lose the binding of image, movement and signer.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM document WHERE category = 'signature') THEN
        RAISE EXCEPTION 'documents of the category signature exist; 000262 cannot be reverted';
    END IF;
    IF EXISTS (SELECT 1 FROM stock_movement WHERE signature_hash IS NOT NULL) THEN
        RAISE EXCEPTION 'stock movements with signature_hash exist; 000262 cannot be reverted';
    END IF;
END
$$;

ALTER TABLE stock_movement
    DROP CONSTRAINT stock_movement_signature_check,
    DROP CONSTRAINT stock_movement_signer_check,
    DROP COLUMN signer_surrogate,
    DROP COLUMN signer_user_id;

ALTER TABLE document DROP CONSTRAINT document_category_check;
ALTER TABLE document ADD CONSTRAINT document_category_check CHECK (category IN (
    'general', 'contract', 'invoice', 'manual', 'diagram', 'certificate', 'policy', 'other'
));
