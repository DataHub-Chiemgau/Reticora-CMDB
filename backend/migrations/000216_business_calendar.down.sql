-- Down of 000216: the ticket_sla and sla indexes and columns, then the
-- calendar table with its triggers and policies.

DROP INDEX idx_ticket_sla_resolution_open;
DROP INDEX idx_ticket_sla_response_open;
DROP INDEX idx_ticket_sla_calendar;
ALTER TABLE ticket_sla DROP CONSTRAINT ticket_sla_calendar_id_fkey,
    DROP COLUMN resolution_breach_notified_at,
    DROP COLUMN response_breach_notified_at,
    DROP COLUMN sla_explicit,
    DROP COLUMN calendar_id;

DROP INDEX idx_sla_calendar;
ALTER TABLE sla DROP CONSTRAINT sla_calendar_id_fkey,
    DROP COLUMN calendar_id;

DROP TABLE business_calendar;
