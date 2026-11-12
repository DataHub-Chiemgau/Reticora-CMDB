-- Reverts 000332: removes applies_when. The converted expressions stay:
-- they are valid MET-45 conditions, and the G1 evaluator that could read
-- the old form no longer exists after the down migration either (WP-1125).
-- Deactivated rules stay inactive with their note in description.

ALTER TABLE compliance_rule DROP COLUMN applies_when;
