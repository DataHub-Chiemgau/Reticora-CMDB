-- Migration 000120: transactional outbox of the domain events (EVT-01,
-- EVT-02, WP-136).
--
-- A mutation writes its event in its own transaction: a rollback leaves no
-- event, a commit leaves exactly one. The publisher delivers the rows in
-- sequence order to the NATS stream RETICORA_EVENTS and to the webhook
-- subscriptions and marks them published. The insert trigger numbers the
-- events per organization (sequence) and per entity (entity_seq) under the
-- organization's transaction advisory lock, so the numbers follow commit
-- order without gaps.

CREATE TABLE event_outbox (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    sequence        BIGINT NOT NULL,
    type            TEXT NOT NULL,
    entity_id       TEXT,
    entity_seq      BIGINT,
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    data            JSONB NOT NULL DEFAULT '{}',
    published_at    TIMESTAMPTZ,
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT,
    CONSTRAINT event_outbox_type_check CHECK (type ~ '^[a-z_]+\.[a-z_]+$'),
    CONSTRAINT event_outbox_entity_check CHECK ((entity_id IS NULL) = (entity_seq IS NULL))
);

CREATE UNIQUE INDEX uq_event_outbox_org_sequence ON event_outbox (organization_id, sequence);
CREATE UNIQUE INDEX uq_event_outbox_entity_seq ON event_outbox (organization_id, entity_id, entity_seq)
    WHERE entity_id IS NOT NULL;
CREATE INDEX idx_event_outbox_pending ON event_outbox (organization_id, sequence) WHERE published_at IS NULL;
CREATE INDEX idx_event_outbox_published ON event_outbox (published_at) WHERE published_at IS NOT NULL;

CREATE FUNCTION event_outbox_number() RETURNS trigger
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

CREATE TRIGGER trg_event_outbox_number BEFORE INSERT ON event_outbox
    FOR EACH ROW EXECUTE FUNCTION event_outbox_number();

ALTER TABLE event_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE event_outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY event_outbox_isolation ON event_outbox FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
CREATE POLICY event_outbox_system_select ON event_outbox FOR SELECT
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid
           OR current_setting('app.system', true) = 'on');
