-- WP-275 (AST-02, AST-07): backfill of the holder cache of the asset
-- (000251) from the open rows of assignment. Data only, no schema change.
--
-- Per asset the newest open row (status active or overdue; assigned_at,
-- then id) decides the holder: assigned_user_id = assigned_to,
-- assignment_due_at = the end of the day due_date in organization.timezone
-- (000101), as the assignment service derives it. A row with only ci_id
-- belongs to the asset of its CI (ci.asset_id). Only users of the
-- organization of the asset are taken over (asset_assigned_user_id_fkey);
-- assets with several open rows and rows that cannot be taken over (no
-- asset, a user of another organization) are reported with RAISE NOTICE.
-- The older open rows are closed by WP-747. From now on the assignment
-- service (backend/internal/assignment/service.go) keeps the cache.
-- The update writes the value history of the asset (asset_history, 000250)
-- without an actor.

DO $$
DECLARE
    filled integer;
    several integer;
    without_asset integer;
    foreign_user integer;
BEGIN
    CREATE TEMPORARY TABLE assignment_holder_open ON COMMIT DROP AS
    SELECT s.id, s.organization_id, COALESCE(s.asset_id, c.asset_id) AS asset_id, s.assigned_to, s.due_date, s.assigned_at,
           EXISTS (SELECT 1 FROM app_user u WHERE u.id = s.assigned_to AND u.organization_id = s.organization_id) AS own_user
      FROM assignment s
      LEFT JOIN ci c ON s.asset_id IS NULL AND c.id = s.ci_id AND c.organization_id = s.organization_id
     WHERE s.status IN ('active', 'overdue');

    SELECT count(*) INTO without_asset FROM assignment_holder_open WHERE asset_id IS NULL;
    SELECT count(*) INTO several FROM (
        SELECT asset_id FROM assignment_holder_open WHERE asset_id IS NOT NULL GROUP BY asset_id HAVING count(*) > 1) m;

    CREATE TEMPORARY TABLE assignment_holder_newest ON COMMIT DROP AS
    SELECT DISTINCT ON (o.asset_id) o.*
      FROM assignment_holder_open o
     WHERE o.asset_id IS NOT NULL
     ORDER BY o.asset_id, o.assigned_at DESC, o.id DESC;

    SELECT count(*) INTO foreign_user FROM assignment_holder_newest WHERE NOT own_user;

    UPDATE asset a
       SET assigned_user_id = n.assigned_to,
           assigned_team_id = NULL,
           assignment_due_at = CASE WHEN n.due_date IS NULL THEN NULL
               ELSE ((n.due_date + 1)::timestamp AT TIME ZONE org.timezone) - interval '1 microsecond' END
      FROM assignment_holder_newest n
      JOIN organization org ON org.id = n.organization_id
     WHERE a.id = n.asset_id AND a.organization_id = n.organization_id AND n.own_user;
    GET DIAGNOSTICS filled = ROW_COUNT;

    RAISE NOTICE 'assignment holder backfill (000254): % assets with a holder, % assets with several open assignments (the newest counts), % open assignments without asset, % newest open assignments of a user of another organization skipped',
        filled, several, without_asset, foreign_user;

    DROP TABLE assignment_holder_newest;
    DROP TABLE assignment_holder_open;
END
$$;
