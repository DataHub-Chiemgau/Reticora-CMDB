-- Stage 5: workflow builder and JSON-Schema backed forms.

INSERT INTO permission (key, resource, action, description) VALUES
    ('form:read', 'form', 'read', 'Read form definitions and submissions'),
    ('form:write', 'form', 'write', 'Manage form definitions and submissions'),
    ('workflow:read', 'workflow', 'read', 'Read workflow definitions and runs'),
    ('workflow:write', 'workflow', 'write', 'Manage and execute workflows')
ON CONFLICT (key) DO UPDATE SET resource = EXCLUDED.resource, action = EXCLUDED.action, description = EXCLUDED.description, updated_at = now();

CREATE TABLE IF NOT EXISTS form_def (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id       UUID REFERENCES client(id) ON DELETE SET NULL,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    schema          JSONB NOT NULL,
    ui_hints        JSONB NOT NULL DEFAULT '{}'::jsonb,
    active          BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);
CREATE INDEX IF NOT EXISTS idx_form_def_org_active ON form_def(organization_id, active);
CREATE INDEX IF NOT EXISTS idx_form_def_client ON form_def(organization_id, client_id);

CREATE TABLE IF NOT EXISTS form_submission (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    form_id         UUID NOT NULL REFERENCES form_def(id) ON DELETE CASCADE,
    values          JSONB NOT NULL DEFAULT '{}'::jsonb,
    submitted_by    UUID REFERENCES app_user(id) ON DELETE SET NULL,
    ci_id           UUID REFERENCES ci(id) ON DELETE SET NULL,
    ticket_id       UUID REFERENCES ticket(id) ON DELETE SET NULL,
    status          TEXT NOT NULL DEFAULT 'submitted',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT form_submission_status_check CHECK (status IN ('submitted', 'accepted', 'rejected', 'draft'))
);
CREATE INDEX IF NOT EXISTS idx_form_submission_form ON form_submission(organization_id, form_id);
CREATE INDEX IF NOT EXISTS idx_form_submission_status ON form_submission(organization_id, status);
CREATE INDEX IF NOT EXISTS idx_form_submission_ci ON form_submission(organization_id, ci_id);
CREATE INDEX IF NOT EXISTS idx_form_submission_ticket ON form_submission(organization_id, ticket_id);

CREATE TABLE IF NOT EXISTS workflow_def (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    trigger         JSONB NOT NULL DEFAULT '{}'::jsonb,
    conditions      JSONB NOT NULL DEFAULT '[]'::jsonb,
    actions         JSONB NOT NULL DEFAULT '[]'::jsonb,
    active          BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);
CREATE INDEX IF NOT EXISTS idx_workflow_def_org_active ON workflow_def(organization_id, active);

CREATE TABLE IF NOT EXISTS workflow_run (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    workflow_id     UUID NOT NULL REFERENCES workflow_def(id) ON DELETE CASCADE,
    status          TEXT NOT NULL DEFAULT 'pending',
    trigger         TEXT NOT NULL,
    context         JSONB NOT NULL DEFAULT '{}'::jsonb,
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT workflow_run_status_check CHECK (status IN ('pending', 'running', 'waiting_approval', 'succeeded', 'failed', 'cancelled'))
);
CREATE INDEX IF NOT EXISTS idx_workflow_run_workflow ON workflow_run(organization_id, workflow_id);
CREATE INDEX IF NOT EXISTS idx_workflow_run_status ON workflow_run(organization_id, status);

CREATE TABLE IF NOT EXISTS workflow_step (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    run_id          UUID NOT NULL REFERENCES workflow_run(id) ON DELETE CASCADE,
    step_index      INTEGER NOT NULL,
    action_type     TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    input           JSONB NOT NULL DEFAULT '{}'::jsonb,
    output          JSONB NOT NULL DEFAULT '{}'::jsonb,
    error           TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, run_id, step_index),
    CONSTRAINT workflow_step_status_check CHECK (status IN ('pending', 'running', 'waiting_approval', 'succeeded', 'failed', 'cancelled'))
);
CREATE INDEX IF NOT EXISTS idx_workflow_step_run ON workflow_step(organization_id, run_id, step_index);

ALTER TABLE form_def ENABLE ROW LEVEL SECURITY;
ALTER TABLE form_def FORCE ROW LEVEL SECURITY;
CREATE POLICY form_def_tenant_isolation ON form_def USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
ALTER TABLE form_submission ENABLE ROW LEVEL SECURITY;
ALTER TABLE form_submission FORCE ROW LEVEL SECURITY;
CREATE POLICY form_submission_tenant_isolation ON form_submission USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
ALTER TABLE workflow_def ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_def FORCE ROW LEVEL SECURITY;
CREATE POLICY workflow_def_tenant_isolation ON workflow_def USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
ALTER TABLE workflow_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_run FORCE ROW LEVEL SECURITY;
CREATE POLICY workflow_run_tenant_isolation ON workflow_run USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
ALTER TABLE workflow_step ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_step FORCE ROW LEVEL SECURITY;
CREATE POLICY workflow_step_tenant_isolation ON workflow_step USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
