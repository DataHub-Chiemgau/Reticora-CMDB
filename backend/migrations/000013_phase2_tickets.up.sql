-- Phase 2: Ticket System (Essential)
-- Lightweight ticketing for tracking issues, requests, and tasks.

CREATE TABLE IF NOT EXISTS ticket (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id),
    ticket_number   SERIAL,
    title           TEXT NOT NULL,
    description     TEXT,
    status          TEXT NOT NULL DEFAULT 'open',
    priority        TEXT NOT NULL DEFAULT 'medium',
    category        TEXT NOT NULL DEFAULT 'incident',
    reporter_id     UUID NOT NULL REFERENCES app_user(id),
    assignee_id     UUID REFERENCES app_user(id),
    team_id         UUID,
    related_ci_id   UUID REFERENCES ci(id),
    related_asset_id UUID REFERENCES asset(id),
    due_date        TIMESTAMPTZ,
    resolved_at     TIMESTAMPTZ,
    closed_at       TIMESTAMPTZ,
    tags            TEXT[] DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ticket_status_check CHECK (status IN (
        'open', 'in_progress', 'waiting', 'resolved', 'closed'
    )),
    CONSTRAINT ticket_priority_check CHECK (priority IN (
        'low', 'medium', 'high', 'critical'
    )),
    CONSTRAINT ticket_category_check CHECK (category IN (
        'incident', 'request', 'problem', 'change', 'task'
    ))
);

CREATE TABLE IF NOT EXISTS ticket_comment (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id),
    ticket_id       UUID NOT NULL REFERENCES ticket(id) ON DELETE CASCADE,
    author_id       UUID NOT NULL REFERENCES app_user(id),
    content         TEXT NOT NULL,
    is_internal     BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ticket_org ON ticket(organization_id);
CREATE INDEX idx_ticket_status ON ticket(organization_id, status);
CREATE INDEX idx_ticket_assignee ON ticket(assignee_id);
CREATE INDEX idx_ticket_reporter ON ticket(reporter_id);
CREATE INDEX idx_ticket_team ON ticket(team_id);
CREATE INDEX idx_ticket_number ON ticket(organization_id, ticket_number);
CREATE INDEX idx_ticket_comment_ticket ON ticket_comment(ticket_id);

ALTER TABLE ticket ENABLE ROW LEVEL SECURITY;
CREATE POLICY ticket_tenant_isolation ON ticket
    USING (organization_id = current_setting('app.current_org')::UUID);

ALTER TABLE ticket_comment ENABLE ROW LEVEL SECURITY;
CREATE POLICY ticket_comment_tenant_isolation ON ticket_comment
    USING (organization_id = current_setting('app.current_org')::UUID);
