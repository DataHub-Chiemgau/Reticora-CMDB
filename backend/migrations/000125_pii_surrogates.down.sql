DROP FUNCTION IF EXISTS audit_rechain(UUID, BIGINT);

CREATE OR REPLACE FUNCTION reject_audit_log_mutation() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE'
       AND current_setting('app.audit_retention', true) = 'on'
       AND OLD.timestamp < now() - interval '10 years' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'audit_log rows are append-only';
END;
$$ LANGUAGE plpgsql;

-- The value subjects of the history go with the column; references to
-- them resolve to nothing afterwards.
DELETE FROM pii_subject WHERE kind = 'value';
ALTER TABLE pii_subject DROP CONSTRAINT pii_subject_kind_check;
ALTER TABLE pii_subject ADD CONSTRAINT pii_subject_kind_check CHECK (kind IN ('user', 'contact', 'person'));
DROP INDEX IF EXISTS idx_pii_subject_parent;
ALTER TABLE pii_subject DROP COLUMN parent_id;
