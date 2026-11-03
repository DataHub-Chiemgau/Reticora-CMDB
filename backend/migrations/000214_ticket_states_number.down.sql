-- Down of 000214: the global ticket number from the sequence and the status
-- set of 000013 ("new" becomes "open").

ALTER TABLE ticket DROP CONSTRAINT ticket_organization_number_key;
CREATE INDEX idx_ticket_number ON ticket (organization_id, ticket_number);

DROP TRIGGER ticket_number_assign ON ticket;
DROP FUNCTION assign_ticket_number();
DROP TABLE ticket_number_counter;

SELECT setval('ticket_ticket_number_seq', max(ticket_number)) FROM ticket HAVING max(ticket_number) IS NOT NULL;
ALTER TABLE ticket ALTER COLUMN ticket_number SET DEFAULT nextval('ticket_ticket_number_seq');

UPDATE ticket SET status = 'open' WHERE status = 'new';
ALTER TABLE ticket DROP CONSTRAINT ticket_status_check;
ALTER TABLE ticket ADD CONSTRAINT ticket_status_check CHECK (status IN (
    'open', 'in_progress', 'waiting', 'resolved', 'closed'
));
ALTER TABLE ticket ALTER COLUMN status SET DEFAULT 'open';
