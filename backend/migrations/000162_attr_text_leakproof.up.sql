-- WP-435 (ABN-02, NFR-02; F-114 attribute part, SR-04; decision E-73,
-- transitional rule option A): a leakproof accessor for the attribute
-- indexes and the attribute filter.
--
-- The attribute filter of the CI list compared attributes ->> '<key>', the
-- expression indexes idx_attr_<id> of the DDL job are built over the same
-- expression. jsonb_object_field_text (->>) is not leakproof: below the RLS
-- barrier of reticora_app the comparison never becomes an index condition,
-- so the index served the owner only and the application role read every CI
-- of the organization.
--
-- 1. reticora_attr_text(attributes, attr_key) is attributes ->> attr_key,
--    declared LEAKPROOF so that the planner may evaluate it before the
--    policies of ci, i.e. in an index condition. The promise holds for every
--    input: STRICT returns NULL for a NULL argument without a call; ->>
--    returns NULL for a missing key and for an array, a scalar or JSON null
--    instead of an object, and the text of any value otherwise; neither the
--    key nor the value appears in a message. Only resource limits (out of
--    memory, cancel) can end a call, like with the leakproof functions of
--    PostgreSQL itself.
--    The function must not be inlined: the planner would replace an inlined
--    body by attributes ->> attr_key, which is not leakproof, and the index
--    would be unusable again. PL/pgSQL is never inlined; the SET clause pins
--    search_path (->> resolves in pg_catalog only, whatever the caller's
--    path is). IMMUTABLE and PARALLEL SAFE like ->> itself.
--    COST 1000 is a hint for the planner: it has no statistics for the
--    value (it ignores those of a partial index) and estimates a comparison
--    at 0.5 % of the rows. With the default cost (100) a page of the CI
--    list read through idx_ci_list_sort, calling the function for every CI
--    of the organization, looked about as cheap as the attribute index for
--    an equality and cheaper for an in list (10,000 CIs, created_at in the
--    order of insertion). Broad predicates (ranges) then read the attribute
--    index as well and sort, which takes milliseconds at that size.
--    Only a superuser can declare a function LEAKPROOF. Without one the
--    migration stops: operation without a superuser (managed PostgreSQL)
--    needs option B of E-73 (side table ci_attribute_value).
-- 2. The indexes of the attribute definitions are built by the DDL job
--    (CREATE INDEX CONCURRENTLY outside a transaction), so this migration
--    queues one attribute.index.rebuild job per searchable or unique
--    definition of an organization: the job builds the index over
--    reticora_attr_text next to the existing one and drops the old one only
--    after the new one is valid (citype.DDLJob.Ensure). The kind is new, so
--    a server of the previous version that still runs during a rolling
--    update leaves the jobs alone (it would end an attribute.index job
--    without a rebuild). A definition with a queued rebuild job gets no
--    second one. System definitions have no index (the job leaves them to
--    their migration, and none builds one).

DO $$
BEGIN
    IF NOT (SELECT rolsuper FROM pg_roles WHERE rolname = current_user) THEN
        RAISE EXCEPTION 'migration 000162_attr_text_leakproof needs a superuser to declare reticora_attr_text LEAKPROOF (decision E-73, option A); operation without a superuser needs option B of decision E-73'
            USING HINT = 'Run the migrations with the bootstrap superuser of the installer; see docs/plan/g1-korrektur/entscheidungen.md#e-73.',
                  ERRCODE = 'insufficient_privilege';
    END IF;
END
$$;

-- ─── 1. Function ───────────────────────────────────────────────────────────────

CREATE FUNCTION reticora_attr_text(attributes jsonb, attr_key text) RETURNS text
LANGUAGE plpgsql IMMUTABLE STRICT LEAKPROOF PARALLEL SAFE COST 1000
SET search_path = pg_catalog
AS $$
BEGIN
    RETURN attributes ->> attr_key;
END
$$;

COMMENT ON FUNCTION reticora_attr_text(jsonb, text) IS
    'attributes ->> attr_key, leakproof and not inlinable: the expression of the attribute indexes idx_attr_<id> and of the attribute filter (WP-435, E-73). Never raises for any input.';

-- ─── 2. Rebuild of the existing attribute indexes ──────────────────────────────

INSERT INTO job (organization_id, kind, params, max_attempts, timeout_seconds)
SELECT d.organization_id, 'attribute.index.rebuild', jsonb_build_object('definition_id', d.id::text), 3, 3600
  FROM attribute_definition d
 WHERE d.organization_id IS NOT NULL
   AND (d.searchable OR d.is_unique)
   AND NOT EXISTS (
        SELECT 1 FROM job j
         WHERE j.organization_id = d.organization_id AND j.kind = 'attribute.index.rebuild' AND j.status = 'queued'
           AND j.params ->> 'definition_id' = d.id::text);
