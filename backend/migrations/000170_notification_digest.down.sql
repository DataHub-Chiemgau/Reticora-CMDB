-- Migration notification_digest down: no digests; the policies, indexes and
-- the TRUNCATE guard go with the table. Items of open windows are lost, the
-- deliveries of ended windows stay.
DROP TABLE IF EXISTS notification_digest_item;
ALTER TABLE notification_preference DROP COLUMN IF EXISTS digest;
