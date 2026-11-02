-- Migration 000189: an enrollment code keeps its site (WP-701, F-430;
-- LOC-11, E-71, COL-04).
--
-- 000187 bound collector_enrollment_code to the site of its creator with
-- ON DELETE CASCADE: deleting the site removed the codes bound to it
-- silently, although no delete of a location cascades and a site keeps the
-- objects bound to it (000157, E-71 option 1). The site foreign key is
-- RESTRICT like agent_enrollment_token_site_id_fkey: the API refuses the
-- delete with 409 'collector_enrollment_code' while a code that can still be
-- redeemed (unused and not expired) is bound to the site; expired and used
-- codes do not count and are removed in the delete transaction
-- (locations/dependencies.go). The foreign key is the backstop for codes
-- the caller cannot see.
--
-- The client foreign key stays CASCADE like agent_enrollment_token's: the
-- delete of a client (DELETE /clients/{id}) removes its sites and their
-- codes with it.

ALTER TABLE collector_enrollment_code DROP CONSTRAINT collector_enrollment_code_site_fkey,
    ADD CONSTRAINT collector_enrollment_code_site_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site(id, organization_id) ON DELETE RESTRICT;
