-- Revert migration 000034: webhook dead-letter queue

DROP TABLE IF EXISTS webhook_dead_letter;

ALTER TABLE webhook_delivery DROP CONSTRAINT IF EXISTS webhook_delivery_status_check;
ALTER TABLE webhook_delivery
    ADD CONSTRAINT webhook_delivery_status_check
    CHECK (status IN ('pending', 'success', 'failed', 'retrying'));
