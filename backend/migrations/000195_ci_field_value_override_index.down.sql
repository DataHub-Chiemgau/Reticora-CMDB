-- Migration 000195 down: without the override index of ci_field_value.

DROP INDEX idx_ci_field_value_override;
