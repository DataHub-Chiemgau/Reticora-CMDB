-- Down of 000226: the delete triggers and their function, the three field
-- permission tables with their indexes, triggers and policies, then the
-- unique keys of role and custom_role this migration added.
-- team_id_organization_key (000150) stays.

DROP TRIGGER trg_ci_relationship_object_grants ON ci_relationship;
DROP TRIGGER trg_document_object_grants ON document;
DROP TRIGGER trg_ticket_object_grants ON ticket;
DROP TRIGGER trg_asset_object_grants ON asset;
DROP TRIGGER trg_ci_object_grants ON ci;
DROP FUNCTION delete_object_grants();

DROP TABLE field_sensitivity;
DROP TABLE object_grant;
DROP TABLE field_grant;

ALTER TABLE custom_role DROP CONSTRAINT custom_role_id_organization_key;
ALTER TABLE role DROP CONSTRAINT role_id_organization_key;
