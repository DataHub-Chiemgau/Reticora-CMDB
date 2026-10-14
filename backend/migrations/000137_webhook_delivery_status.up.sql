-- WHK-01: the delivery statuses of the catalog. success is delivered; a
-- failed attempt with a retry scheduled is failed (was retrying); a delivery
-- a worker holds is delivering; dead stays. A delivery claimed under the old
-- names was retrying and becomes failed: its lease time is its next attempt.
ALTER TABLE webhook_delivery DROP CONSTRAINT webhook_delivery_status_check;
UPDATE webhook_delivery SET status = CASE status
    WHEN 'success' THEN 'delivered'
    WHEN 'retrying' THEN 'failed'
    WHEN 'failed' THEN 'dead'
    ELSE status END;
ALTER TABLE webhook_delivery ADD CONSTRAINT webhook_delivery_status_check
    CHECK (status IN ('pending', 'delivering', 'delivered', 'failed', 'dead'));
