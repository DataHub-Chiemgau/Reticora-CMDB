-- Migration notice_sent down: no record of sent notices; the policy and the
-- TRUNCATE guard go with the table.
DROP TABLE IF EXISTS notice_sent;
