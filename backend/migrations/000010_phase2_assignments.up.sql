-- Phase 2: Assignments (Zuweisung / Rückgabe / Transfer)
-- Tracks assignment of assets/CIs to users, including history.

CREATE TABLE IF NOT EXISTS assignment (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id),
    asset_id        UUID REFERENCES asset(id),
    ci_id           UUID REFERENCES ci(id),
    assigned_to     UUID NOT NULL REFERENCES app_user(id),
    assigned_by     UUID NOT NULL REFERENCES app_user(id),
    assignment_type TEXT NOT NULL DEFAULT 'assignment',
    status          TEXT NOT NULL DEFAULT 'active',
    assigned_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    due_date        DATE,
    returned_at     TIMESTAMPTZ,
    return_condition TEXT,
    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT assignment_type_check CHECK (assignment_type IN (
        'assignment', 'return', 'transfer'
    )),
    CONSTRAINT assignment_status_check CHECK (status IN (
        'active', 'returned', 'transferred', 'overdue', 'cancelled'
    )),
    CONSTRAINT assignment_target_check CHECK (asset_id IS NOT NULL OR ci_id IS NOT NULL)
);

CREATE INDEX idx_assignment_org ON assignment(organization_id);
CREATE INDEX idx_assignment_user ON assignment(assigned_to);
CREATE INDEX idx_assignment_asset ON assignment(asset_id);
CREATE INDEX idx_assignment_status ON assignment(organization_id, status);

ALTER TABLE assignment ENABLE ROW LEVEL SECURITY;
CREATE POLICY assignment_tenant_isolation ON assignment
    USING (organization_id = current_setting('app.current_org')::UUID);
