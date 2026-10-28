-- WP-574 (AST-05, DB-05; F-243): no CI may contain itself, directly or
-- through its parents.
--
-- Migration 000089 turned stored contains rows into ci.parent_ci_id before
-- it created the cycle trigger trg_ci_parent_cycle. Cyclic legacy rows (A
-- contains B, B contains A) therefore became a parent_ci_id cycle that the
-- trigger never saw. This migration finds every such cycle and breaks it at
-- its youngest CI (created_at, then id): the CI's parent_ci_id moves to
-- migration_quarantine (E-26) and is cleared. An operator reviews the row
-- and sets the right parent through the API. CIs on no cycle, also those
-- below a cycle, keep their parent.
--
-- Each CI has at most one parent, so every CI lies on at most one cycle.
-- The walk follows the parents from every CI with a parent and stops at the
-- first CI it has already visited; a walk that comes back to its start
-- found a cycle, and its path holds exactly the CIs of that cycle.

WITH RECURSIVE walk (start_id, cur, path) AS (
    SELECT id, parent_ci_id, ARRAY[id]
    FROM ci
    WHERE parent_ci_id IS NOT NULL
    UNION ALL
    SELECT w.start_id, c.parent_ci_id, w.path || c.id
    FROM walk w
    JOIN ci c ON c.id = w.cur
    WHERE c.parent_ci_id IS NOT NULL AND c.id <> ALL (w.path)
), cycle AS (
    -- One row per CI on a cycle; path starts at that CI and follows its
    -- parents around the cycle.
    SELECT start_id, path FROM walk WHERE cur = start_id
), victim AS (
    SELECT c.id, c.organization_id, c.parent_ci_id, cy.path
    FROM cycle cy
    JOIN ci c ON c.id = cy.start_id
    WHERE c.id = (SELECT m.id FROM ci m WHERE m.id = ANY (cy.path)
                  ORDER BY m.created_at DESC, m.id DESC LIMIT 1)
), repaired AS (
    UPDATE ci SET parent_ci_id = NULL
    FROM victim v
    WHERE ci.id = v.id
    RETURNING v.id, v.organization_id, v.parent_ci_id, v.path
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT organization_id, 'ci',
       jsonb_build_object('id', id, 'parent_ci_id', parent_ci_id, 'cycle', to_jsonb(path)),
       'parent_ci_id closed a containment cycle (AST-05): it is cleared on the youngest CI '
       || 'of the cycle; cycle lists the CIs from this one along their parents', '000179'
FROM repaired;
