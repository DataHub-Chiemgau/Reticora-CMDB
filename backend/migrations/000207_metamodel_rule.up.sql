-- Migration 000207: rule catalog metamodel_rule (WP-716, MET-45, API-06,
-- AUD-07, E-111).
--
-- 1. metamodel_rule holds named rules of the catalog: each rule targets one
--    attribute (attribute_key) of exactly one target, a CI type, an
--    attribute set or a template version (E-111: template rules bind to the
--    version, not to the template). kind is validation (the value rule ast
--    of api/rules.schema.json, required exactly then; allowed_values_when
--    becomes validation with a condition and enum), required, readonly or
--    visible. condition is a condition of MET-45 (NULL = always). message is
--    an optional i18n text. A row with organization_id NULL is a system rule
--    (E-10): the application only reads it. The table is named
--    metamodel_rule because compliance_rule and /api/v1/compliance/rules
--    already use the name rule; the REST path stays /api/v1/rules (E-38).
-- 2. Keys of type and set rules are unique per organization and among the
--    system rules; keys of template rules are unique per template version.
-- 3. Foreign keys are checked without row level security, so the binding
--    trigger refuses a target the transaction does not see or that belongs
--    to another organization (pattern template_binding, 000205).
-- 4. metamodel_rule_copy_template_version copies the rules of the previous
--    version (the highest version below the new one) when a template
--    version is created (WP-251 creates a new version as a copy); the keys
--    are kept, the copies start at version 1.
-- 5. Row level security with ENABLE and FORCE and separate policies
--    (pattern 000059): the organization's and the system rules are
--    readable, only the organization's rules are writable; truncate guard
--    of WP-352.

CREATE TABLE metamodel_rule (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id     UUID REFERENCES organization (id) ON DELETE CASCADE,
    key                 TEXT NOT NULL,
    attribute_key       TEXT NOT NULL,
    kind                TEXT NOT NULL
        CONSTRAINT metamodel_rule_kind_check CHECK (kind IN ('validation', 'required', 'readonly', 'visible')),
    condition           JSONB,
    ast                 JSONB,
    message             JSONB
        CONSTRAINT metamodel_rule_message_i18n CHECK (message IS NULL OR reticora_i18n_text_valid(message)),
    is_active           BOOLEAN NOT NULL DEFAULT true,
    version             BIGINT NOT NULL DEFAULT 1 CONSTRAINT metamodel_rule_version_check CHECK (version >= 1),
    ci_type_id          UUID REFERENCES ci_type (id) ON DELETE CASCADE,
    attribute_set_id    UUID REFERENCES attribute_set (id) ON DELETE CASCADE,
    template_version_id UUID REFERENCES template_version (id) ON DELETE CASCADE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT metamodel_rule_key_check CHECK (length(key) BETWEEN 1 AND 100),
    CONSTRAINT metamodel_rule_attribute_key_check CHECK (length(attribute_key) BETWEEN 1 AND 100),
    CONSTRAINT metamodel_rule_one_target CHECK (num_nonnulls(ci_type_id, attribute_set_id, template_version_id) = 1),
    CONSTRAINT metamodel_rule_ast_check CHECK ((kind = 'validation') = (ast IS NOT NULL))
);
CREATE UNIQUE INDEX metamodel_rule_org_key ON metamodel_rule (organization_id, key)
    WHERE organization_id IS NOT NULL AND template_version_id IS NULL;
CREATE UNIQUE INDEX metamodel_rule_system_key ON metamodel_rule (key)
    WHERE organization_id IS NULL AND template_version_id IS NULL;
CREATE UNIQUE INDEX metamodel_rule_template_version_key ON metamodel_rule (template_version_id, key)
    WHERE template_version_id IS NOT NULL;
CREATE INDEX idx_metamodel_rule_ci_type ON metamodel_rule (ci_type_id);
CREATE INDEX idx_metamodel_rule_attribute_set ON metamodel_rule (attribute_set_id);
CREATE INDEX idx_metamodel_rule_template_version ON metamodel_rule (template_version_id);
CREATE TRIGGER trg_metamodel_rule_updated_at BEFORE UPDATE ON metamodel_rule
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- The target must be visible and a row of the rule's organization or a
-- system row.
CREATE FUNCTION metamodel_rule_binding() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    ref_org UUID;
BEGIN
    IF NEW.ci_type_id IS NOT NULL THEN
        SELECT ct.organization_id INTO ref_org FROM ci_type ct WHERE ct.id = NEW.ci_type_id;
        IF NOT FOUND OR (ref_org IS NOT NULL AND ref_org IS DISTINCT FROM NEW.organization_id) THEN
            RAISE EXCEPTION 'ci type of another organization' USING ERRCODE = 'foreign_key_violation',
                CONSTRAINT = 'metamodel_rule_ci_type_id_fkey';
        END IF;
    END IF;
    IF NEW.attribute_set_id IS NOT NULL THEN
        SELECT s.organization_id INTO ref_org FROM attribute_set s WHERE s.id = NEW.attribute_set_id;
        IF NOT FOUND OR (ref_org IS NOT NULL AND ref_org IS DISTINCT FROM NEW.organization_id) THEN
            RAISE EXCEPTION 'attribute set of another organization' USING ERRCODE = 'foreign_key_violation',
                CONSTRAINT = 'metamodel_rule_attribute_set_id_fkey';
        END IF;
    END IF;
    IF NEW.template_version_id IS NOT NULL THEN
        SELECT v.organization_id INTO ref_org FROM template_version v WHERE v.id = NEW.template_version_id;
        IF NOT FOUND OR (ref_org IS NOT NULL AND ref_org IS DISTINCT FROM NEW.organization_id) THEN
            RAISE EXCEPTION 'template version of another organization' USING ERRCODE = 'foreign_key_violation',
                CONSTRAINT = 'metamodel_rule_template_version_id_fkey';
        END IF;
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_metamodel_rule_binding
    BEFORE INSERT OR UPDATE OF organization_id, ci_type_id, attribute_set_id, template_version_id ON metamodel_rule
    FOR EACH ROW EXECUTE FUNCTION metamodel_rule_binding();

ALTER TABLE metamodel_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE metamodel_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY metamodel_rule_isolation_select ON metamodel_rule FOR SELECT
    USING (organization_id IS NULL OR organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY metamodel_rule_isolation_insert ON metamodel_rule FOR INSERT
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY metamodel_rule_isolation_update ON metamodel_rule FOR UPDATE
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY metamodel_rule_isolation_delete ON metamodel_rule FOR DELETE
    USING (organization_id = current_setting('app.org_id')::UUID);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON metamodel_rule
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── Copy of the rules into a new template version ─────────────────────────
-- The rules of the previous version (the highest version of the template
-- below the new one) of the new version's organization; no audit entry per
-- copied rule.
CREATE FUNCTION metamodel_rule_copy_template_version() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    prev UUID;
BEGIN
    SELECT v.id INTO prev FROM template_version v
    WHERE v.template_id = NEW.template_id AND v.version < NEW.version
    ORDER BY v.version DESC LIMIT 1;
    IF prev IS NULL THEN
        RETURN NULL;
    END IF;
    INSERT INTO metamodel_rule (organization_id, key, attribute_key, kind, condition, ast, message, is_active,
        template_version_id)
    SELECT NEW.organization_id, r.key, r.attribute_key, r.kind, r.condition, r.ast, r.message, r.is_active, NEW.id
    FROM metamodel_rule r
    WHERE r.template_version_id = prev AND r.organization_id IS NOT DISTINCT FROM NEW.organization_id;
    RETURN NULL;
END
$$;
CREATE TRIGGER metamodel_rule_copy_template_version AFTER INSERT ON template_version
    FOR EACH ROW EXECUTE FUNCTION metamodel_rule_copy_template_version();
