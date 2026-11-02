-- Migration 000193: the event outbox numbers its events from a counter per
-- organization (WP-834, SR-08, EVT-01, NFR-03).
--
-- Until now the insert trigger event_outbox_number() (000120) set
-- NEW.sequence to max(sequence) + 1 over the organization's events, once
-- per row, under the organization's transaction advisory lock (class
-- 1165389428). The ingest writes its events in that locked section right
-- before the commit (database.BeforeCommit, WP-334); with stale statistics
-- the aggregate read every event of the organization and held the section
-- for about 200 ms per batch. The trigger now raises the organization's
-- row in event_outbox_counter under the same lock (one primary key upsert,
-- independent of the number of events): sequence stays gapless in commit
-- order. entity_seq stays per entity (max over the entity's index range).
--
-- The counter also lets Publisher.Cleanup drop the newest event of an
-- organization: the numbering no longer continues from it. Each entity's
-- newest event stays as the anchor of entity_seq (WP-548).
--
-- event_outbox_counter is a tenant table with the policies of event_outbox
-- (own organization for every command, the system context reads) and the
-- TRUNCATE guard of the tenant tables (000144, WP-352). The rows are filled
-- from max(sequence) per organization; the outbox is locked against inserts
-- until the migration commits, so no event is numbered from a stale count.
--
-- The number 000193 follows 000192, which a parallel work package of phase
-- 2 holds; it has to be merged first (versions without gaps).

CREATE TABLE event_outbox_counter (
    organization_id UUID PRIMARY KEY REFERENCES organization (id) ON DELETE CASCADE,
    last_sequence   BIGINT NOT NULL,
    CONSTRAINT event_outbox_counter_positive CHECK (last_sequence >= 0)
);

ALTER TABLE event_outbox_counter ENABLE ROW LEVEL SECURITY;
ALTER TABLE event_outbox_counter FORCE ROW LEVEL SECURITY;
CREATE POLICY event_outbox_counter_isolation ON event_outbox_counter FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
CREATE POLICY event_outbox_counter_system_select ON event_outbox_counter FOR SELECT
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid
           OR current_setting('app.system', true) = 'on');

-- Every tenant table carries the TRUNCATE guard of the tenant tables.
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON event_outbox_counter
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- No event is written while the counters are filled.
LOCK TABLE event_outbox IN SHARE MODE;

INSERT INTO event_outbox_counter (organization_id, last_sequence)
SELECT organization_id, max(sequence) FROM event_outbox GROUP BY organization_id;

CREATE OR REPLACE FUNCTION event_outbox_number() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(1165389428, audit_org_lock_key(NEW.organization_id));
    INSERT INTO event_outbox_counter AS c (organization_id, last_sequence)
    VALUES (NEW.organization_id, 1)
    ON CONFLICT (organization_id) DO UPDATE SET last_sequence = c.last_sequence + 1
    RETURNING c.last_sequence INTO NEW.sequence;
    IF NEW.entity_id IS NOT NULL THEN
        SELECT COALESCE(max(entity_seq), 0) + 1 INTO NEW.entity_seq
        FROM event_outbox WHERE organization_id = NEW.organization_id AND entity_id = NEW.entity_id;
    ELSE
        NEW.entity_seq := NULL;
    END IF;
    RETURN NEW;
END;
$$;
