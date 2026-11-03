-- WP-290 (TKT-01): ticket states and the ticket number per organization.
--
-- 1. Status: ticket_status_check gains "new", the default of a new ticket.
--    Existing rows keep their status; the transitions are checked by the
--    application (backend/internal/ticket/states.go).
-- 2. Number: ticket_number was a global SERIAL. It is now counted per
--    organization in ticket_number_counter, assigned by the BEFORE INSERT
--    trigger ticket_number_assign. The counter is read and advanced with
--    INSERT ... ON CONFLICT ... RETURNING, never with max()+1 over ticket,
--    which under client, site or team RLS would see the visible rows only.
--    Its policy checks app.org_id alone, so scoped principals share it.
--    Existing numbers stay; each counter starts at max(ticket_number) of its
--    organization. A ticket inserted with a number (import, restore) keeps
--    it and moves the counter up to it.
--    The sequence ticket_ticket_number_seq stays for the down migration.

-- ─── 1. Status ────────────────────────────────────────────────────────────────

ALTER TABLE ticket DROP CONSTRAINT ticket_status_check;
ALTER TABLE ticket ADD CONSTRAINT ticket_status_check CHECK (status IN (
    'new', 'open', 'in_progress', 'waiting', 'resolved', 'closed'
));
ALTER TABLE ticket ALTER COLUMN status SET DEFAULT 'new';

-- ─── 2. Number per organization ───────────────────────────────────────────────

CREATE TABLE ticket_number_counter (
    organization_id UUID PRIMARY KEY REFERENCES organization (id) ON DELETE CASCADE,
    last_number     INTEGER NOT NULL
);

INSERT INTO ticket_number_counter (organization_id, last_number)
SELECT organization_id, max(ticket_number) FROM ticket GROUP BY organization_id;

ALTER TABLE ticket_number_counter ENABLE ROW LEVEL SECURITY;
ALTER TABLE ticket_number_counter FORCE ROW LEVEL SECURITY;
CREATE POLICY ticket_number_counter_isolation ON ticket_number_counter FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON ticket_number_counter
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

CREATE FUNCTION assign_ticket_number() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.ticket_number IS NULL THEN
        INSERT INTO ticket_number_counter (organization_id, last_number)
        VALUES (NEW.organization_id, 1)
        ON CONFLICT (organization_id) DO UPDATE
            SET last_number = ticket_number_counter.last_number + 1
        RETURNING last_number INTO NEW.ticket_number;
    ELSE
        INSERT INTO ticket_number_counter (organization_id, last_number)
        VALUES (NEW.organization_id, NEW.ticket_number)
        ON CONFLICT (organization_id) DO UPDATE
            SET last_number = GREATEST(ticket_number_counter.last_number, EXCLUDED.last_number);
    END IF;
    RETURN NEW;
END
$$;

ALTER TABLE ticket ALTER COLUMN ticket_number DROP DEFAULT;

CREATE TRIGGER ticket_number_assign BEFORE INSERT ON ticket
    FOR EACH ROW EXECUTE FUNCTION assign_ticket_number();

DROP INDEX idx_ticket_number;
ALTER TABLE ticket ADD CONSTRAINT ticket_organization_number_key UNIQUE (organization_id, ticket_number);
