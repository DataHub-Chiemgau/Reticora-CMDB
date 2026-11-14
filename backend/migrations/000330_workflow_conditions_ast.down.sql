-- Reverts 000330 as a documented no-op (WP-1087): the migration changed
-- only data. The G1 form of the conditions cannot be restored without loss
-- (exists and not_empty both became not_empty, a dropped value is gone),
-- and the G1 evaluator no longer exists. The converted conditions are valid
-- MET-45 nodes and stay; deactivated definitions stay inactive.
SELECT 1;
