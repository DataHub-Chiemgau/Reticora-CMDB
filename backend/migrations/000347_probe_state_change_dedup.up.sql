-- Migration 000347 (provisional number): the state changes of the worker
-- probe-state (PRB-05, PRB-07, PRB-11, WP-961, E-175, E-180).
--
-- stale marks a change with an older epoch than the assignment of its
-- probe (or of another collector than the assigned one): it is kept in the
-- history but decides no state and notifies nobody (split-brain protection).
-- The idempotency key (probe_id, collector_id, epoch, seq) exists since
-- 000311 (probe_state_change_idempotency_key); the worker writes with ON
-- CONFLICT DO NOTHING against it. RLS, policies and truncate guard of
-- probe_state_change stay as they are.

ALTER TABLE probe_state_change ADD COLUMN stale boolean NOT NULL DEFAULT false;
