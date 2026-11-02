-- Reverts WP-639: the foreign keys of the modules desk, key and training name
-- the referenced row only again (migration 000050).

ALTER TABLE training_assignment DROP CONSTRAINT training_assignment_user_id_fkey,
    ADD CONSTRAINT training_assignment_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user (id),
    DROP CONSTRAINT training_assignment_training_id_fkey,
    ADD CONSTRAINT training_assignment_training_id_fkey FOREIGN KEY (training_id) REFERENCES training (id) ON DELETE CASCADE;
ALTER TABLE key_assignment DROP CONSTRAINT key_assignment_assigned_to_fkey,
    ADD CONSTRAINT key_assignment_assigned_to_fkey FOREIGN KEY (assigned_to) REFERENCES app_user (id),
    DROP CONSTRAINT key_assignment_key_item_id_fkey,
    ADD CONSTRAINT key_assignment_key_item_id_fkey FOREIGN KEY (key_item_id) REFERENCES key_item (id) ON DELETE CASCADE;
ALTER TABLE desk_booking DROP CONSTRAINT desk_booking_user_id_fkey,
    ADD CONSTRAINT desk_booking_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user (id),
    DROP CONSTRAINT desk_booking_desk_id_fkey,
    ADD CONSTRAINT desk_booking_desk_id_fkey FOREIGN KEY (desk_id) REFERENCES desk (id) ON DELETE CASCADE;
ALTER TABLE desk DROP CONSTRAINT desk_room_id_fkey,
    ADD CONSTRAINT desk_room_id_fkey FOREIGN KEY (room_id) REFERENCES room (id) ON DELETE SET NULL;

ALTER TABLE training DROP CONSTRAINT training_id_organization_key;
ALTER TABLE key_item DROP CONSTRAINT key_item_id_organization_key;
ALTER TABLE desk DROP CONSTRAINT desk_id_organization_key;
ALTER TABLE room DROP CONSTRAINT room_id_organization_key;
ALTER TABLE app_user DROP CONSTRAINT app_user_id_organization_key;
