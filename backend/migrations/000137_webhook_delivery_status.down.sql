ALTER TABLE webhook_delivery DROP CONSTRAINT webhook_delivery_status_check;
UPDATE webhook_delivery SET status = CASE status
    WHEN 'delivered' THEN 'success'
    WHEN 'failed' THEN 'retrying'
    WHEN 'delivering' THEN 'retrying'
    ELSE status END;
ALTER TABLE webhook_delivery ADD CONSTRAINT webhook_delivery_status_check
    CHECK (status IN ('pending', 'success', 'failed', 'retrying', 'dead'));
