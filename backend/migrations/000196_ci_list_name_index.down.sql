-- Migration 000196 down: without the index of the CI list sorted by name.

DROP INDEX idx_ci_list_name;
