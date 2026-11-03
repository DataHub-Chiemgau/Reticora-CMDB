-- Migration 000199: attribute sets (WP-250, MET-16, API-06, AUD-07).
--
-- 1. attribute_set is a named, reusable group of attribute definitions. A
--    row with organization_id NULL is a system set (E-10): it must be
--    is_system, the application only reads it, and only migrations create
--    one. Keys are unique per organization and among the system sets.
-- 2. Set attributes are rows of attribute_definition (DB-03) with
--    scope = 'set' and attribute_set_id; there is no item table and there
--    are no override columns. They take the organization of their set
--    (attribute_definition_defaults). scope decides the layer: global has
--    neither type nor set, type a type, set a set. The unique keys of the
--    global layer (attribute_definition_global_key,
--    attribute_definition_system_global_key) cover scope 'global' only; a
--    set holds a key once.
-- 3. ci_type_attribute_set assigns sets to a type of the organization in an
--    ordered list (position). Foreign keys are checked without row level
--    security, so ci_type_attribute_set_binding (pattern
--    discovery_scope_binding, 000129) takes the organization from the type
--    and refuses a set the transaction does not see or that belongs to
--    another organization; a system set may be assigned.
-- 4. The base of the materialized schema (reticora_chain_attribute_schema,
--    called by generate_attribute_schema) and the regeneration trigger
--    count scope = 'global' as the system base instead of every row without
--    a type, so a set attribute (of an organization or of a system set)
--    neither enters ci_type.attribute_schema nor raises schema_version. The
--    bodies are those of 000197 (WP-248) apart from that predicate. Sets
--    enter the composition with WP-709.

-- ─── attribute_set ──────────────────────────────────────────────────────────
CREATE TABLE attribute_set (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID REFERENCES organization (id) ON DELETE CASCADE,
    key             TEXT NOT NULL,
    display_name    JSONB NOT NULL
        CONSTRAINT attribute_set_display_name_i18n CHECK (reticora_i18n_text_valid(display_name)),
    description     TEXT,
    is_system       BOOLEAN NOT NULL DEFAULT false,
    is_active       BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT attribute_set_key_check CHECK (length(key) BETWEEN 1 AND 100),
    CONSTRAINT attribute_set_org_or_global CHECK ((organization_id IS NULL AND is_system) OR organization_id IS NOT NULL)
);
CREATE UNIQUE INDEX attribute_set_org_key ON attribute_set (organization_id, key)
    WHERE organization_id IS NOT NULL;
CREATE UNIQUE INDEX attribute_set_system_key ON attribute_set (key)
    WHERE organization_id IS NULL;
CREATE TRIGGER trg_attribute_set_updated_at BEFORE UPDATE ON attribute_set
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE attribute_set ENABLE ROW LEVEL SECURITY;
ALTER TABLE attribute_set FORCE ROW LEVEL SECURITY;
CREATE POLICY attribute_set_isolation_select ON attribute_set FOR SELECT
    USING (organization_id IS NULL OR organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY attribute_set_isolation_insert ON attribute_set FOR INSERT
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY attribute_set_isolation_update ON attribute_set FOR UPDATE
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY attribute_set_isolation_delete ON attribute_set FOR DELETE
    USING (organization_id = current_setting('app.org_id')::UUID);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON attribute_set
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── Set attributes in attribute_definition ─────────────────────────────────
ALTER TABLE attribute_definition
    ADD COLUMN attribute_set_id UUID REFERENCES attribute_set (id) ON DELETE CASCADE;
-- The unique key is the index of attribute_set_id as well.
ALTER TABLE attribute_definition
    ADD CONSTRAINT attribute_definition_attribute_set_id_key_key UNIQUE (attribute_set_id, key);

ALTER TABLE attribute_definition DROP CONSTRAINT attribute_definition_scope_check;
ALTER TABLE attribute_definition ADD CONSTRAINT attribute_definition_scope_check
    CHECK (scope IN ('global', 'type', 'set'));
ALTER TABLE attribute_definition DROP CONSTRAINT attribute_definition_scope_type_check;
ALTER TABLE attribute_definition ADD CONSTRAINT attribute_definition_scope_type_check
    CHECK ((scope = 'global' AND ci_type_id IS NULL AND attribute_set_id IS NULL)
        OR (scope = 'type' AND ci_type_id IS NOT NULL AND attribute_set_id IS NULL)
        OR (scope = 'set' AND attribute_set_id IS NOT NULL AND ci_type_id IS NULL));

DROP INDEX attribute_definition_global_key;
DROP INDEX attribute_definition_system_global_key;
CREATE UNIQUE INDEX attribute_definition_global_key ON attribute_definition (organization_id, key)
    WHERE scope = 'global' AND organization_id IS NOT NULL;
CREATE UNIQUE INDEX attribute_definition_system_global_key ON attribute_definition (key)
    WHERE scope = 'global' AND organization_id IS NULL;

-- The organization follows the CI type or the set; a global attribute
-- keeps the organization it was written with (checked by the policies). A
-- set the transaction does not see leaves the organization NULL, which the
-- insert policy refuses. A missing display name is the key in both
-- languages.
CREATE OR REPLACE FUNCTION attribute_definition_defaults() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.ci_type_id IS NOT NULL THEN
        SELECT ct.organization_id INTO NEW.organization_id FROM ci_type ct WHERE ct.id = NEW.ci_type_id;
    ELSIF NEW.attribute_set_id IS NOT NULL THEN
        SELECT s.organization_id INTO NEW.organization_id FROM attribute_set s WHERE s.id = NEW.attribute_set_id;
    END IF;
    IF NEW.display_name IS NULL OR NEW.display_name = '{}'::jsonb THEN
        NEW.display_name := jsonb_build_object('de', NEW.key, 'en', NEW.key);
    END IF;
    RETURN NEW;
END
$$;

-- ─── ci_type_attribute_set ──────────────────────────────────────────────────
CREATE TABLE ci_type_attribute_set (
    organization_id  UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    ci_type_id       UUID NOT NULL REFERENCES ci_type (id) ON DELETE CASCADE,
    attribute_set_id UUID NOT NULL REFERENCES attribute_set (id) ON DELETE RESTRICT,
    position         INTEGER NOT NULL CONSTRAINT ci_type_attribute_set_position_check CHECK (position >= 0),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (ci_type_id, attribute_set_id),
    CONSTRAINT ci_type_attribute_set_position_key UNIQUE (ci_type_id, position)
);
CREATE INDEX idx_ci_type_attribute_set_set ON ci_type_attribute_set (attribute_set_id);

CREATE FUNCTION ci_type_attribute_set_binding() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    set_org UUID;
BEGIN
    SELECT ct.organization_id INTO NEW.organization_id FROM ci_type ct WHERE ct.id = NEW.ci_type_id;
    SELECT s.organization_id INTO set_org FROM attribute_set s WHERE s.id = NEW.attribute_set_id;
    IF NOT FOUND OR (set_org IS NOT NULL AND set_org IS DISTINCT FROM NEW.organization_id) THEN
        RAISE EXCEPTION 'attribute set of another organization' USING ERRCODE = 'foreign_key_violation',
            CONSTRAINT = 'ci_type_attribute_set_attribute_set_id_fkey';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_ci_type_attribute_set_binding BEFORE INSERT OR UPDATE ON ci_type_attribute_set
    FOR EACH ROW EXECUTE FUNCTION ci_type_attribute_set_binding();

ALTER TABLE ci_type_attribute_set ENABLE ROW LEVEL SECURITY;
ALTER TABLE ci_type_attribute_set FORCE ROW LEVEL SECURITY;
CREATE POLICY ci_type_attribute_set_isolation_select ON ci_type_attribute_set FOR SELECT
    USING (organization_id IS NULL OR organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY ci_type_attribute_set_isolation_insert ON ci_type_attribute_set FOR INSERT
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY ci_type_attribute_set_isolation_update ON ci_type_attribute_set FOR UPDATE
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY ci_type_attribute_set_isolation_delete ON ci_type_attribute_set FOR DELETE
    USING (organization_id = current_setting('app.org_id')::UUID);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON ci_type_attribute_set
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── System base: scope 'global' only ───────────────────────────────────────
-- The schema of a type p_type below p_parent: the system base, the
-- inheritable definitions of the chain root → p_parent and all definitions
-- of p_type, folded per key by compose_attribute_definition (MET-16).
CREATE OR REPLACE FUNCTION reticora_chain_attribute_schema(p_parent uuid, p_type uuid, p_key text) RETURNS jsonb
LANGUAGE plpgsql STABLE AS $$
DECLARE
    props jsonb := '{}'::jsonb;
    req jsonb := '[]'::jsonb;
    cur text;
    d attribute_definition;
    l record;
BEGIN
    FOR l IN
        SELECT x.def, (x.def).key AS key FROM (
            SELECT a AS def, 0 AS lvl FROM attribute_definition a
            WHERE a.organization_id IS NULL AND a.scope = 'global'
            UNION ALL
            SELECT a, c.level FROM ci_type_ancestors(p_parent) c
            JOIN attribute_definition a ON a.ci_type_id = c.id
            WHERE a.inheritable
            UNION ALL
            SELECT a, 1000 FROM attribute_definition a WHERE a.ci_type_id = p_type
        ) x ORDER BY (x.def).key, x.lvl
    LOOP
        IF cur IS NOT DISTINCT FROM l.key THEN
            d := compose_attribute_definition(d, l.def);
            CONTINUE;
        END IF;
        IF cur IS NOT NULL THEN
            props := props || jsonb_build_object(cur, attribute_json_schema(d));
            IF d.required THEN
                req := req || to_jsonb(cur);
            END IF;
        END IF;
        cur := l.key;
        d := l.def;
    END LOOP;
    IF cur IS NOT NULL THEN
        props := props || jsonb_build_object(cur, attribute_json_schema(d));
        IF d.required THEN
            req := req || to_jsonb(cur);
        END IF;
    END IF;
    RETURN jsonb_build_object(
        '$schema', 'https://json-schema.org/draft/2020-12/schema',
        '$id', 'urn:reticora:ci-type:' || p_key,
        'type', 'object',
        'properties', props,
        'required', req);
END
$$;

-- A change of the system base regenerates every type, a change of a type's
-- definition the type and its subtree; a set attribute regenerates nothing
-- (WP-709).
CREATE OR REPLACE FUNCTION attribute_definition_regenerate() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF (TG_OP IN ('UPDATE', 'DELETE') AND OLD.organization_id IS NULL AND OLD.scope = 'global')
       OR (TG_OP IN ('INSERT', 'UPDATE') AND NEW.organization_id IS NULL AND NEW.scope = 'global') THEN
        PERFORM regenerate_attribute_schema(t.id) FROM ci_type t;
        RETURN NULL;
    END IF;
    IF TG_OP IN ('UPDATE', 'DELETE') AND OLD.ci_type_id IS NOT NULL THEN
        PERFORM regenerate_attribute_schema(s.id) FROM ci_type_subtree(OLD.ci_type_id) s;
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') AND NEW.ci_type_id IS NOT NULL THEN
        PERFORM regenerate_attribute_schema(s.id) FROM ci_type_subtree(NEW.ci_type_id) s;
    END IF;
    RETURN NULL;
END
$$;
