-- Migration 000205: templates with versions (WP-251, MET-16, MET-53,
-- API-06, AUD-07).
--
-- 1. template is a versioned, ordered bundle of attribute definitions for
--    one CI type (MET-16). A row with organization_id NULL is a system
--    template (E-10): it must be is_system, the application only reads it,
--    and only migrations create one. Keys are unique per organization and
--    among the system templates (pattern ci_type_org_key_uniq and
--    ci_type_system_key_uniq of 000056). current_version is the last
--    published version (NULL until one is published); cloned_from_id names
--    the template a clone was made from. A CI references templates only
--    through ci_template (WP-710); ci has no template columns.
-- 2. template_version holds the versions of a template: draft, published
--    or retired. published_by is a user id without a foreign key, like
--    job.created_by (000115). Published and retired versions are immutable
--    (the application answers 409).
-- 3. template_version_item lists the attribute definitions of a version in
--    position order, each with optional overrides that only narrow
--    (required_override, default_override, validation_override; MET-16).
--    Templates introduce no attribute keys of their own (E-37).
-- 4. The organization of a version and of an item follows its template
--    (pattern 000059, section 2b); for a system template it is NULL, so the
--    insert policy refuses every write and system rows stay read-only.
--    Foreign keys are checked without row level security, so the binding
--    triggers refuse a CI type, a clone source or an attribute definition
--    the transaction does not see or that belongs to another organization
--    (pattern ci_type_attribute_set_binding, 000199; cf. 000136). The
--    clone source is checked on insert only, so ON DELETE SET NULL of
--    cloned_from_id does not run the binding again.
-- 5. Seed: the nine system templates the collector profiles name through
--    discovery.SystemTemplateKey (WP-581, E-37), each at the system type of
--    its profiles, with version 1 published and without items (E-37).

-- ─── template ───────────────────────────────────────────────────────────────
CREATE TABLE template (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID REFERENCES organization (id) ON DELETE CASCADE,
    key             TEXT NOT NULL,
    ci_type_id      UUID NOT NULL REFERENCES ci_type (id),
    display_name    JSONB NOT NULL
        CONSTRAINT template_display_name_i18n CHECK (reticora_i18n_text_valid(display_name)),
    description     TEXT,
    is_system       BOOLEAN NOT NULL DEFAULT false,
    is_active       BOOLEAN NOT NULL DEFAULT true,
    current_version INTEGER CONSTRAINT template_current_version_check CHECK (current_version >= 1),
    cloned_from_id  UUID REFERENCES template (id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT template_key_check CHECK (length(key) BETWEEN 1 AND 100),
    CONSTRAINT template_org_or_global CHECK ((organization_id IS NULL AND is_system) OR organization_id IS NOT NULL)
);
CREATE UNIQUE INDEX template_org_key ON template (organization_id, key)
    WHERE organization_id IS NOT NULL;
CREATE UNIQUE INDEX template_system_key ON template (key)
    WHERE organization_id IS NULL;
CREATE INDEX idx_template_ci_type ON template (ci_type_id);
CREATE INDEX idx_template_cloned_from ON template (cloned_from_id);
CREATE TRIGGER trg_template_updated_at BEFORE UPDATE ON template
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE FUNCTION template_binding() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    ref_org UUID;
BEGIN
    SELECT ct.organization_id INTO ref_org FROM ci_type ct WHERE ct.id = NEW.ci_type_id;
    IF NOT FOUND OR (ref_org IS NOT NULL AND ref_org IS DISTINCT FROM NEW.organization_id) THEN
        RAISE EXCEPTION 'ci type of another organization' USING ERRCODE = 'foreign_key_violation',
            CONSTRAINT = 'template_ci_type_id_fkey';
    END IF;
    IF TG_OP = 'INSERT' AND NEW.cloned_from_id IS NOT NULL THEN
        SELECT t.organization_id INTO ref_org FROM template t WHERE t.id = NEW.cloned_from_id;
        IF NOT FOUND OR (ref_org IS NOT NULL AND ref_org IS DISTINCT FROM NEW.organization_id) THEN
            RAISE EXCEPTION 'template of another organization' USING ERRCODE = 'foreign_key_violation',
                CONSTRAINT = 'template_cloned_from_id_fkey';
        END IF;
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_template_binding BEFORE INSERT OR UPDATE OF ci_type_id, organization_id ON template
    FOR EACH ROW EXECUTE FUNCTION template_binding();

ALTER TABLE template ENABLE ROW LEVEL SECURITY;
ALTER TABLE template FORCE ROW LEVEL SECURITY;
CREATE POLICY template_isolation_select ON template FOR SELECT
    USING (organization_id IS NULL OR organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY template_isolation_insert ON template FOR INSERT
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY template_isolation_update ON template FOR UPDATE
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY template_isolation_delete ON template FOR DELETE
    USING (organization_id = current_setting('app.org_id')::UUID);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON template
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── template_version ───────────────────────────────────────────────────────
CREATE TABLE template_version (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID REFERENCES organization (id) ON DELETE CASCADE,
    template_id     UUID NOT NULL REFERENCES template (id) ON DELETE CASCADE,
    version         INTEGER NOT NULL CONSTRAINT template_version_version_check CHECK (version >= 1),
    status          TEXT NOT NULL DEFAULT 'draft'
        CONSTRAINT template_version_status_check CHECK (status IN ('draft', 'published', 'retired')),
    published_at    TIMESTAMPTZ,
    published_by    UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT template_version_template_id_version_key UNIQUE (template_id, version),
    CONSTRAINT template_version_published_check CHECK ((status = 'draft') = (published_at IS NULL))
);
CREATE TRIGGER trg_template_version_updated_at BEFORE UPDATE ON template_version
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- The organization follows the template; a template the transaction does
-- not see is refused like a missing one.
CREATE FUNCTION template_version_set_org() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    SELECT t.organization_id INTO NEW.organization_id FROM template t WHERE t.id = NEW.template_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'template not found' USING ERRCODE = 'foreign_key_violation',
            CONSTRAINT = 'template_version_template_id_fkey';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_template_version_set_org
    BEFORE INSERT OR UPDATE OF template_id, organization_id ON template_version
    FOR EACH ROW EXECUTE FUNCTION template_version_set_org();

ALTER TABLE template_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE template_version FORCE ROW LEVEL SECURITY;
CREATE POLICY template_version_isolation_select ON template_version FOR SELECT
    USING (organization_id IS NULL OR organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY template_version_isolation_insert ON template_version FOR INSERT
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY template_version_isolation_update ON template_version FOR UPDATE
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY template_version_isolation_delete ON template_version FOR DELETE
    USING (organization_id = current_setting('app.org_id')::UUID);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON template_version
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── template_version_item ──────────────────────────────────────────────────
CREATE TABLE template_version_item (
    organization_id         UUID REFERENCES organization (id) ON DELETE CASCADE,
    template_version_id     UUID NOT NULL REFERENCES template_version (id) ON DELETE CASCADE,
    attribute_definition_id UUID NOT NULL REFERENCES attribute_definition (id),
    position                INTEGER NOT NULL CONSTRAINT template_version_item_position_check CHECK (position >= 0),
    required_override       BOOLEAN,
    default_override        JSONB,
    validation_override     JSONB,
    PRIMARY KEY (template_version_id, attribute_definition_id),
    CONSTRAINT template_version_item_position_key UNIQUE (template_version_id, position)
);
CREATE INDEX idx_template_version_item_definition ON template_version_item (attribute_definition_id);

CREATE FUNCTION template_version_item_binding() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    def_org UUID;
BEGIN
    SELECT v.organization_id INTO NEW.organization_id FROM template_version v WHERE v.id = NEW.template_version_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'template version not found' USING ERRCODE = 'foreign_key_violation',
            CONSTRAINT = 'template_version_item_template_version_id_fkey';
    END IF;
    SELECT a.organization_id INTO def_org FROM attribute_definition a WHERE a.id = NEW.attribute_definition_id;
    IF NOT FOUND OR (def_org IS NOT NULL AND def_org IS DISTINCT FROM NEW.organization_id) THEN
        RAISE EXCEPTION 'attribute definition of another organization' USING ERRCODE = 'foreign_key_violation',
            CONSTRAINT = 'template_version_item_attribute_definition_id_fkey';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_template_version_item_binding BEFORE INSERT OR UPDATE ON template_version_item
    FOR EACH ROW EXECUTE FUNCTION template_version_item_binding();

ALTER TABLE template_version_item ENABLE ROW LEVEL SECURITY;
ALTER TABLE template_version_item FORCE ROW LEVEL SECURITY;
CREATE POLICY template_version_item_isolation_select ON template_version_item FOR SELECT
    USING (organization_id IS NULL OR organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY template_version_item_isolation_insert ON template_version_item FOR INSERT
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY template_version_item_isolation_update ON template_version_item FOR UPDATE
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY template_version_item_isolation_delete ON template_version_item FOR DELETE
    USING (organization_id = current_setting('app.org_id')::UUID);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON template_version_item
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── Seed: system templates of the collector profiles (E-37) ────────────────
-- key → system CI type: the target keys of discovery.SystemTemplateKey and
-- the type the profiles mapping onto the key classify (storage →
-- storage_array is the plan's assumption under E-37).
INSERT INTO template (organization_id, key, ci_type_id, display_name, description, is_system, current_version)
SELECT NULL, s.key, ct.id, jsonb_build_object('de', s.de, 'en', s.en),
       'System template of the collector profiles (DIS-02)', true, 1
FROM (VALUES
    ('physical_server', 'server', 'Physischer Server', 'Physical Server'),
    ('switch', 'switch', 'Switch', 'Switch'),
    ('router', 'router', 'Router', 'Router'),
    ('firewall', 'firewall', 'Firewall', 'Firewall'),
    ('access_point', 'access_point', 'Access Point', 'Access Point'),
    ('hypervisor', 'hypervisor', 'Hypervisor', 'Hypervisor'),
    ('storage', 'storage_array', 'Speicher', 'Storage'),
    ('ups', 'ups', 'USV', 'UPS'),
    ('pdu', 'pdu', 'PDU', 'PDU')
) AS s (key, type_key, de, en)
JOIN ci_type ct ON ct.organization_id IS NULL AND ct.key = s.type_key;

INSERT INTO template_version (template_id, version, status, published_at)
SELECT t.id, 1, 'published', now() FROM template t WHERE t.organization_id IS NULL;
