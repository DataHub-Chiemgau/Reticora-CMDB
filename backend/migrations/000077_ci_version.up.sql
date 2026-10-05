-- WP-061 (API-07): optimistic concurrency for CIs.
--
-- ci.version is the ETag of a CI. It rises only with writes of rank >= 92
-- (manual, import, workflow); observed discovery and agent updates leave it.
-- ci_change.ci_version records the version a change produced, so a PATCH
-- with an older If-Match is refused only when one of its fields changed
-- manually since (field-granular 409).

ALTER TABLE ci ADD COLUMN version BIGINT NOT NULL DEFAULT 1;
ALTER TABLE ci_change ADD COLUMN ci_version BIGINT;

CREATE INDEX idx_ci_change_ci_version ON ci_change (ci_id, ci_version) WHERE ci_version IS NOT NULL;
