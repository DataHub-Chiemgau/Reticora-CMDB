-- Down of 000323: the backfill jobs not started yet go; a started or
-- finished one stays (its writes are data).
DELETE FROM job WHERE kind = 'ipam.mgmt_backfill' AND status = 'queued';
