-- Reverts 000358: the follow-up actions of the rules are dropped (every
-- alarm notifies and reaches the webhooks again).

ALTER TABLE alert_rule DROP COLUMN actions;
