-- WP-745 (AST-02, E-124): the holder cache of the asset and the due date
-- of an assignment in the ledger. No data (the backfill and the service
-- that keeps the cache follow in WP-275), no policy change.
--
-- 1. asset.assigned_user_id and asset.assigned_team_id name the current
--    holder, a user or a team (app_user.id, team.id: pseudonymous keys like
--    ticket.assignee_id, no vault surrogate; POST
--    /api/v1/users/{id}/anonymize removes the plain text). Foreign keys
--    check without RLS, so the references carry the organization
--    (app_user_id_organization_key of 000177, team_id_organization_key of
--    000150). They have no delete action: a user or team that holds an
--    asset is not deleted (DELETE /api/v1/teams/{id} answers 409); the
--    deletion of the organization takes assets, users and teams along in
--    one statement. assignment_due_at is the due date of the assignment
--    and exists only with a holder (asset_holder_due); there is at most one
--    holder (asset_single_holder). The holder is not the owner
--    (owner_user_id, owner_team_id of 000229), and assigned_team_id is no
--    scope column: the policies of asset stay, the visibility for
--    team-scoped roles follows owner_team_id (TEN-05).
--    The partial indexes serve the open objects of a user or team (TLC-04),
--    has_owner and the foreign key checks of a delete.
--    The trigger asset_history (000250) writes the three columns into the
--    value history like location_id: they are no mirror of another column
--    of the row (the reason the deprecated mirrors and serial_number_norm
--    are left out) but the only place of the current holder on the asset,
--    and GET /api/v1/assets/{id}/state reconstructs the DTO with them. The
--    trail of the assignments themselves is the ledger (AST-07).
-- 2. stock_movement.due_at is the due date of an assignment movement
--    (assignment.due_date had no column in the ledger of 000242).

-- ─── 1. Holder cache of the asset ────────────────────────────────────────────

ALTER TABLE asset
    ADD COLUMN assigned_user_id UUID,
    ADD COLUMN assigned_team_id UUID,
    ADD COLUMN assignment_due_at TIMESTAMPTZ;

ALTER TABLE asset
    ADD CONSTRAINT asset_assigned_user_id_fkey FOREIGN KEY (assigned_user_id, organization_id)
        REFERENCES app_user (id, organization_id),
    ADD CONSTRAINT asset_assigned_team_id_fkey FOREIGN KEY (assigned_team_id, organization_id)
        REFERENCES team (id, organization_id),
    ADD CONSTRAINT asset_single_holder CHECK (num_nonnulls(assigned_user_id, assigned_team_id) <= 1),
    ADD CONSTRAINT asset_holder_due CHECK (
        assignment_due_at IS NULL OR num_nonnulls(assigned_user_id, assigned_team_id) = 1);

CREATE INDEX idx_asset_assigned_user ON asset (organization_id, assigned_user_id) WHERE assigned_user_id IS NOT NULL;
CREATE INDEX idx_asset_assigned_team ON asset (organization_id, assigned_team_id) WHERE assigned_team_id IS NOT NULL;

-- ─── 2. Due date in the ledger ──────────────────────────────────────────────

ALTER TABLE stock_movement ADD COLUMN due_at TIMESTAMPTZ;
ALTER TABLE stock_movement
    ADD CONSTRAINT stock_movement_due_at_assignment CHECK (due_at IS NULL OR movement_type = 'assignment');
