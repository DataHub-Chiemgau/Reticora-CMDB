-- Migration 000212: form layouts per CI type, template and role (WP-259,
-- MET-50, API-06, AUD-07, E-110, E-111).
--
-- 1. form_layout holds the layout of a form: exactly one target, a CI type
--    or a template (form_layout_one_target; a template layout applies to
--    the template id, whatever its version, E-111), an optional role and
--    the sections in layout ({sections: [{key, title, visible_when,
--    fields: [{name, width}]}]}, checked by the application against the
--    effective fields of the target). There are no global rows: layouts of
--    system types are rows of the organization. version rises with every
--    change (If-Match, API-07). Drafts run over metamodel_draft (WP-260),
--    so there is no status column.
-- 2. At most one layout per target and role: two partial unique indexes
--    with NULLS NOT DISTINCT (no expression index, K22); key is the
--    natural key per organization (export and import, WP-262, WP-722).
-- 3. role_id names a role of the organization from role or custom_role
--    (the id set of GET /api/v1/roles). With two role tables it cannot be
--    a foreign key: the application checks it when writing, and the
--    AFTER DELETE triggers on role and custom_role remove the layouts of a
--    deleted role. Without app.org_id they see no rows under row level
--    security instead of failing.
-- 4. Row level security with ENABLE and FORCE and one policy FOR ALL
--    (pattern notification_channel_isolation, 000121); truncate guard of
--    WP-352.

CREATE TABLE form_layout (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    key             TEXT NOT NULL,
    ci_type_id      UUID REFERENCES ci_type (id) ON DELETE CASCADE,
    template_id     UUID REFERENCES template (id) ON DELETE CASCADE,
    role_id         UUID,
    priority        INT NOT NULL DEFAULT 0,
    version         BIGINT NOT NULL DEFAULT 1 CONSTRAINT form_layout_version_check CHECK (version >= 1),
    is_active       BOOLEAN NOT NULL DEFAULT true,
    layout          JSONB NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT form_layout_key_check CHECK (length(key) BETWEEN 1 AND 100),
    CONSTRAINT form_layout_one_target CHECK (num_nonnulls(ci_type_id, template_id) = 1),
    CONSTRAINT form_layout_org_key UNIQUE (organization_id, key)
);
CREATE UNIQUE INDEX form_layout_type_role ON form_layout (organization_id, ci_type_id, role_id) NULLS NOT DISTINCT
    WHERE ci_type_id IS NOT NULL;
CREATE UNIQUE INDEX form_layout_template_role ON form_layout (organization_id, template_id, role_id) NULLS NOT DISTINCT
    WHERE template_id IS NOT NULL;
CREATE INDEX idx_form_layout_ci_type ON form_layout (ci_type_id);
CREATE INDEX idx_form_layout_template ON form_layout (template_id);
CREATE INDEX idx_form_layout_role ON form_layout (role_id);
CREATE TRIGGER trg_form_layout_updated_at BEFORE UPDATE ON form_layout
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE form_layout ENABLE ROW LEVEL SECURITY;
ALTER TABLE form_layout FORCE ROW LEVEL SECURITY;
CREATE POLICY form_layout_isolation ON form_layout FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON form_layout
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── Layouts of a deleted role ─────────────────────────────────────────────
-- role_id is no foreign key (two role tables): deleting a role or a custom
-- role removes its layouts, under the caller's row level security.
CREATE FUNCTION form_layout_delete_role() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM form_layout WHERE role_id = OLD.id;
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_role_form_layout AFTER DELETE ON role
    FOR EACH ROW EXECUTE FUNCTION form_layout_delete_role();
CREATE TRIGGER trg_custom_role_form_layout AFTER DELETE ON custom_role
    FOR EACH ROW EXECUTE FUNCTION form_layout_delete_role();
