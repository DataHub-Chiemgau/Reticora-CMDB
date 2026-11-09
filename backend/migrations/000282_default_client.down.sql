-- Migration 000282 down: the trigger and its function go. The default
-- clients of the backfill and of the trigger stay: sites, racks and other
-- objects may reference them by now, and the schema is the same without them.

DROP TRIGGER IF EXISTS trg_organization_default_client ON organization;
DROP FUNCTION IF EXISTS organization_default_client();
