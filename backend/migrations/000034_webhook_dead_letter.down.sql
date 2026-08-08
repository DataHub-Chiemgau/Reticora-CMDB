-- Revert migration 000034: webhook dead-letter queue

DROP TABLE IF EXISTS webhook_dead_letter;

-- 'dead' is unknown to the pre-000034 status set; those rows return to
-- 'failed', which was their state before the dispatcher had a DLQ.
UPDATE webhook_delivery SET status = 'failed' WHERE status = 'dead';

ALTER TABLE webhook_delivery DROP CONSTRAINT IF EXISTS webhook_delivery_status_check;
ALTER TABLE webhook_delivery
    ADD CONSTRAINT webhook_delivery_status_check
    CHECK (status IN ('pending', 'success', 'failed', 'retrying'));
