-- Migration 000050: key management, training management, desk booking

-- Schlüsselmanagement (physical/digital key items with issue/return)
CREATE TABLE IF NOT EXISTS key_item (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id       UUID REFERENCES client(id),
    name            TEXT NOT NULL,
    key_type        TEXT NOT NULL DEFAULT 'physical' CHECK (key_type IN ('physical','digital')),
    identifier      TEXT,
    status          TEXT NOT NULL DEFAULT 'available' CHECK (status IN ('available','issued','lost','retired')),
    location        TEXT,
    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS key_assignment (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    key_item_id     UUID NOT NULL REFERENCES key_item(id) ON DELETE CASCADE,
    assigned_to     UUID NOT NULL REFERENCES app_user(id),
    issued_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    returned_at     TIMESTAMPTZ,
    issued_by       UUID,
    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Schulungsverwaltung (courses with due dates and proof)
CREATE TABLE IF NOT EXISTS training (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    title           TEXT NOT NULL,
    description     TEXT,
    category        TEXT,
    validity_months INT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS training_assignment (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    training_id     UUID NOT NULL REFERENCES training(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES app_user(id),
    assigned_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    due_at          TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    proof_object_key TEXT,
    status          TEXT NOT NULL DEFAULT 'assigned' CHECK (status IN ('assigned','completed','overdue','expired')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (training_id, user_id)
);

-- Arbeitsplatz-Buchung (wechselnde Arbeitsplätze)
CREATE TABLE IF NOT EXISTS desk (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    room_id         UUID REFERENCES room(id) ON DELETE SET NULL,
    name            TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'available' CHECK (status IN ('available','occupied','maintenance')),
    attributes      JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS desk_booking (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    desk_id         UUID NOT NULL REFERENCES desk(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES app_user(id),
    starts_at       TIMESTAMPTZ NOT NULL,
    ends_at         TIMESTAMPTZ NOT NULL,
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','cancelled','completed')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT desk_booking_range CHECK (ends_at > starts_at)
);

-- RLS for all
DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['key_item','key_assignment','training','training_assignment','desk','desk_booking'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_isolation', t);
        EXECUTE format('CREATE POLICY %I ON %I USING (organization_id = current_setting(''app.org_id'')::UUID) WITH CHECK (organization_id = current_setting(''app.org_id'')::UUID)', t || '_isolation', t);
    END LOOP;
END $$;

CREATE INDEX IF NOT EXISTS idx_key_item_org ON key_item(organization_id);
CREATE INDEX IF NOT EXISTS idx_key_assignment_key ON key_assignment(key_item_id);
CREATE INDEX IF NOT EXISTS idx_training_assignment_user ON training_assignment(user_id);
CREATE INDEX IF NOT EXISTS idx_desk_org ON desk(organization_id);
CREATE INDEX IF NOT EXISTS idx_desk_booking_desk ON desk_booking(desk_id);
CREATE INDEX IF NOT EXISTS idx_desk_booking_time ON desk_booking(desk_id, starts_at, ends_at);

INSERT INTO permission (key, resource, action, description) VALUES
    ('key:read', 'key', 'read', 'Read key items'),
    ('key:write', 'key', 'write', 'Manage key items and assignments'),
    ('training:read', 'training', 'read', 'Read trainings and assignments'),
    ('training:write', 'training', 'write', 'Manage trainings and assignments'),
    ('desk:read', 'desk', 'read', 'Read desks and bookings'),
    ('desk:write', 'desk', 'write', 'Manage desks and bookings')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, p.key
FROM role r
CROSS JOIN (VALUES ('key:read'), ('key:write'), ('training:read'), ('training:write'), ('desk:read'), ('desk:write')) AS p(key)
WHERE r.name IN ('org_admin', 'engineer')
ON CONFLICT DO NOTHING;

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, p.key
FROM role r
CROSS JOIN (VALUES ('key:read'), ('training:read'), ('desk:read')) AS p(key)
WHERE r.name IN ('viewer', 'client_technician')
ON CONFLICT DO NOTHING;
