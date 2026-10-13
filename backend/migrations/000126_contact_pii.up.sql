-- Migration 000126: contacts through the PII vault (AUD-01, SEC-05, WP-242).
--
-- Name, phone and e-mail of a contact live in its vault subject (kind
-- contact); contact keeps the surrogate (pii_subject_id) and loses the
-- plaintext once the pii.backfill run has moved it (the master key is not
-- available to SQL). The contacts resource resolves the subject when it
-- reads. Search finds contacts by name, e-mail and phone through a blind
-- index: search_tokens holds keyed hashes (HMAC under a key derived from
-- the organization's data key) of the subject's words, recomputed when the
-- data key rotates. AUD-01 adds the 24/7 flag of a contact.

ALTER TABLE pii_subject ADD COLUMN search_tokens TEXT[] NOT NULL DEFAULT '{}';
CREATE INDEX idx_pii_subject_tokens ON pii_subject USING gin (search_tokens);

ALTER TABLE contact ADD COLUMN pii_subject_id UUID REFERENCES pii_subject (surrogate_id) ON DELETE SET NULL;
ALTER TABLE contact ADD COLUMN is_24_7 BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE contact ALTER COLUMN display_name DROP NOT NULL;
CREATE INDEX idx_contact_pii_subject ON contact (pii_subject_id) WHERE pii_subject_id IS NOT NULL;
