-- Migration 000047: digital signature on assignment handover/return
--
-- The signature payload (image as base64 or S3 object key plus metadata) is
-- stored revision-safe with the assignment (spec §10: Übergabe mit digitaler
-- Unterschrift). One signature per event (checkout/return).
ALTER TABLE assignment ADD COLUMN IF NOT EXISTS checkout_signature JSONB;
ALTER TABLE assignment ADD COLUMN IF NOT EXISTS return_signature JSONB;

COMMENT ON COLUMN assignment.checkout_signature IS 'digital handover signature: {image_base64|object_key, signer_name, signed_at, method}';
COMMENT ON COLUMN assignment.return_signature IS 'digital return signature: {image_base64|object_key, signer_name, signed_at, method}';
