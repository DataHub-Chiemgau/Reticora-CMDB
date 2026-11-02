-- Migration attr_index_per_key down: back to one index per searchable
-- definition. The DDL job of the previous version does not know the indexes
-- of keys (idx_attr_key_* and idx_attr_keyh_* on ci), so they are dropped
-- here (DROP INDEX holds the lock on ci briefly); one attribute.index job
-- per definition an index of its key served builds the definition's own
-- index on the next run of the DDL job (a definition with a queued
-- attribute.index job gets no second one). Until then the attribute filter
-- of these definitions reads without an index. Run it after the previous
-- version of the server is deployed: the current one would build the
-- indexes of the keys again. Queued attribute.index.key jobs of the up
-- migration are cancelled; the previous version does not know their kind.
UPDATE job SET status = 'cancelled', finished_at = now(), error = 'migration 000164_attr_index_per_key reverted'
 WHERE kind = 'attribute.index.key' AND status = 'queued';

INSERT INTO job (organization_id, kind, params, max_attempts, timeout_seconds)
SELECT d.organization_id, 'attribute.index', jsonb_build_object('definition_id', d.id::text), 3, 3600
  FROM attribute_definition d
 WHERE d.organization_id IS NOT NULL
   AND d.searchable AND NOT d.is_unique AND d.ci_type_id IS NULL
   AND NOT EXISTS (
        SELECT 1 FROM job j
         WHERE j.organization_id = d.organization_id AND j.kind = 'attribute.index' AND j.status = 'queued'
           AND j.params ->> 'definition_id' = d.id::text);

DO $$
DECLARE
    idx text;
BEGIN
    FOR idx IN
        SELECT c.relname FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
         WHERE i.indrelid = 'public.ci'::regclass AND starts_with(c.relname::text, 'idx_attr_key')
         ORDER BY 1
    LOOP
        EXECUTE format('DROP INDEX public.%I', idx);
    END LOOP;
END
$$;
