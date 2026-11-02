-- Migration platform_secret down: the derivation root is dropped with the
-- table (policies, grants and the TRUNCATE guard go with it). The server of
-- the previous version derives its secrets from the master key again; after
-- a new up the next start draws a new root, so the derived secrets change
-- (restart nats-edge, docs/ops/workers.md).
DROP TABLE IF EXISTS platform_secret;
