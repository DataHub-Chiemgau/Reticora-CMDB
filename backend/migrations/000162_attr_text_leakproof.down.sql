-- Migration attr_text_leakproof down: the attribute indexes and the filter
-- go back to attributes ->> '<key>'. Dropping the function drops every index
-- built over it (CASCADE); one attribute.index job per searchable or unique
-- definition of an organization rebuilds them over ->> on the next run of
-- the DDL job (a definition with a queued attribute.index job gets no second
-- one). Until then the attribute filter reads without an index and a unique
-- attribute is checked by the application only (UniqueConflicts). Run it
-- after the previous version of the server is deployed: the current one
-- cannot build an index without the function, and its DDL job would fail
-- these jobs. Queued rebuild jobs of the up migration are cancelled; the
-- previous version does not know their kind.
UPDATE job SET status = 'cancelled', finished_at = now(), error = 'migration 000162_attr_text_leakproof reverted'
 WHERE kind = 'attribute.index.rebuild' AND status = 'queued';

INSERT INTO job (organization_id, kind, params, max_attempts, timeout_seconds)
SELECT d.organization_id, 'attribute.index', jsonb_build_object('definition_id', d.id::text), 3, 3600
  FROM attribute_definition d
 WHERE d.organization_id IS NOT NULL
   AND (d.searchable OR d.is_unique)
   AND NOT EXISTS (
        SELECT 1 FROM job j
         WHERE j.organization_id = d.organization_id AND j.kind = 'attribute.index' AND j.status = 'queued'
           AND j.params ->> 'definition_id' = d.id::text);

DROP FUNCTION IF EXISTS reticora_attr_text(jsonb, text) CASCADE;
