-- WP-026 (LOC-10, CH28, TEC-06): canonical location tree.
--
-- 1. Extension ltree.
-- 2. Table location: one row per node of the tree site > building > room >
--    rack and site > warehouse > zone > shelf > bin. path (ltree of node ids),
--    site_id and client_id are derived by trigger from the parent and never
--    taken from the caller; only a site carries its own client.
-- 3. The parent matrix and the cycle guard live in the trigger, so every
--    write path is checked by the database. Re-parenting serializes per
--    organization with an advisory lock.
-- 4. The specialist tables site, building, room and rack share their primary
--    key with their location row (1:1). Their triggers create, update and
--    delete the location row; a deferred constraint trigger rejects location
--    rows of these kinds without a specialist row.
-- 5. Backfill from site/building/room/rack and, through
--    location_import_nodes, from the storage nodes of location_node.
-- 6. RLS with organization, client and site predicate (TEN-05, CH25).

CREATE EXTENSION IF NOT EXISTS ltree;

-- ─── 1. Table ──────────────────────────────────────────────────────────────────

CREATE TABLE location (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id       UUID NOT NULL REFERENCES client(id) ON DELETE CASCADE,
    site_id         UUID NOT NULL REFERENCES location(id) ON DELETE CASCADE,
    parent_id       UUID REFERENCES location(id) ON DELETE CASCADE,
    kind            TEXT NOT NULL CHECK (kind IN ('site', 'building', 'room', 'rack', 'warehouse', 'zone', 'shelf', 'bin')),
    name            TEXT NOT NULL,
    path            LTREE NOT NULL UNIQUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT location_root_is_site CHECK ((kind = 'site') = (parent_id IS NULL))
);

CREATE INDEX idx_location_org ON location (organization_id);
CREATE INDEX idx_location_parent ON location (parent_id);
CREATE INDEX idx_location_client ON location (client_id);
CREATE INDEX idx_location_site ON location (site_id);
CREATE INDEX idx_location_path ON location USING GIST (path);

CREATE TRIGGER trg_location_updated_at BEFORE UPDATE ON location
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ─── 2. Matrix, derivation and cycle guard ─────────────────────────────────────

-- location_parent_kind is the parent matrix of LOC-10: the only allowed kind
-- of the parent of a node; NULL for the root kind site.
CREATE FUNCTION location_parent_kind(child TEXT) RETURNS TEXT
LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE child
        WHEN 'building' THEN 'site'
        WHEN 'room' THEN 'building'
        WHEN 'rack' THEN 'room'
        WHEN 'warehouse' THEN 'site'
        WHEN 'zone' THEN 'warehouse'
        WHEN 'shelf' THEN 'zone'
        WHEN 'bin' THEN 'shelf'
    END
$$;

-- location_label is the ltree label of a node id.
CREATE FUNCTION location_label(id UUID) RETURNS TEXT
LANGUAGE sql IMMUTABLE AS $$
    SELECT replace(id::text, '-', '_')
$$;

CREATE FUNCTION location_maintain() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    parent location%ROWTYPE;
    moved BOOLEAN := TRUE;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        moved := OLD.parent_id IS DISTINCT FROM NEW.parent_id;
        IF NEW.kind <> OLD.kind OR NEW.organization_id <> OLD.organization_id THEN
            RAISE EXCEPTION 'location kind and organization are immutable'
                USING ERRCODE = '23514', CONSTRAINT = 'location_immutable';
        END IF;
        -- Nodes with a specialist table change through that table only, so
        -- both stay in step. Its sync trigger runs one level deeper.
        IF NEW.kind IN ('site', 'building', 'room', 'rack') AND pg_trigger_depth() = 1
           AND (moved OR NEW.name <> OLD.name
                OR (NEW.kind = 'site' AND NEW.client_id <> OLD.client_id)) THEN
            RAISE EXCEPTION 'location % is a %; change it through table %', NEW.id, NEW.kind, NEW.kind
                USING ERRCODE = '23514', CONSTRAINT = 'location_specialist_owned';
        END IF;
    END IF;

    IF NEW.kind = 'site' THEN
        IF NEW.parent_id IS NOT NULL THEN
            RAISE EXCEPTION 'a site has no parent location'
                USING ERRCODE = '23514', CONSTRAINT = 'location_parent_matrix';
        END IF;
        IF NEW.client_id IS NULL THEN
            RAISE EXCEPTION 'a site needs a client'
                USING ERRCODE = '23502', COLUMN = 'client_id';
        END IF;
        NEW.site_id := NEW.id;
        NEW.path := location_label(NEW.id)::ltree;
        RETURN NEW;
    END IF;

    IF NEW.parent_id IS NULL THEN
        RAISE EXCEPTION 'a % needs a parent %', NEW.kind, location_parent_kind(NEW.kind)
            USING ERRCODE = '23514', CONSTRAINT = 'location_parent_matrix';
    END IF;

    IF moved AND TG_OP = 'UPDATE' THEN
        -- Concurrent re-parentings of one organization run one after the
        -- other; the parent is read after the lock with a fresh snapshot.
        PERFORM pg_advisory_xact_lock(hashtextextended('location_tree:' || NEW.organization_id::text, 0));
    END IF;

    SELECT * INTO parent FROM location WHERE id = NEW.parent_id;
    IF NOT FOUND OR parent.organization_id <> NEW.organization_id THEN
        RAISE EXCEPTION 'parent location % not found', NEW.parent_id
            USING ERRCODE = '23503', CONSTRAINT = 'location_parent_id_fkey';
    END IF;
    IF moved AND TG_OP = 'UPDATE' AND parent.path ~ ('*.' || location_label(NEW.id) || '.*')::lquery THEN
        RAISE EXCEPTION 'moving location % below % creates a cycle', NEW.id, parent.id
            USING ERRCODE = '23514', CONSTRAINT = 'location_no_cycle';
    END IF;
    IF parent.kind IS DISTINCT FROM location_parent_kind(NEW.kind) THEN
        RAISE EXCEPTION 'a % cannot be placed below a %', NEW.kind, parent.kind
            USING ERRCODE = '23514', CONSTRAINT = 'location_parent_matrix';
    END IF;

    NEW.client_id := parent.client_id;
    NEW.site_id := parent.site_id;
    NEW.path := parent.path || location_label(NEW.id);
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_location_maintain BEFORE INSERT OR UPDATE ON location
    FOR EACH ROW EXECUTE FUNCTION location_maintain();

-- location_propagate re-derives the children after a node changed path or
-- client; each child's maintain trigger recurses further down.
CREATE FUNCTION location_propagate() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE location SET path = path WHERE parent_id = NEW.id;
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_location_propagate AFTER UPDATE ON location
    FOR EACH ROW
    WHEN (OLD.path IS DISTINCT FROM NEW.path OR OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION location_propagate();

-- ─── 3. Specialist tables ──────────────────────────────────────────────────────

-- location_specialist_sync keeps the location row of a site, building, room
-- or rack in step. TG_ARGV[0] is the kind, TG_ARGV[1] the parent column
-- ('' for site).
CREATE FUNCTION location_specialist_sync() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_kind TEXT := TG_ARGV[0];
    v_row JSONB;
    v_parent UUID;
    v_client UUID;
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM location WHERE id = OLD.id;
        RETURN NULL;
    END IF;

    v_row := to_jsonb(NEW);
    IF TG_ARGV[1] <> '' THEN
        v_parent := (v_row ->> TG_ARGV[1])::uuid;
    ELSE
        v_client := (v_row ->> 'client_id')::uuid;
    END IF;

    IF TG_OP = 'INSERT' THEN
        INSERT INTO location (id, organization_id, client_id, kind, parent_id, name)
        VALUES (NEW.id, NEW.organization_id, v_client, v_kind, v_parent, NEW.name);
        RETURN NEW;
    END IF;

    UPDATE location l
       SET name = NEW.name, parent_id = v_parent, client_id = COALESCE(v_client, l.client_id)
     WHERE l.id = NEW.id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'location of % % is not visible', v_kind, NEW.id
            USING ERRCODE = '42501';
    END IF;
    RETURN NEW;
END
$$;

-- location_specialist_exists rejects, at commit, a location row of a
-- specialist kind that has no row in its specialist table.
CREATE FUNCTION location_specialist_exists() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    has_row BOOLEAN;
BEGIN
    IF NEW.kind NOT IN ('site', 'building', 'room', 'rack') THEN
        RETURN NULL;
    END IF;
    EXECUTE format('SELECT EXISTS (SELECT 1 FROM %I WHERE id = $1)', NEW.kind)
        INTO has_row USING NEW.id;
    IF NOT has_row AND EXISTS (SELECT 1 FROM location WHERE id = NEW.id) THEN
        RAISE EXCEPTION 'location % of kind % has no % row', NEW.id, NEW.kind, NEW.kind
            USING ERRCODE = '23503', CONSTRAINT = 'location_specialist_row';
    END IF;
    RETURN NULL;
END
$$;

-- ─── 4. Backfill ───────────────────────────────────────────────────────────────

INSERT INTO location (id, organization_id, client_id, kind, name)
SELECT id, organization_id, client_id, 'site', name FROM site;
INSERT INTO location (id, organization_id, kind, parent_id, name)
SELECT id, organization_id, 'building', site_id, name FROM building;
INSERT INTO location (id, organization_id, kind, parent_id, name)
SELECT id, organization_id, 'room', building_id, name FROM room;
INSERT INTO location (id, organization_id, kind, parent_id, name)
SELECT id, organization_id, 'rack', room_id, name FROM rack;

-- location_import_nodes copies the storage nodes (warehouse/zone/shelf/bin)
-- of location_node into the tree; they keep their ids. A parent node that
-- stands for a site/building/room/rack maps to that object. Nodes already in
-- the tree are skipped; nodes that do not fit the parent matrix are left out,
-- reported and counted. org NULL imports all organizations.
CREATE FUNCTION location_import_nodes(org UUID DEFAULT NULL) RETURNS INTEGER
LANGUAGE plpgsql AS $$
DECLARE
    node RECORD;
    skipped INTEGER := 0;
BEGIN
    FOR node IN
        WITH RECURSIVE tree AS (
            SELECT n.*, 0 AS depth FROM location_node n
             WHERE n.parent_id IS NULL AND (org IS NULL OR n.organization_id = org)
            UNION ALL
            SELECT n.*, t.depth + 1 FROM location_node n JOIN tree t ON n.parent_id = t.id
        )
        SELECT t.id, t.organization_id, t.node_type, t.name,
               CASE p.node_type
                   WHEN 'site' THEN p.site_id
                   WHEN 'building' THEN p.building_id
                   WHEN 'room' THEN p.room_id
                   WHEN 'rack' THEN p.rack_id
                   ELSE p.id
               END AS parent_location
          FROM tree t
          LEFT JOIN location_node p ON p.id = t.parent_id
         WHERE t.node_type IN ('warehouse', 'zone', 'shelf', 'bin')
           AND NOT EXISTS (SELECT 1 FROM location l WHERE l.id = t.id)
         ORDER BY t.depth
    LOOP
        BEGIN
            INSERT INTO location (id, organization_id, kind, parent_id, name)
            VALUES (node.id, node.organization_id, node.node_type, node.parent_location, node.name);
        EXCEPTION WHEN integrity_constraint_violation THEN
            skipped := skipped + 1;
            RAISE NOTICE 'location_node % (%) not imported: %', node.id, node.node_type, SQLERRM;
        END;
    END LOOP;
    RETURN skipped;
END
$$;

SELECT location_import_nodes();

ALTER TABLE site ADD CONSTRAINT site_location_fkey
    FOREIGN KEY (id) REFERENCES location(id) ON DELETE CASCADE;
ALTER TABLE building ADD CONSTRAINT building_location_fkey
    FOREIGN KEY (id) REFERENCES location(id) ON DELETE CASCADE;
ALTER TABLE room ADD CONSTRAINT room_location_fkey
    FOREIGN KEY (id) REFERENCES location(id) ON DELETE CASCADE;
ALTER TABLE rack ADD CONSTRAINT rack_location_fkey
    FOREIGN KEY (id) REFERENCES location(id) ON DELETE CASCADE;

CREATE TRIGGER trg_site_location_insert BEFORE INSERT ON site
    FOR EACH ROW EXECUTE FUNCTION location_specialist_sync('site', '');
CREATE TRIGGER trg_site_location_update AFTER UPDATE ON site
    FOR EACH ROW WHEN (OLD.name IS DISTINCT FROM NEW.name OR OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION location_specialist_sync('site', '');
CREATE TRIGGER trg_site_location_delete AFTER DELETE ON site
    FOR EACH ROW EXECUTE FUNCTION location_specialist_sync('site', '');

CREATE TRIGGER trg_building_location_insert BEFORE INSERT ON building
    FOR EACH ROW EXECUTE FUNCTION location_specialist_sync('building', 'site_id');
CREATE TRIGGER trg_building_location_update AFTER UPDATE ON building
    FOR EACH ROW WHEN (OLD.name IS DISTINCT FROM NEW.name OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION location_specialist_sync('building', 'site_id');
CREATE TRIGGER trg_building_location_delete AFTER DELETE ON building
    FOR EACH ROW EXECUTE FUNCTION location_specialist_sync('building', 'site_id');

CREATE TRIGGER trg_room_location_insert BEFORE INSERT ON room
    FOR EACH ROW EXECUTE FUNCTION location_specialist_sync('room', 'building_id');
CREATE TRIGGER trg_room_location_update AFTER UPDATE ON room
    FOR EACH ROW WHEN (OLD.name IS DISTINCT FROM NEW.name OR OLD.building_id IS DISTINCT FROM NEW.building_id)
    EXECUTE FUNCTION location_specialist_sync('room', 'building_id');
CREATE TRIGGER trg_room_location_delete AFTER DELETE ON room
    FOR EACH ROW EXECUTE FUNCTION location_specialist_sync('room', 'building_id');

CREATE TRIGGER trg_rack_location_insert BEFORE INSERT ON rack
    FOR EACH ROW EXECUTE FUNCTION location_specialist_sync('rack', 'room_id');
CREATE TRIGGER trg_rack_location_update AFTER UPDATE ON rack
    FOR EACH ROW WHEN (OLD.name IS DISTINCT FROM NEW.name OR OLD.room_id IS DISTINCT FROM NEW.room_id)
    EXECUTE FUNCTION location_specialist_sync('rack', 'room_id');
CREATE TRIGGER trg_rack_location_delete AFTER DELETE ON rack
    FOR EACH ROW EXECUTE FUNCTION location_specialist_sync('rack', 'room_id');

-- ─── 5. RLS ────────────────────────────────────────────────────────────────────

ALTER TABLE location ENABLE ROW LEVEL SECURITY;
ALTER TABLE location FORCE ROW LEVEL SECURITY;

CREATE POLICY location_isolation ON location
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    );

-- Created last: pending deferred events would block the ALTER TABLE above.
CREATE CONSTRAINT TRIGGER trg_location_specialist_exists AFTER INSERT ON location
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION location_specialist_exists();
