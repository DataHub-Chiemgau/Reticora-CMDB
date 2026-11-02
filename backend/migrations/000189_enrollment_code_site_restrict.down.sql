-- Reverts 000189: the delete rule of 000187.

ALTER TABLE collector_enrollment_code DROP CONSTRAINT collector_enrollment_code_site_fkey,
    ADD CONSTRAINT collector_enrollment_code_site_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site(id, organization_id) ON DELETE CASCADE;
