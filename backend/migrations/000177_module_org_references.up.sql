-- Migration 000177: the references of the modules desk, key and training stay
-- in their organization (WP-639, TEN-06, TEN-09, MGT-03; F-366).
--
-- The foreign keys of migration 000050 name the referenced row only
-- (desk_booking.user_id, key_assignment.assigned_to and
-- training_assignment.user_id → app_user(id), training_assignment.training_id
-- → training(id) ON DELETE CASCADE). Like every foreign key they are checked
-- without row level security: an organization-A principal booked a desk for
-- a user of organization B, handed a key to him and assigned a course of
-- organization B (201 each), 201 against 404 for an unknown id told A that
-- the id exists in another organization, and the deletion of the course by
-- organization B deleted the assignment of organization A.
--
-- Every foreign key of the module tables to app_user or to another tenant
-- table now includes the organization (like client in 000136): the
-- referenced row must belong to the organization of the referencing row, a
-- reference into another organization fails like an unknown id (23503, which
-- the handlers answer like an unknown id). The constraint names and the
-- ON DELETE actions stay. The referenced tables get the unique key
-- (id, organization_id) the composite foreign keys need. The policies are
-- unchanged. key_assignment.issued_by is a surrogate of the issuing user
-- without reference (like job.created_by) and stays so.

-- Existing rows must already follow the rule; the migration stops with the
-- list of offending rows instead of changing or deleting them (they have to
-- be corrected by hand).
DO $$
DECLARE
    crossing text;
BEGIN
    SELECT string_agg(c.r, ', ' ORDER BY c.r) INTO crossing FROM (
        SELECT 'desk ' || d.id || ' room_id' FROM desk d JOIN room x ON x.id = d.room_id
         WHERE x.organization_id <> d.organization_id
        UNION ALL
        SELECT 'desk_booking ' || b.id || ' desk_id' FROM desk_booking b JOIN desk x ON x.id = b.desk_id
         WHERE x.organization_id <> b.organization_id
        UNION ALL
        SELECT 'desk_booking ' || b.id || ' user_id' FROM desk_booking b JOIN app_user x ON x.id = b.user_id
         WHERE x.organization_id <> b.organization_id
        UNION ALL
        SELECT 'key_assignment ' || a.id || ' key_item_id' FROM key_assignment a JOIN key_item x ON x.id = a.key_item_id
         WHERE x.organization_id <> a.organization_id
        UNION ALL
        SELECT 'key_assignment ' || a.id || ' assigned_to' FROM key_assignment a JOIN app_user x ON x.id = a.assigned_to
         WHERE x.organization_id <> a.organization_id
        UNION ALL
        SELECT 'training_assignment ' || a.id || ' training_id' FROM training_assignment a JOIN training x ON x.id = a.training_id
         WHERE x.organization_id <> a.organization_id
        UNION ALL
        SELECT 'training_assignment ' || a.id || ' user_id' FROM training_assignment a JOIN app_user x ON x.id = a.user_id
         WHERE x.organization_id <> a.organization_id
    ) AS c(r);
    IF crossing IS NOT NULL THEN
        RAISE EXCEPTION 'rows referencing another organization, correct them before migration 000177: %', crossing;
    END IF;
END
$$;

ALTER TABLE app_user ADD CONSTRAINT app_user_id_organization_key UNIQUE (id, organization_id);
ALTER TABLE room ADD CONSTRAINT room_id_organization_key UNIQUE (id, organization_id);
ALTER TABLE desk ADD CONSTRAINT desk_id_organization_key UNIQUE (id, organization_id);
ALTER TABLE key_item ADD CONSTRAINT key_item_id_organization_key UNIQUE (id, organization_id);
ALTER TABLE training ADD CONSTRAINT training_id_organization_key UNIQUE (id, organization_id);

ALTER TABLE desk DROP CONSTRAINT desk_room_id_fkey,
    ADD CONSTRAINT desk_room_id_fkey FOREIGN KEY (room_id, organization_id) REFERENCES room (id, organization_id) ON DELETE SET NULL (room_id);
ALTER TABLE desk_booking DROP CONSTRAINT desk_booking_desk_id_fkey,
    ADD CONSTRAINT desk_booking_desk_id_fkey FOREIGN KEY (desk_id, organization_id) REFERENCES desk (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT desk_booking_user_id_fkey,
    ADD CONSTRAINT desk_booking_user_id_fkey FOREIGN KEY (user_id, organization_id) REFERENCES app_user (id, organization_id);
ALTER TABLE key_assignment DROP CONSTRAINT key_assignment_key_item_id_fkey,
    ADD CONSTRAINT key_assignment_key_item_id_fkey FOREIGN KEY (key_item_id, organization_id) REFERENCES key_item (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT key_assignment_assigned_to_fkey,
    ADD CONSTRAINT key_assignment_assigned_to_fkey FOREIGN KEY (assigned_to, organization_id) REFERENCES app_user (id, organization_id);
ALTER TABLE training_assignment DROP CONSTRAINT training_assignment_training_id_fkey,
    ADD CONSTRAINT training_assignment_training_id_fkey FOREIGN KEY (training_id, organization_id) REFERENCES training (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT training_assignment_user_id_fkey,
    ADD CONSTRAINT training_assignment_user_id_fkey FOREIGN KEY (user_id, organization_id) REFERENCES app_user (id, organization_id);
