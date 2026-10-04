-- WP-029 (MGT-05..07, TKT-01, TEN-05, CH25, E-11): team, room and person
-- reference.
--
-- Team scope intersects with client and site scope (E-11, provisional): a row
-- must pass every dimension.
--
-- 1. ticket: team predicate on team_id in addition to client and site
--    (migration 000064). Tickets without team stay readable; only principals
--    without team restriction write them (E-09 analog).
-- 2. desk and desk_booking: client_id/site_id derived from the room's
--    location (migration 000062), bookings from their desk; client and site
--    predicate. Moves of the room's tree propagate to desks and bookings.
-- 3. training_assignment: a team-restricted principal sees and writes the
--    assignments of users who are members of a team in its scope. The EXISTS
--    runs under the RLS of team_member, whose policy carries the team
--    predicate.
-- 4. desk_booking: active bookings of one desk must not overlap (exclusion
--    constraint, MGT-05). Existing overlaps are resolved by cancelling the
--    later booking, which is reported.

-- ─── 1. ticket ─────────────────────────────────────────────────────────────────

DROP POLICY ticket_tenant_isolation ON ticket;
CREATE POLICY ticket_tenant_isolation ON ticket
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.team_scope', true), '') IS NULL OR team_id IS NULL
             OR team_id = ANY (string_to_array(current_setting('app.team_scope', true), ',')::uuid[]))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.team_scope', true), '') IS NULL
             OR team_id = ANY (string_to_array(current_setting('app.team_scope', true), ',')::uuid[]))
    );

-- ─── 2. desk and desk_booking ──────────────────────────────────────────────────

ALTER TABLE desk ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;
ALTER TABLE desk_booking ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;

CREATE FUNCTION derive_desk_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    IF NEW.room_id IS NOT NULL THEN
        SELECT l.client_id, l.site_id INTO NEW.client_id, NEW.site_id FROM location l WHERE l.id = NEW.room_id;
    END IF;
    RETURN NEW;
END
$$;

CREATE FUNCTION derive_desk_booking_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    SELECT d.client_id, d.site_id INTO NEW.client_id, NEW.site_id FROM desk d WHERE d.id = NEW.desk_id;
    RETURN NEW;
END
$$;

CREATE FUNCTION propagate_room_desk_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE desk SET room_id = room_id WHERE room_id = NEW.id;
    RETURN NULL;
END
$$;

CREATE FUNCTION propagate_desk_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE desk_booking SET desk_id = desk_id WHERE desk_id = NEW.id;
    RETURN NULL;
END
$$;

CREATE TRIGGER desk_scope BEFORE INSERT OR UPDATE ON desk
    FOR EACH ROW EXECUTE FUNCTION derive_desk_scope();
CREATE TRIGGER desk_booking_scope BEFORE INSERT OR UPDATE ON desk_booking
    FOR EACH ROW EXECUTE FUNCTION derive_desk_booking_scope();
CREATE TRIGGER location_room_desk_scope_propagation AFTER UPDATE ON location
    FOR EACH ROW
    WHEN (NEW.kind = 'room' AND (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id))
    EXECUTE FUNCTION propagate_room_desk_scope();
CREATE TRIGGER desk_scope_propagation AFTER UPDATE ON desk
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_desk_scope();

UPDATE desk SET room_id = room_id;
UPDATE desk_booking SET desk_id = desk_id;

CREATE INDEX idx_desk_client ON desk (client_id) WHERE client_id IS NOT NULL;
CREATE INDEX idx_desk_booking_client ON desk_booking (client_id) WHERE client_id IS NOT NULL;

DO $$
DECLARE
    entry RECORD;
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES ('desk', 'desk_isolation'), ('desk_booking', 'desk_booking_isolation')) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('DROP POLICY %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format($p$
            CREATE POLICY %1$I ON %2$I
            USING (
                organization_id = current_setting('app.org_id')::UUID
                AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL
                     OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
                AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id IS NULL
                     OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
            )
            WITH CHECK (
                organization_id = current_setting('app.org_id')::UUID
                AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
                     OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
                AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
                     OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
            )$p$, entry.policy_name, entry.table_name);
    END LOOP;
END
$$;

-- ─── 3. training_assignment ────────────────────────────────────────────────────

DROP POLICY training_assignment_isolation ON training_assignment;
CREATE POLICY training_assignment_isolation ON training_assignment
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.team_scope', true), '') IS NULL
             OR EXISTS (SELECT 1 FROM team_member tm WHERE tm.user_id = training_assignment.user_id))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.team_scope', true), '') IS NULL
             OR EXISTS (SELECT 1 FROM team_member tm WHERE tm.user_id = training_assignment.user_id))
    );

-- ─── 4. Overlapping bookings ───────────────────────────────────────────────────

DO $$
DECLARE
    cancelled INTEGER;
BEGIN
    WITH overlapping AS (
        SELECT later.id
          FROM desk_booking later
          JOIN desk_booking earlier
            ON earlier.desk_id = later.desk_id
           AND earlier.id <> later.id
           AND earlier.status = 'active'
           AND tstzrange(earlier.starts_at, earlier.ends_at) && tstzrange(later.starts_at, later.ends_at)
           AND (earlier.created_at, earlier.id) < (later.created_at, later.id)
         WHERE later.status = 'active'
    )
    UPDATE desk_booking SET status = 'cancelled' WHERE id IN (SELECT id FROM overlapping);
    GET DIAGNOSTICS cancelled = ROW_COUNT;
    IF cancelled > 0 THEN
        RAISE NOTICE 'cancelled % overlapping desk bookings', cancelled;
    END IF;
END
$$;

ALTER TABLE desk_booking ADD CONSTRAINT desk_booking_no_overlap
    EXCLUDE USING gist (desk_id WITH =, tstzrange(starts_at, ends_at) WITH &&)
    WHERE (status = 'active');
