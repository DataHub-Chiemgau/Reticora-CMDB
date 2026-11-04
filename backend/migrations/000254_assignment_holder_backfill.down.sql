-- Down of migration 000254: empties the holder cache of the asset; the
-- schema stays unchanged (000251 removes the columns).

UPDATE asset
   SET assigned_user_id = NULL, assigned_team_id = NULL, assignment_due_at = NULL
 WHERE assigned_user_id IS NOT NULL OR assigned_team_id IS NOT NULL OR assignment_due_at IS NOT NULL;
