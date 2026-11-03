-- Reverts WP-714 (000205) as a documented no-op: the removed overrides of
-- instance attributes are not restored. They were no valid state (MET-14,
-- CI-04): instance attributes are protected by the central write decision,
-- and the up migration left attributes._instance unchanged.
SELECT 1;
