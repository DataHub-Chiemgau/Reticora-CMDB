-- Reverts WP-636: unmatched discovery results take the client of their
-- collector only when they are written. The backfilled client_id of the
-- existing results stays (it is what derive_discovery_result_scope of
-- 000061 writes for them).

DROP TRIGGER IF EXISTS trg_collector_result_scope ON collector;
DROP FUNCTION IF EXISTS propagate_collector_scope();
