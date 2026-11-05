-- Migration 000268: the legacy signatures become signature documents
-- (AST-02, E-51, WP-749).
--
-- The system run signature.migrate stores the image of a legacy signature
-- of assignment_signature_legacy (000266) as a document of the category
-- signature linked to its movement and replaces the payload by its
-- image-free form {document_id, signer_surrogate, signed_at, method}: the
-- signer only as vault surrogate, payload_sha256 stays as the evidence of
-- the original. The guard of 000266 refused every UPDATE but
-- payload := NULL; it now also accepts this replacement, once: the old
-- payload is an object without document_id, the new one an object with
-- exactly the four keys (document_id a UUID, signer_surrogate a UUID or
-- null, signed_at and method strings of at most 64 characters or null, so
-- no image fits), and id, organization_id, movement_id, kind,
-- payload_sha256 and created_at stay. DELETE stays refused; RLS and
-- policies are unchanged. Down restores the guard of 000266; replaced
-- payloads stay.

CREATE OR REPLACE FUNCTION guard_assignment_signature_legacy() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    uuid_pattern constant text := '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';
BEGIN
    IF TG_OP = 'UPDATE'
       AND NEW.id = OLD.id AND NEW.organization_id = OLD.organization_id
       AND NEW.movement_id = OLD.movement_id AND NEW.kind = OLD.kind
       AND NEW.payload_sha256 = OLD.payload_sha256 AND NEW.created_at = OLD.created_at THEN
        IF NEW.payload IS NULL THEN
            RETURN NEW;
        END IF;
        IF jsonb_typeof(OLD.payload) = 'object' AND NOT OLD.payload ? 'document_id'
           AND jsonb_typeof(NEW.payload) = 'object'
           AND (SELECT array_agg(k ORDER BY k) FROM jsonb_object_keys(NEW.payload) AS k)
               = ARRAY['document_id', 'method', 'signed_at', 'signer_surrogate']
           AND jsonb_typeof(NEW.payload -> 'document_id') = 'string'
           AND NEW.payload ->> 'document_id' ~ uuid_pattern
           AND (jsonb_typeof(NEW.payload -> 'signer_surrogate') = 'null'
                OR (jsonb_typeof(NEW.payload -> 'signer_surrogate') = 'string'
                    AND NEW.payload ->> 'signer_surrogate' ~ uuid_pattern))
           AND (jsonb_typeof(NEW.payload -> 'signed_at') = 'null'
                OR (jsonb_typeof(NEW.payload -> 'signed_at') = 'string' AND length(NEW.payload ->> 'signed_at') <= 64))
           AND (jsonb_typeof(NEW.payload -> 'method') = 'null'
                OR (jsonb_typeof(NEW.payload -> 'method') = 'string' AND length(NEW.payload ->> 'method') <= 64)) THEN
            RETURN NEW;
        END IF;
    END IF;
    RAISE EXCEPTION 'assignment_signature_legacy rows are kept; payload may only be cleared or replaced once by its image-free form'
        USING ERRCODE = '42501';
END
$$;
