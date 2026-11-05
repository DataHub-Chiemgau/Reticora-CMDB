-- WP-276 (AST-02, E-51, E-124): digital signatures of assignments, returns
-- and transfers. The image is a document of the category signature linked
-- to its movement (document_link entity_type stock_movement, WP-744); the
-- movement carries document_id, signature_hash and the signer.
--
-- 1. document_category_check admits signature next to the eight values of
--    000011.
-- 2. stock_movement.signer_user_id (a user id as pseudonymous key, WP-241)
--    and signer_surrogate (a pii_subject surrogate: contact or external
--    person), both without foreign key like actor_id: a key on pii_subject
--    would prevent crypto shredding. At most one of them; a movement with
--    signature_hash has a document and exactly one signer. The append-only
--    trigger and the policies of stock_movement stay unchanged; the target
--    type stock_movement of document_link and its branch in
--    derive_document_link_scope() came with 000242 (WP-744).

ALTER TABLE document DROP CONSTRAINT document_category_check;
ALTER TABLE document ADD CONSTRAINT document_category_check CHECK (category IN (
    'general', 'contract', 'invoice', 'manual', 'diagram', 'certificate', 'policy', 'other', 'signature'
));

ALTER TABLE stock_movement
    ADD COLUMN signer_user_id UUID,
    ADD COLUMN signer_surrogate UUID,
    ADD CONSTRAINT stock_movement_signer_check CHECK (num_nonnulls(signer_user_id, signer_surrogate) <= 1),
    ADD CONSTRAINT stock_movement_signature_check CHECK (signature_hash IS NULL
        OR (document_id IS NOT NULL AND num_nonnulls(signer_user_id, signer_surrogate) = 1));
