-- Migration 000119: audit retention (NFR-06, AUD-09, WP-135).
--
-- audit_log stays append-only, with one exception: the retention run may
-- delete entries older than ten years (the legal minimum of NFR-06) when
-- it declares itself with app.audit_retention. It deletes the oldest
-- entries of an organization's chain and appends an audit.retention entry
-- that anchors the rest of the chain (audit.Purge), so the chain stays
-- verifiable.

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
