-- Reverts 000268_signature_legacy_payload: the guard of 000266 again
-- (DELETE refused, an UPDATE may only clear payload). Payloads replaced by
-- their image-free form stay.

CREATE OR REPLACE FUNCTION guard_assignment_signature_legacy() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.payload IS NULL
       AND NEW.id = OLD.id AND NEW.organization_id = OLD.organization_id
       AND NEW.movement_id = OLD.movement_id AND NEW.kind = OLD.kind
       AND NEW.payload_sha256 = OLD.payload_sha256 AND NEW.created_at = OLD.created_at THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'assignment_signature_legacy rows are kept; only payload may be cleared'
        USING ERRCODE = '42501';
END
$$;
