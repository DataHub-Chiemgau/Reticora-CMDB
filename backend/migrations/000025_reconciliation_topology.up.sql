-- Migration 000025: reconciliation & topology support
--
-- Discovery-derived topology edges use a `powered_by` relationship type (device
-- powered_by PDU/UPS) in addition to the existing `connected_to` L2 links. The
-- original ci_relationship CHECK constraint (migration 000006) did not include
-- `powered_by`, so extend it here. No new tables are required: the review queue
-- reuses `review_item`, jobs reuse `discovery_job`, and suppression reuses
-- `relationship_suppression` (all from earlier migrations).

ALTER TABLE ci_relationship DROP CONSTRAINT IF EXISTS ci_relationship_rel_type_check;
ALTER TABLE ci_relationship ADD CONSTRAINT ci_relationship_rel_type_check
    CHECK (rel_type IN (
        'connected_to', 'hosted_on', 'depends_on', 'member_of',
        'powers', 'powered_by', 'stores', 'monitors', 'backs_up'
    ));

-- Index to speed up discovery-sourced topology edge upserts and queries.
CREATE INDEX IF NOT EXISTS idx_ci_rel_source_kind ON ci_relationship(organization_id, source);
