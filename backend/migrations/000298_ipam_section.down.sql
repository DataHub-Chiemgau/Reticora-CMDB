-- Reverts 000298: subnet.section_id, the default-section triggers and
-- functions and the table ipam_section go.
DROP TRIGGER trg_subnet_default_section ON subnet;
DROP FUNCTION subnet_default_section();
DROP INDEX idx_subnet_org_section;
ALTER TABLE subnet DROP CONSTRAINT subnet_section_fkey;
ALTER TABLE subnet DROP COLUMN section_id;
DROP FUNCTION default_section_of(uuid);
DROP TRIGGER trg_organization_default_section ON organization;
DROP FUNCTION organization_default_section();
DROP TABLE ipam_section;
DROP FUNCTION ipam_section_protect_default();
