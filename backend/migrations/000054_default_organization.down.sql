-- Revert migration 000054.
--
-- Only remove the seeded default organization when it is still empty, so a
-- rollback never deletes tenant data that accumulated after the first login.
-- The cascading delete would otherwise drop clients, sites, users and every
-- other FK-bound row of that tenant.

DELETE FROM organization
WHERE id = '00000000-0000-0000-0000-000000000001'
  AND NOT EXISTS (SELECT 1 FROM app_user WHERE organization_id = organization.id)
  AND NOT EXISTS (SELECT 1 FROM client WHERE organization_id = organization.id);
