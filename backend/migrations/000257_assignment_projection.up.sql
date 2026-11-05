-- Migration 000257: assignments as a projection of the ledger stock_movement
-- (WP-747, AST-02, AST-07).
--
-- Since WP-275 and WP-746 only the assignment service writes assignments,
-- into the ledger; the table assignment kept transition rows (the id of
-- the assignment movement). Older rows are missing in the ledger or stand
-- there only as best-effort movements: the history 000242 copied with the
-- notes 'assignment:<id>' and the movements of asset_movement (WP-744).
-- From now on GET /api/v1/assignments, the DSAR export, TLC-04 and the
-- filter has_owner read the ledger or the holder cache; the table
-- assignment stays unchanged until WP-843.
--
-- 1. Index idx_stock_movement_holder for the projection (return of an
--    assignment, the holder's movements of an asset).
-- 2. Takeover of the history, keeping the ids: every row of assignment
--    without a movement of the same id becomes an assignment movement (id =
--    assignment.id, occurred_at = assigned_at, assignee_user_id =
--    assigned_to, actor_id = assigned_by, notes, due_at = end of the day
--    due_date in organization.timezone, created_at of the row). A closed
--    row gets a return movement with the holder: returned at returned_at
--    (reason = return_condition), transferred at the occurred_at of the
--    following assignment of the asset (else updated_at), cancelled at
--    updated_at (reason 'cancelled'). A return lies after its assignment
--    (at least 1 µs). A row with only ci_id takes the asset of its CI
--    (ci.asset_id); a row without an asset of its organization
--    (assignment_without_asset) or with a user of another organization
--    (assignment_foreign_user) goes to migration_quarantine (pattern
--    000056, E-26, without the signatures, which WP-843 secures) and stays
--    in assignment.
-- 3. Best-effort movements: an assignment or return movement of the same
--    asset without a holder or with the same user, less than 60 s from
--    assigned_at or the return time, or the copy of 000242 (notes
--    'assignment:<id>'), is a duplicate. It is replaced by the complete
--    movement while stock_movement_append_only is disabled (pattern
--    000074); the document links (entity_type 'stock_movement') move to
--    the new movement. A return with the holder (written by the assignment
--    service, which closed the row) is kept and not booked twice.
-- 4. An asset with several open assignments keeps the newest (the one of
--    its holder cache first, then occurred_at, id); the others get a return
--    movement at the migration time (reason 'migration: superseded'), with
--    the same holder as the kept one just before it.
-- 5. View assignment_projection WITH (security_invoker = true) over
--    stock_movement, so its row level security applies to the caller: one
--    row per assignment movement of an asset. Its return is the first
--    return movement of the same asset and holder after it (returned_at,
--    return_condition). status: transferred (the return has an assignment
--    movement of the asset at the same time), returned, overdue (open and
--    due_at in the past), else active; assignment_type: transfer for the
--    assignment of a holder change (a return of the asset at the same
--    time), else assignment. updated_at is the created_at of the return,
--    else of the assignment. Pure location moves (transfer) stay out.
--
-- All numbers are reported with RAISE NOTICE. Down removes view, index and
-- the quarantine entries; the movements stay (append-only), a new up finds
-- them by their id.

-- ─── 1. Index ──────────────────────────────────────────────────────────────────

CREATE INDEX IF NOT EXISTS idx_stock_movement_holder ON stock_movement (organization_id, asset_id, movement_type, occurred_at);

-- ─── 2. to 4. History ──────────────────────────────────────────────────────────

DO $$
DECLARE
    n_booked bigint;
    n_imported bigint;
    n_returns bigint;
    n_kept bigint;
    n_replaced_assignments bigint;
    n_replaced_returns bigint;
    n_relinked bigint;
    n_superseded bigint;
    n_without_asset bigint;
    n_foreign_user bigint;
BEGIN
    -- Every row of assignment with its asset in the organization and
    -- whether the ledger holds a movement of its id.
    CREATE TEMPORARY TABLE assignment_projection_row ON COMMIT DROP AS
    SELECT s.id, s.organization_id, s.assigned_to, s.assigned_by, s.status, s.assigned_at, s.returned_at,
           s.return_condition, s.notes, s.created_at, s.updated_at,
           to_jsonb(s) - 'checkout_signature' - 'return_signature' AS row_data,
           (SELECT a.id FROM asset a
             WHERE a.organization_id = s.organization_id
               AND a.id = COALESCE(s.asset_id,
                   (SELECT c.asset_id FROM ci c WHERE c.id = s.ci_id AND c.organization_id = s.organization_id))) AS asset_id,
           EXISTS (SELECT 1 FROM app_user u WHERE u.id = s.assigned_to AND u.organization_id = s.organization_id) AS own_user,
           EXISTS (SELECT 1 FROM stock_movement m WHERE m.id = s.id) AS booked,
           CASE WHEN s.due_date IS NOT NULL
                THEN ((s.due_date + 1)::timestamp AT TIME ZONE o.timezone) - interval '1 microsecond' END AS due_at,
           NULL::timestamptz AS return_at,
           NULL::text AS return_reason
      FROM assignment s
      JOIN organization o ON o.id = s.organization_id;

    SELECT count(*) INTO n_booked FROM assignment_projection_row WHERE booked;

    INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
    SELECT r.organization_id, 'assignment', r.row_data,
           CASE WHEN r.asset_id IS NULL THEN 'assignment_without_asset' ELSE 'assignment_foreign_user' END, '000257'
      FROM assignment_projection_row r
     WHERE NOT r.booked AND (r.asset_id IS NULL OR NOT r.own_user);
    SELECT count(*) FILTER (WHERE asset_id IS NULL), count(*) FILTER (WHERE asset_id IS NOT NULL AND NOT own_user)
      INTO n_without_asset, n_foreign_user
      FROM assignment_projection_row WHERE NOT booked;

    DELETE FROM assignment_projection_row WHERE booked OR asset_id IS NULL OR NOT own_user;

    -- The return of a closed row; the following assignment of a transfer
    -- is a row of the asset or a team assignment of the service.
    UPDATE assignment_projection_row r
       SET return_at = CASE r.status
               WHEN 'returned' THEN COALESCE(r.returned_at, r.updated_at)
               WHEN 'transferred' THEN COALESCE(LEAST(
                   (SELECT min(n.assigned_at) FROM assignment n
                     WHERE n.organization_id = r.organization_id AND n.assigned_at > r.assigned_at
                       AND (n.asset_id = r.asset_id OR (n.asset_id IS NULL AND n.ci_id IN
                           (SELECT c.id FROM ci c WHERE c.organization_id = r.organization_id AND c.asset_id = r.asset_id)))),
                   (SELECT min(m.occurred_at) FROM stock_movement m
                     WHERE m.organization_id = r.organization_id AND m.asset_id = r.asset_id AND m.movement_type = 'assignment'
                       AND m.assignee_team_id IS NOT NULL AND m.occurred_at > r.assigned_at)), r.updated_at)
               WHEN 'cancelled' THEN r.updated_at
           END,
           return_reason = CASE r.status WHEN 'returned' THEN r.return_condition WHEN 'cancelled' THEN 'cancelled' END;
    UPDATE assignment_projection_row SET return_at = GREATEST(return_at, assigned_at + interval '1 microsecond')
     WHERE return_at IS NOT NULL;

    -- 3. Duplicates: per row and per movement at most one, the copy of
    -- 000242 first, then the nearest. Movements of another row of
    -- assignment (the service's) are never duplicates.
    CREATE TEMPORARY TABLE assignment_projection_duplicate ON COMMIT DROP AS
    SELECT DISTINCT ON (c.movement_id) c.row_id, c.movement_id, c.movement_type
      FROM (
        SELECT DISTINCT ON (r.id) r.id AS row_id, m.id AS movement_id, m.movement_type,
               COALESCE(m.notes = 'assignment:' || r.id, false) AS copy, abs(extract(epoch FROM m.occurred_at - r.assigned_at)) AS gap
          FROM assignment_projection_row r
          JOIN stock_movement m ON m.organization_id = r.organization_id AND m.asset_id = r.asset_id
               AND m.movement_type = 'assignment'
         WHERE m.assignee_team_id IS NULL AND (m.assignee_user_id IS NULL OR m.assignee_user_id = r.assigned_to)
           AND (m.notes = 'assignment:' || r.id
                OR (abs(extract(epoch FROM m.occurred_at - r.assigned_at)) < 60
                    AND COALESCE(m.notes, '') !~ '^assignment:[0-9a-f-]{36}$'))
           AND NOT EXISTS (SELECT 1 FROM assignment x WHERE x.id = m.id)
         ORDER BY r.id, copy DESC, gap, m.id
      ) c
     ORDER BY c.movement_id, c.copy DESC, c.gap, c.row_id;

    -- A return with the holder that is no copy of 000242 stays.
    UPDATE assignment_projection_row r SET return_at = NULL
     WHERE r.return_at IS NOT NULL AND EXISTS (
        SELECT 1 FROM stock_movement m
         WHERE m.organization_id = r.organization_id AND m.asset_id = r.asset_id AND m.movement_type = 'return'
           AND m.assignee_user_id = r.assigned_to
           AND abs(extract(epoch FROM m.occurred_at - r.return_at)) < 60
           AND COALESCE(m.notes, '') !~ '^assignment:[0-9a-f-]{36}$');
    GET DIAGNOSTICS n_kept = ROW_COUNT;

    INSERT INTO assignment_projection_duplicate (row_id, movement_id, movement_type)
    SELECT DISTINCT ON (c.movement_id) c.row_id, c.movement_id, 'return'
      FROM (
        SELECT DISTINCT ON (r.id) r.id AS row_id, m.id AS movement_id,
               COALESCE(m.notes = 'assignment:' || r.id, false) AS copy, abs(extract(epoch FROM m.occurred_at - r.return_at)) AS gap
          FROM assignment_projection_row r
          JOIN stock_movement m ON m.organization_id = r.organization_id AND m.asset_id = r.asset_id
               AND m.movement_type = 'return'
         WHERE r.return_at IS NOT NULL
           AND m.assignee_team_id IS NULL AND m.assignee_user_id IS NULL
           AND (m.notes = 'assignment:' || r.id
                OR (abs(extract(epoch FROM m.occurred_at - r.return_at)) < 60
                    AND COALESCE(m.notes, '') !~ '^assignment:[0-9a-f-]{36}$'))
           AND NOT EXISTS (SELECT 1 FROM assignment x WHERE x.id = m.id)
         ORDER BY r.id, copy DESC, gap, m.id
      ) c
     WHERE NOT EXISTS (SELECT 1 FROM assignment_projection_duplicate d WHERE d.movement_id = c.movement_id)
     ORDER BY c.movement_id, c.copy DESC, c.gap, c.row_id;

    -- The new return movements get their id here, for the document links.
    CREATE TEMPORARY TABLE assignment_projection_return ON COMMIT DROP AS
    SELECT gen_random_uuid() AS id, r.id AS row_id FROM assignment_projection_row r WHERE r.return_at IS NOT NULL;

    INSERT INTO stock_movement (
        id, organization_id, item_kind, asset_id, movement_type, actor_id, assignee_user_id,
        occurred_at, due_at, notes, created_at)
    SELECT r.id, r.organization_id, 'asset', r.asset_id, 'assignment', r.assigned_by, r.assigned_to,
           r.assigned_at, r.due_at, r.notes, r.created_at
      FROM assignment_projection_row r;
    GET DIAGNOSTICS n_imported = ROW_COUNT;

    INSERT INTO stock_movement (
        id, organization_id, item_kind, asset_id, movement_type, assignee_user_id,
        occurred_at, reason, created_at)
    SELECT n.id, r.organization_id, 'asset', r.asset_id, 'return', r.assigned_to,
           r.return_at, r.return_reason, r.return_at
      FROM assignment_projection_return n
      JOIN assignment_projection_row r ON r.id = n.row_id;
    GET DIAGNOSTICS n_returns = ROW_COUNT;

    -- The document links of a replaced movement move to its successor.
    UPDATE document_link l
       SET entity_id = CASE d.movement_type WHEN 'assignment' THEN d.row_id ELSE n.id END
      FROM assignment_projection_duplicate d
      LEFT JOIN assignment_projection_return n ON n.row_id = d.row_id
     WHERE l.entity_type = 'stock_movement' AND l.entity_id = d.movement_id
       AND (d.movement_type = 'assignment' OR n.id IS NOT NULL);
    GET DIAGNOSTICS n_relinked = ROW_COUNT;

    SELECT count(*) FILTER (WHERE movement_type = 'assignment'), count(*) FILTER (WHERE movement_type = 'return')
      INTO n_replaced_assignments, n_replaced_returns
      FROM assignment_projection_duplicate;

    ALTER TABLE stock_movement DISABLE TRIGGER stock_movement_append_only;
    DELETE FROM stock_movement m USING assignment_projection_duplicate d WHERE m.id = d.movement_id;
    ALTER TABLE stock_movement ENABLE TRIGGER stock_movement_append_only;

    -- 4. Several open assignments of an asset.
    CREATE TEMPORARY TABLE assignment_projection_open ON COMMIT DROP AS
    SELECT m.id, m.organization_id, m.asset_id, m.assignee_user_id, m.assignee_team_id, m.occurred_at,
           row_number() OVER (PARTITION BY m.organization_id, m.asset_id
               ORDER BY (m.assignee_user_id IS NOT DISTINCT FROM a.assigned_user_id
                         AND m.assignee_team_id IS NOT DISTINCT FROM a.assigned_team_id) DESC,
                        m.occurred_at DESC, m.id DESC) AS rank
      FROM stock_movement m
      JOIN asset a ON a.id = m.asset_id AND a.organization_id = m.organization_id
     WHERE m.movement_type = 'assignment'
       AND NOT EXISTS (SELECT 1 FROM stock_movement r
                        WHERE r.organization_id = m.organization_id AND r.asset_id = m.asset_id
                          AND r.movement_type = 'return'
                          AND r.assignee_user_id IS NOT DISTINCT FROM m.assignee_user_id
                          AND r.assignee_team_id IS NOT DISTINCT FROM m.assignee_team_id
                          AND r.occurred_at > m.occurred_at);

    INSERT INTO stock_movement (
        organization_id, item_kind, asset_id, movement_type, assignee_user_id, assignee_team_id,
        occurred_at, reason, created_at)
    SELECT o.organization_id, 'asset', o.asset_id, 'return', o.assignee_user_id, o.assignee_team_id,
           CASE WHEN o.assignee_user_id IS NOT DISTINCT FROM k.assignee_user_id
                     AND o.assignee_team_id IS NOT DISTINCT FROM k.assignee_team_id
                THEN GREATEST(k.occurred_at - interval '1 microsecond', o.occurred_at + interval '1 microsecond')
                ELSE GREATEST(now(), o.occurred_at + interval '1 microsecond') END,
           'migration: superseded', now()
      FROM assignment_projection_open o
      JOIN assignment_projection_open k ON k.organization_id = o.organization_id AND k.asset_id = o.asset_id AND k.rank = 1
     WHERE o.rank > 1;
    GET DIAGNOSTICS n_superseded = ROW_COUNT;

    RAISE NOTICE 'assignment projection (000257): % assignments booked already, % taken over as assignment movements with % return movements (% returns of the service kept), % assignment and % return movements replaced as duplicates, % document links moved, % open assignments superseded, % assignments without asset and % with a user of another organization quarantined',
        n_booked, n_imported, n_returns, n_kept, n_replaced_assignments, n_replaced_returns, n_relinked, n_superseded,
        n_without_asset, n_foreign_user;

    DROP TABLE assignment_projection_open;
    DROP TABLE assignment_projection_return;
    DROP TABLE assignment_projection_duplicate;
    DROP TABLE assignment_projection_row;
END
$$;

-- ─── 5. Projection ─────────────────────────────────────────────────────────────

CREATE VIEW assignment_projection WITH (security_invoker = true) AS
SELECT m.id,
       m.organization_id,
       m.asset_id,
       m.assignee_user_id AS assigned_to,
       m.assignee_team_id AS assigned_team_id,
       m.actor_id AS assigned_by,
       CASE WHEN EXISTS (SELECT 1 FROM stock_movement t
                          WHERE t.organization_id = m.organization_id AND t.asset_id = m.asset_id
                            AND t.movement_type = 'return' AND t.occurred_at = m.occurred_at)
            THEN 'transfer' ELSE 'assignment' END AS assignment_type,
       CASE WHEN r.id IS NULL THEN CASE WHEN m.due_at < now() THEN 'overdue' ELSE 'active' END
            WHEN EXISTS (SELECT 1 FROM stock_movement n
                          WHERE n.organization_id = r.organization_id AND n.asset_id = r.asset_id
                            AND n.movement_type = 'assignment' AND n.occurred_at = r.occurred_at)
            THEN 'transferred'
            ELSE 'returned' END AS status,
       m.occurred_at AS assigned_at,
       m.due_at,
       (m.due_at AT TIME ZONE (SELECT o.timezone FROM organization o WHERE o.id = m.organization_id))::date AS due_date,
       r.occurred_at AS returned_at,
       r.reason AS return_condition,
       m.notes,
       m.created_at,
       COALESCE(r.created_at, m.created_at) AS updated_at
  FROM stock_movement m
  LEFT JOIN LATERAL (
        SELECT x.id, x.organization_id, x.asset_id, x.occurred_at, x.reason, x.created_at
          FROM stock_movement x
         WHERE x.organization_id = m.organization_id AND x.asset_id = m.asset_id AND x.movement_type = 'return'
           AND x.assignee_user_id IS NOT DISTINCT FROM m.assignee_user_id
           AND x.assignee_team_id IS NOT DISTINCT FROM m.assignee_team_id
           AND x.occurred_at > m.occurred_at
         ORDER BY x.occurred_at, x.id
         LIMIT 1) r ON true
 WHERE m.movement_type = 'assignment' AND m.asset_id IS NOT NULL;
