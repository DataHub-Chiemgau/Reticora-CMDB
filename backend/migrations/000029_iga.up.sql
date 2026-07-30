-- Stage 11.6: Identity Governance and Administration (active provisioning).

INSERT INTO permission (key, resource, action, description) VALUES
    ('iga:read', 'iga', 'read', 'Read IGA connectors, tasks, reviews and drift findings'),
    ('iga:write', 'iga', 'write', 'Manage IGA provisioning, access reviews and drift remediation')
ON CONFLICT (key) DO UPDATE SET resource = EXCLUDED.resource, action = EXCLUDED.action, description = EXCLUDED.description, updated_at = now();

CREATE TABLE iga_connector (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    type            TEXT NOT NULL CHECK (type IN ('scim', 'relay')),
    base_url        TEXT,
    credential_id   UUID REFERENCES credential(id) ON DELETE SET NULL,
    collector_id    UUID REFERENCES collector(id) ON DELETE SET NULL,
    capabilities    JSONB NOT NULL DEFAULT '{}'::jsonb,
    config          JSONB NOT NULL DEFAULT '{}'::jsonb,
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'error')),
    last_sync_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name),
    CONSTRAINT iga_connector_scim_url CHECK (type <> 'scim' OR base_url LIKE 'https://%'),
    CONSTRAINT iga_connector_relay_collector CHECK (type <> 'relay' OR collector_id IS NOT NULL)
);

CREATE TABLE iga_provisioning_task (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    connector_id    UUID NOT NULL REFERENCES iga_connector(id) ON DELETE CASCADE,
    user_id         UUID REFERENCES app_user(id) ON DELETE SET NULL,
    external_id     TEXT,
    action          TEXT NOT NULL CHECK (action IN ('create_account','update_account','disable_account','delete_account','add_group_member','remove_group_member')),
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','succeeded','failed','cancelled')),
    payload         JSONB NOT NULL DEFAULT '{}'::jsonb,
    result          JSONB NOT NULL DEFAULT '{}'::jsonb,
    error           TEXT,
    attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts    INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts > 0),
    next_run_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_run_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE iga_lifecycle_policy (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    event           TEXT NOT NULL CHECK (event IN ('joiner','mover','leaver','identity_changed')),
    priority        INTEGER NOT NULL DEFAULT 0,
    active          BOOLEAN NOT NULL DEFAULT true,
    conditions      JSONB NOT NULL DEFAULT '{}'::jsonb,
    actions         JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

CREATE TABLE iga_access_request (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id  UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    requester_id     UUID REFERENCES app_user(id) ON DELETE SET NULL,
    subject_user_id  UUID NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    connector_id     UUID REFERENCES iga_connector(id) ON DELETE SET NULL,
    entitlement      TEXT NOT NULL,
    reason           TEXT,
    status           TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','cancelled')),
    workflow_run_id  UUID REFERENCES workflow_run(id) ON DELETE SET NULL,
    decision_by      UUID REFERENCES app_user(id) ON DELETE SET NULL,
    decision_comment TEXT,
    decided_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE iga_access_review (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT,
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('draft','active','completed','cancelled')),
    due_at          TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE iga_access_review_item (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    review_id       UUID NOT NULL REFERENCES iga_access_review(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    connector_id    UUID REFERENCES iga_connector(id) ON DELETE SET NULL,
    entitlement     TEXT NOT NULL,
    decision        TEXT CHECK (decision IN ('approve','revoke')),
    decision_by     UUID REFERENCES app_user(id) ON DELETE SET NULL,
    decided_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE iga_drift_finding (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id     UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    connector_id        UUID NOT NULL REFERENCES iga_connector(id) ON DELETE CASCADE,
    external_id         TEXT NOT NULL,
    user_id             UUID REFERENCES app_user(id) ON DELETE SET NULL,
    drift_type          TEXT NOT NULL CHECK (drift_type IN ('orphan_account','missing_account','attribute_mismatch','membership_mismatch')),
    severity            TEXT NOT NULL DEFAULT 'medium' CHECK (severity IN ('low','medium','high','critical')),
    expected            JSONB NOT NULL DEFAULT '{}'::jsonb,
    observed            JSONB NOT NULL DEFAULT '{}'::jsonb,
    status              TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','accepted','remediating','resolved','dismissed')),
    remediation_task_id UUID REFERENCES iga_provisioning_task(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_iga_connector_org_type ON iga_connector(organization_id, type);
CREATE INDEX idx_iga_task_org_status ON iga_provisioning_task(organization_id, status, next_run_at);
CREATE INDEX idx_iga_lifecycle_org_event ON iga_lifecycle_policy(organization_id, event, active);
CREATE INDEX idx_iga_access_request_org_status ON iga_access_request(organization_id, status);
CREATE INDEX idx_iga_review_org_status ON iga_access_review(organization_id, status);
CREATE INDEX idx_iga_review_item_review ON iga_access_review_item(organization_id, review_id);
CREATE INDEX idx_iga_drift_org_status ON iga_drift_finding(organization_id, status);

ALTER TABLE iga_connector ENABLE ROW LEVEL SECURITY;
ALTER TABLE iga_connector FORCE ROW LEVEL SECURITY;
CREATE POLICY iga_connector_tenant_isolation ON iga_connector USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
ALTER TABLE iga_provisioning_task ENABLE ROW LEVEL SECURITY;
ALTER TABLE iga_provisioning_task FORCE ROW LEVEL SECURITY;
CREATE POLICY iga_task_tenant_isolation ON iga_provisioning_task USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
ALTER TABLE iga_lifecycle_policy ENABLE ROW LEVEL SECURITY;
ALTER TABLE iga_lifecycle_policy FORCE ROW LEVEL SECURITY;
CREATE POLICY iga_lifecycle_tenant_isolation ON iga_lifecycle_policy USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
ALTER TABLE iga_access_request ENABLE ROW LEVEL SECURITY;
ALTER TABLE iga_access_request FORCE ROW LEVEL SECURITY;
CREATE POLICY iga_access_request_tenant_isolation ON iga_access_request USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
ALTER TABLE iga_access_review ENABLE ROW LEVEL SECURITY;
ALTER TABLE iga_access_review FORCE ROW LEVEL SECURITY;
CREATE POLICY iga_review_tenant_isolation ON iga_access_review USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
ALTER TABLE iga_access_review_item ENABLE ROW LEVEL SECURITY;
ALTER TABLE iga_access_review_item FORCE ROW LEVEL SECURITY;
CREATE POLICY iga_review_item_tenant_isolation ON iga_access_review_item USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
ALTER TABLE iga_drift_finding ENABLE ROW LEVEL SECURITY;
ALTER TABLE iga_drift_finding FORCE ROW LEVEL SECURITY;
CREATE POLICY iga_drift_tenant_isolation ON iga_drift_finding USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
