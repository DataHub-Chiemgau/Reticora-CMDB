-- Migration 000197: type inheritance over ci_type.parent_type_id (WP-248,
-- MET-06, MET-01) and category and icon of the system types (E-117).
--
-- 1. ci_type.parent_type_id names the parent type. The foreign key is plain
--    (ON DELETE NO ACTION): system types have organization_id NULL, so a
--    composite key with the organization (K19) cannot express "same
--    organization or a system type"; trg_ci_type_parent_guard checks that.
--    The organization cascade of 000002 deletes parents and children in one
--    statement.
-- 2. ci_type_ancestors(uuid) is the chain root → type (level 1 = root),
--    ci_type_subtree(uuid) the type and every descendant row level security
--    shows (depth 0 = the type). Both are SECURITY INVOKER: RLS applies.
-- 3. trg_ci_type_parent_guard (BEFORE INSERT OR UPDATE, before
--    trg_ci_type_schema_guard by name) serializes the tree changes of an
--    organization (advisory lock, pattern location_tree of 000062), reads
--    the parent under RLS (an invisible parent is unknown) and refuses an
--    unknown or foreign parent (ci_type_parent_unknown), an organization
--    parent of a system type (ci_type_parent_system), a cycle
--    (ci_type_parent_cycle) and a chain of more than 6 types including the
--    height of the moved subtree (ci_type_parent_depth).
-- 4. The materialized schema composes system base → type chain: of the
--    ancestors only definitions with inheritable, of the type all
--    (compose_attribute_definition folds them, MET-16). A definition change
--    regenerates the type and its subtree, a parent change the moved
--    subtree; schema_version rises only where the schema changed.
-- 5. Data (E-117): category of the 17 physical system types of 000021 from
--    the catalogue citype.Templates(), icon (lucide icon name) of all 23
--    system types. The 6 logical types of 000104 keep their category.

ALTER TABLE ci_type ADD COLUMN parent_type_id uuid REFERENCES ci_type(id);
CREATE INDEX idx_ci_type_parent ON ci_type (parent_type_id) WHERE parent_type_id IS NOT NULL;

-- The chain root → p_type; level 1 is the root.
CREATE FUNCTION ci_type_ancestors(p_type uuid) RETURNS TABLE (id uuid, level integer)
LANGUAGE sql STABLE SECURITY INVOKER AS $$
    WITH RECURSIVE up (id, parent, dist) AS (
        SELECT t.id, t.parent_type_id, 0 FROM ci_type t WHERE t.id = p_type
        UNION ALL
        SELECT t.id, t.parent_type_id, up.dist + 1 FROM up JOIN ci_type t ON t.id = up.parent
        WHERE up.dist < 64
    )
    SELECT up.id, (max(up.dist) OVER () - up.dist + 1)::integer FROM up ORDER BY up.dist DESC
$$;

-- p_type and every visible descendant; depth 0 is p_type.
CREATE FUNCTION ci_type_subtree(p_type uuid) RETURNS TABLE (id uuid, depth integer)
LANGUAGE sql STABLE SECURITY INVOKER AS $$
    WITH RECURSIVE down (id, depth) AS (
        SELECT t.id, 0 FROM ci_type t WHERE t.id = p_type
        UNION ALL
        SELECT t.id, down.depth + 1 FROM down JOIN ci_type t ON t.parent_type_id = down.id
        WHERE down.depth < 64
    )
    SELECT down.id, down.depth FROM down ORDER BY down.depth
$$;

CREATE FUNCTION ci_type_parent_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    parent_org uuid;
    chain_length integer;
    height integer := 1;
BEGIN
    IF NEW.parent_type_id IS NULL
       OR (TG_OP = 'UPDATE' AND NEW.parent_type_id IS NOT DISTINCT FROM OLD.parent_type_id) THEN
        RETURN NEW;
    END IF;
    -- New children and re-parentings of one organization run one after
    -- the other; the tree is read after the lock with a fresh snapshot.
    PERFORM pg_advisory_xact_lock(hashtextextended('ci_type_tree:' || COALESCE(NEW.organization_id::text, 'system'), 0));

    SELECT t.organization_id INTO parent_org FROM ci_type t WHERE t.id = NEW.parent_type_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'parent ci type not found'
            USING ERRCODE = 'foreign_key_violation', CONSTRAINT = 'ci_type_parent_unknown';
    END IF;
    IF NEW.organization_id IS NULL AND parent_org IS NOT NULL THEN
        RAISE EXCEPTION 'a system ci type has only system ci types as parent'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'ci_type_parent_system';
    END IF;
    IF parent_org IS NOT NULL AND parent_org IS DISTINCT FROM NEW.organization_id THEN
        RAISE EXCEPTION 'parent ci type not found'
            USING ERRCODE = 'foreign_key_violation', CONSTRAINT = 'ci_type_parent_unknown';
    END IF;
    IF NEW.parent_type_id = NEW.id
       OR EXISTS (SELECT 1 FROM ci_type_ancestors(NEW.parent_type_id) a WHERE a.id = NEW.id) THEN
        RAISE EXCEPTION 'the parent ci type would make the type its own ancestor'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'ci_type_parent_cycle';
    END IF;
    SELECT count(*) INTO chain_length FROM ci_type_ancestors(NEW.parent_type_id);
    IF TG_OP = 'UPDATE' THEN
        SELECT COALESCE(max(s.depth), 0) + 1 INTO height FROM ci_type_subtree(NEW.id) s;
    END IF;
    IF chain_length + height > 6 THEN
        RAISE EXCEPTION 'a ci type chain has at most 6 levels (here %)', chain_length + height
            USING ERRCODE = 'check_violation', CONSTRAINT = 'ci_type_parent_depth';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_ci_type_parent_guard BEFORE INSERT OR UPDATE ON ci_type
    FOR EACH ROW EXECUTE FUNCTION ci_type_parent_guard();

-- The schema of a type p_type below p_parent: the system base, the
-- inheritable definitions of the chain root → p_parent and all definitions
-- of p_type, folded per key by compose_attribute_definition (MET-16).
CREATE FUNCTION reticora_chain_attribute_schema(p_parent uuid, p_type uuid, p_key text) RETURNS jsonb
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
            WHERE a.organization_id IS NULL AND a.ci_type_id IS NULL
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

-- The materialized schema of a type: system base → type chain; p_type NULL
-- gives the base alone.
CREATE OR REPLACE FUNCTION generate_attribute_schema(p_type uuid, p_key text) RETURNS jsonb
LANGUAGE plpgsql STABLE AS $$
BEGIN
    RETURN reticora_chain_attribute_schema(
        (SELECT t.parent_type_id FROM ci_type t WHERE t.id = p_type), p_type, p_key);
END
$$;

-- A change of the system base regenerates every type, a change of a type's
-- definition the type and its subtree.
CREATE OR REPLACE FUNCTION attribute_definition_regenerate() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF (TG_OP IN ('UPDATE', 'DELETE') AND OLD.organization_id IS NULL AND OLD.ci_type_id IS NULL)
       OR (TG_OP IN ('INSERT', 'UPDATE') AND NEW.organization_id IS NULL AND NEW.ci_type_id IS NULL) THEN
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

-- A new type starts with the schema of its chain: the system base and the
-- inheritable definitions of its ancestors.
CREATE OR REPLACE FUNCTION ci_type_schema_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF current_setting('reticora.schema_generation', true) IS NOT DISTINCT FROM 'on' THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'INSERT' THEN
        NEW.attribute_schema := reticora_chain_attribute_schema(NEW.parent_type_id, NULL, COALESCE(NEW.key, NEW.name));
        NEW.required_fields := ARRAY(SELECT jsonb_array_elements_text(NEW.attribute_schema -> 'required'));
        NEW.schema_version := 1;
    ELSIF NEW.attribute_schema IS DISTINCT FROM OLD.attribute_schema
       OR NEW.required_fields IS DISTINCT FROM OLD.required_fields
       OR NEW.schema_version IS DISTINCT FROM OLD.schema_version THEN
        RAISE EXCEPTION 'attribute_schema, required_fields and schema_version are generated from attribute_definition (DB-03)'
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

-- A parent change regenerates the moved subtree.
CREATE FUNCTION ci_type_parent_regenerate() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.parent_type_id IS DISTINCT FROM OLD.parent_type_id THEN
        PERFORM regenerate_attribute_schema(s.id) FROM ci_type_subtree(NEW.id) s;
    END IF;
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_ci_type_parent_regenerate AFTER UPDATE OF parent_type_id ON ci_type
    FOR EACH ROW EXECUTE FUNCTION ci_type_parent_regenerate();

-- E-117: category of the physical system types (catalogue of
-- citype.Templates()) and the icon of every system type.
UPDATE ci_type t SET category = v.category
FROM (VALUES
    ('switch', 'network'), ('router', 'network'), ('firewall', 'network'),
    ('access_point', 'network'), ('patch_panel', 'network'),
    ('server', 'hardware'),
    ('hypervisor', 'infrastructure'), ('vm', 'infrastructure'),
    ('client', 'end_user'), ('ip_phone', 'end_user'),
    ('pdu', 'power'), ('ups', 'power'),
    ('nas', 'storage'), ('storage_array', 'storage'),
    ('printer', 'peripheral'),
    ('camera', 'iot'),
    ('generic_device', 'generic')
) v (key, category)
WHERE t.organization_id IS NULL AND t.key = v.key;

UPDATE ci_type t SET icon = v.icon
FROM (VALUES
    ('switch', 'network'), ('router', 'router'), ('firewall', 'shield'),
    ('access_point', 'wifi'), ('patch_panel', 'cable'), ('server', 'server'),
    ('hypervisor', 'layers'), ('vm', 'box'), ('client', 'laptop'),
    ('ip_phone', 'phone'), ('pdu', 'plug-zap'), ('ups', 'battery-charging'),
    ('nas', 'hard-drive'), ('storage_array', 'hard-drive'), ('printer', 'printer'),
    ('camera', 'camera'), ('generic_device', 'cpu'),
    ('business_service', 'briefcase'), ('application', 'app-window'),
    ('database', 'database'), ('cluster', 'boxes'), ('cloud_resource', 'cloud'),
    ('software', 'package')
) v (key, icon)
WHERE t.organization_id IS NULL AND t.key = v.key;
