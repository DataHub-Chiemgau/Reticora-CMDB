-- Reverts 000319: the table source_rank_policy with its trigger function.
-- source_priority_policy was not changed.

DROP TABLE source_rank_policy;
DROP FUNCTION source_rank_policy_type();
