-- Migration 000193 down: the outbox numbers from max(sequence) per
-- organization again (the trigger function of 000120). The counters go
-- with their table, the policies and the TRUNCATE guard with it. An
-- organization whose newest event the cleanup removed continues from its
-- newest remaining event, as before 000193.

CREATE OR REPLACE FUNCTION event_outbox_number() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(1165389428, audit_org_lock_key(NEW.organization_id));
    SELECT COALESCE(max(sequence), 0) + 1 INTO NEW.sequence
    FROM event_outbox WHERE organization_id = NEW.organization_id;
    IF NEW.entity_id IS NOT NULL THEN
        SELECT COALESCE(max(entity_seq), 0) + 1 INTO NEW.entity_seq
        FROM event_outbox WHERE organization_id = NEW.organization_id AND entity_id = NEW.entity_id;
    ELSE
        NEW.entity_seq := NULL;
    END IF;
    RETURN NEW;
END;
$$;

DROP TABLE IF EXISTS event_outbox_counter;
