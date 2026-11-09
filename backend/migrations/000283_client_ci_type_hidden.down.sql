-- Migration 000283 down: no CI types hidden per client (WP-858).

DROP TABLE IF EXISTS client_ci_type_hidden;
DROP FUNCTION IF EXISTS client_ci_type_hidden_type();
