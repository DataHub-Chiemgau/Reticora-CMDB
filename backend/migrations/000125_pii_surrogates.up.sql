-- Migration 000125: surrogates instead of plaintext personal data in the
-- history (SEC-05, AUD-04, WP-241).
--
-- Audit changes, the CI change history and event data keep a personal
-- value (e-mail, name, phone …) as a reference {"$pii": "<surrogate_id>"}
-- to a value subject in the vault (kind value); the API resolves it at
-- runtime. A value subject may belong to the subject of its person
-- (parent_id): deleting the person deletes their values with it, and the
-- history shows no personal data of them anymore.
--
-- Existing entries are sealed by the pii.backfill run (the master key is
-- not available to SQL). It may rewrite the changes of audit entries when
-- it declares itself with app.audit_pii_migration, and re-chains the
-- organization's entries from the first rewritten one (audit_rechain), so
-- the hash chain stays verifiable. Nothing else of an entry may change.

ALTER TABLE pii_subject ADD COLUMN parent_id UUID REFERENCES pii_subject (surrogate_id) ON DELETE CASCADE;
CREATE INDEX idx_pii_subject_parent ON pii_subject (parent_id) WHERE parent_id IS NOT NULL;
ALTER TABLE pii_subject DROP CONSTRAINT pii_subject_kind_check;
ALTER TABLE pii_subject ADD CONSTRAINT pii_subject_kind_check CHECK (kind IN ('user', 'contact', 'person', 'value'));

CREATE OR REPLACE FUNCTION reject_audit_log_mutation() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE'
       AND current_setting('app.audit_retention', true) = 'on'
       AND OLD.timestamp < now() - interval '10 years' THEN
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE'
       AND current_setting('app.audit_pii_migration', true) = 'on'
       AND (NEW.id, NEW.organization_id, NEW.seq, NEW.timestamp, NEW.actor_id, NEW.actor_type, NEW.action,
            NEW.resource_type, NEW.resource_id, NEW.source, NEW.request_id, NEW.correlation_id, NEW.created_at)
           IS NOT DISTINCT FROM
           (OLD.id, OLD.organization_id, OLD.seq, OLD.timestamp, OLD.actor_id, OLD.actor_type, OLD.action,
            OLD.resource_type, OLD.resource_id, OLD.source, OLD.request_id, OLD.correlation_id, OLD.created_at) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'audit_log rows are append-only';
END;
$$ LANGUAGE plpgsql;

-- audit_rechain recomputes previous_hash and hash of an organization's
-- entries from p_from_seq on, after their changes were sealed; it returns
-- how many entries it re-chained. The caller holds the chain lock.
CREATE FUNCTION audit_rechain(p_org UUID, p_from_seq BIGINT) RETURNS INTEGER
LANGUAGE plpgsql AS $$
DECLARE
    prev TEXT;
    entry RECORD;
    n INTEGER := 0;
BEGIN
    IF current_setting('app.audit_pii_migration', true) IS DISTINCT FROM 'on' THEN
        RAISE EXCEPTION 'audit_rechain runs only in the pii migration';
    END IF;
    SELECT hash INTO prev FROM audit_log WHERE organization_id = p_org AND seq < p_from_seq ORDER BY seq DESC LIMIT 1;
    IF NOT FOUND THEN
        -- The first entry of a purged chain keeps its anchor.
        SELECT previous_hash INTO prev FROM audit_log WHERE organization_id = p_org ORDER BY seq LIMIT 1;
    END IF;
    FOR entry IN SELECT * FROM audit_log WHERE organization_id = p_org AND seq >= p_from_seq ORDER BY seq LOOP
        UPDATE audit_log
        SET previous_hash = prev,
            hash = audit_entry_hash(entry.organization_id, entry.seq, entry.timestamp, entry.actor_id, entry.actor_type,
                entry.action, entry.resource_type, entry.resource_id, entry.changes, entry.source, entry.request_id,
                entry.correlation_id, prev)
        WHERE id = entry.id
        RETURNING hash INTO prev;
        n := n + 1;
    END LOOP;
    RETURN n;
END;
$$;
