-- Reverts 000360: the score snapshots, the weights and the aggregation
-- index.

DROP INDEX idx_compliance_result_rule_status;
DROP TABLE compliance_score_snapshot;
DROP TABLE compliance_settings;
